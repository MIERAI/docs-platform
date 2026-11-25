package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/judgev1"
)

// ClaudeCodeConfig configures the claude-code provider.
type ClaudeCodeConfig struct {
	Program        string // absolute path of the claude CLI (default: found on PATH)
	Model          string
	Effort         string
	PassEnv        []string
	Timeout        time.Duration
	MaxConcurrency int
}

// ClaudeCode answers each request with one non-interactive `claude -p` run using the
// operator's own Claude Code login (locally) or the token variable named in pass_env (CI).
// The run has no tools, no MCP servers, no settings, no slash commands and no session on
// disk; it starts in an empty temporary directory so no project instructions are picked up;
// the prompt goes in on stdin and the answer comes back as JSON validated against the reply
// schema. docs-platform never reads, prints or copies the credential.
type ClaudeCode struct {
	cfg ClaudeCodeConfig
	sem chan struct{}
}

// claudeCodeOverhead is the CLI's own fixed prompt (tokens), measured with --system-prompt
// and no tools; used only for estimates.
const claudeCodeOverhead = 1300

// maxCLIOutput bounds what one CLI run may print.
const maxCLIOutput = 8 << 20

// NewClaudeCode validates cfg.
func NewClaudeCode(cfg ClaudeCodeConfig) (*ClaudeCode, error) {
	if cfg.Program == "" {
		p, err := exec.LookPath("claude")
		if err != nil {
			return nil, errors.New("claude-code provider: the claude CLI is not on PATH; set ai.<task>.claude_code.program")
		}
		cfg.Program = p
	}
	if !filepath.IsAbs(cfg.Program) {
		abs, err := filepath.Abs(cfg.Program)
		if err != nil {
			return nil, fmt.Errorf("claude-code provider: %w", err)
		}
		cfg.Program = abs
	}
	if cfg.Model == "" {
		return nil, errors.New("claude-code provider: model is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTaskTimeout
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = DefaultCloudConcurrent
	}
	return &ClaudeCode{cfg: cfg, sem: make(chan struct{}, cfg.MaxConcurrency)}, nil
}

// Name implements Provider.
func (*ClaudeCode) Name() string { return instance.ProviderClaudeCode }

// Model implements Provider.
func (c *ClaudeCode) Model() string { return c.cfg.Model }

// Cloud implements Provider.
func (*ClaudeCode) Cloud() bool { return true }

// Billing implements Provider: the operator's subscription (cost figures are notional).
func (*ClaudeCode) Billing() string { return judgev1.BillingSubscription }

// Overhead is the CLI's fixed prompt in tokens (for estimates).
func (*ClaudeCode) Overhead() int { return claudeCodeOverhead }

// Modes implements Provider: snippet mode only (agentic mode is being evaluated separately).
func (*ClaudeCode) Modes() []string { return snippetOnly }

// Close implements Provider.
func (*ClaudeCode) Close() error { return nil }

// Args is the argv (without the program) of one run.
func (c *ClaudeCode) Args(req *judgev1.Request) []string {
	args := []string{
		"-p", "--model", c.cfg.Model, "--output-format", "json",
		"--json-schema", string(req.Prompt.ReplySchema), "--system-prompt", req.Prompt.System,
		"--tools", "", "--no-session-persistence", "--disable-slash-commands", "--strict-mcp-config",
		"--setting-sources", "",
	}
	if c.cfg.Effort != "" {
		args = append(args, "--effort", c.cfg.Effort)
	}
	if r := req.Limits.BudgetRemainingUSD; r != nil && *r > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.4f", *r))
	}
	return args
}

// Judge implements Provider.
func (c *ClaudeCode) Judge(ctx context.Context, req *judgev1.Request) (*judgev1.Response, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("claude-code: %w", ctx.Err())
	}
	defer func() { <-c.sem }()
	dir, err := os.MkdirTemp("", "docsp-claude-")
	if err != nil {
		return nil, fmt.Errorf("claude-code: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, c.cfg.Program, c.Args(req)...) //nolint:gosec // operator-configured absolute path, argv vector, no shell
	cmd.Dir = dir
	cmd.Env = childEnv(c.cfg.PassEnv)
	cmd.Stdin = strings.NewReader(req.Prompt.User)
	var out bytes.Buffer
	cmd.Stdout = &capped{w: &out, n: maxCLIOutput}
	cmd.Stderr = io.Discard // never surfaced: it may mention account details
	runErr := cmd.Run()
	m := judgev1.Model{Provider: c.Name(), ID: c.cfg.Model}
	if ctx.Err() != nil {
		return judgev1.Failure(req, m, judgev1.ErrCodeTimeout, fmt.Sprintf("claude -p did not finish within %s", c.cfg.Timeout)), nil
	}
	resp := ParseClaudeOutput(req, out.Bytes(), m)
	resp.LatencyMS = time.Since(start).Milliseconds()
	if runErr != nil && resp.Error == nil {
		resp = judgev1.Failure(req, m, judgev1.ErrCodeTransport, "claude -p exited with an error")
	}
	return resp, nil
}

// cliResult is the part of `claude -p --output-format json` docs-platform reads. Other
// fields (session ids, credential metadata) are ignored and never logged.
type cliResult struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	TotalCostUSD     float64         `json:"total_cost_usd"`
	Usage            struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage    map[string]json.RawMessage `json:"modelUsage"`
	APIErrorState any                        `json:"api_error_status"`
}

// ParseClaudeOutput maps the CLI's JSON result to a judge/v1 response. Anything unexpected
// becomes a bad_reply failure; error messages never include the CLI output.
func ParseClaudeOutput(req *judgev1.Request, b []byte, m judgev1.Model) *judgev1.Response {
	var r cliResult
	if err := json.Unmarshal(bytes.TrimSpace(b), &r); err != nil || r.Type != "result" {
		return judgev1.Failure(req, m, judgev1.ErrCodeBadReply, "claude -p printed no JSON result")
	}
	if len(r.ModelUsage) == 1 {
		for id := range r.ModelUsage {
			m.ID = id
		}
	}
	if r.IsError || r.Subtype != "success" {
		code := judgev1.ErrCodeTransport
		if strings.Contains(r.Subtype, "budget") {
			code = judgev1.ErrCodeBudget
		}
		return judgev1.Failure(req, m, code, "claude -p reported "+cutASCII(r.Subtype, 60))
	}
	text := r.Result
	if len(r.StructuredOutput) > 0 && string(r.StructuredOutput) != "null" {
		text = string(r.StructuredOutput)
	}
	a, err := judgev1.ParseAnswer(req.Task.Type, text)
	if err != nil {
		return judgev1.Failure(req, m, judgev1.ErrCodeBadReply, err.Error())
	}
	return judgev1.NewResponse(req, a, m, judgev1.Usage{
		InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens,
		CacheReadInputTokens: r.Usage.CacheReadInputTokens, CacheCreationInputTokens: r.Usage.CacheCreationInputTokens,
		CostUSD: r.TotalCostUSD, Billing: judgev1.BillingSubscription,
	})
}

func cutASCII(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= n {
			break
		}
		if r >= 'a' && r <= 'z' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type capped struct {
	w io.Writer
	n int
}

func (c *capped) Write(p []byte) (int, error) {
	if len(p) > c.n {
		return 0, errors.New("output limit exceeded")
	}
	c.n -= len(p)
	n, err := c.w.Write(p)
	if err != nil {
		return n, fmt.Errorf("write: %w", err)
	}
	return n, nil
}
