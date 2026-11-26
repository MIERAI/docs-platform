// Package judge is the model side of docs-platform. A Provider answers docs-platform/judge/v1
// requests (contracts/judge-v1-*.schema.json); which provider and model answer which task
// (judge, draft) comes only from instance configuration, so a model is swapped by editing that
// file. Built-in providers: none, ollama (local), claude-code (the Claude Code CLI with the
// operator's own login), anthropic (the Messages API, out of process) and exec (any program
// speaking judge/v1). Every provider is wrapped by a Guard that enforces data classification,
// the per-run budget, the contract, and verbatim-quote verification.
package judge

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/judgev1"
)

// Provider answers judge/v1 requests. Implementations must be safe for concurrent use.
type Provider interface {
	Name() string
	Model() string
	// Cloud reports whether request content leaves the operator's machines.
	Cloud() bool
	// Billing is none, api or subscription.
	Billing() string
	// Judge answers one request. An error means no response at all (transport, process);
	// a model-level failure is a response with Error set.
	Judge(ctx context.Context, r *judgev1.Request) (*judgev1.Response, error)
	// Modes lists the judge/v1 modes the provider supports (snippet, agentic).
	Modes() []string
	Close() error
}

var snippetOnly = []string{judgev1.ModeSnippet}

// Default limits.
const (
	DefaultMaxRequests     = 300
	DefaultMaxTokens       = 3_000_000
	DefaultAPIMaxUSD       = 5.0
	DefaultMaxOutputTokens = 8000
	DefaultTaskTimeout     = 180 * time.Second
	DefaultCloudConcurrent = 2
)

// New returns the provider of one task, or nil for "none".
func New(t instance.Task) (Provider, error) {
	timeout := time.Duration(t.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultTaskTimeout
	}
	conc := t.MaxConcurrency
	if conc <= 0 {
		conc = DefaultCloudConcurrent
	}
	switch t.Provider {
	case "", instance.ProviderNone:
		return nil, nil
	case instance.ProviderOllama:
		o, err := NewOllama(OllamaConfig{
			Endpoint: t.Ollama.Endpoint, Model: t.Model, Timeout: time.Duration(t.TimeoutSeconds) * time.Second,
			Retries: t.Ollama.Retries, MaxConcurrency: t.MaxConcurrency, Seed: t.Ollama.Seed,
			NumCtx: t.Ollama.NumCtx, NumPredict: t.Ollama.NumPredict,
		})
		if err != nil {
			return nil, err
		}
		return o, nil
	case instance.ProviderClaudeCode:
		return NewClaudeCode(ClaudeCodeConfig{
			Program: t.ClaudeCode.Program, Model: t.Model, Effort: t.Effort, AgenticEffort: t.Agentic.Effort, PassEnv: t.ClaudeCode.PassEnv,
			Timeout: timeout, MaxConcurrency: conc,
		})
	case instance.ProviderAnthropic:
		return NewExec(anthropicExec(t, timeout, conc))
	case instance.ProviderExec:
		return NewExec(ExecConfig{
			Name: "exec", Model: t.Model, Command: t.Exec.Command, PassEnv: t.Exec.PassEnv, Modes: t.Exec.Modes,
			Cloud: !t.Exec.Local, Billing: judgev1.BillingAPI, Timeout: timeout, MaxConcurrency: conc,
		})
	}
	return nil, fmt.Errorf("unknown provider %q", t.Provider)
}

// anthropicExec is the anthropic preset of the exec provider: the Messages API adapter
// (cmd/docsp-judge-anthropic, its own module) receives the model, limits and where the key
// is, never the key itself.
func anthropicExec(t instance.Task, timeout time.Duration, conc int) ExecConfig {
	a := t.Anthropic
	argv := []string{a.Program, "-model", t.Model}
	if t.Effort != "" {
		argv = append(argv, "-effort", t.Effort)
	}
	if a.MaxRetries > 0 {
		argv = append(argv, "-max-retries", strconv.Itoa(a.MaxRetries))
	}
	if a.Fallbacks != "" {
		argv = append(argv, "-fallbacks", a.Fallbacks)
	}
	var pass []string
	if a.APIKeyFile != "" {
		argv = append(argv, "-api-key-file", a.APIKeyFile)
	} else {
		argv = append(argv, "-api-key-env", a.APIKeyEnv)
		pass = append(pass, a.APIKeyEnv)
	}
	return ExecConfig{
		Name: instance.ProviderAnthropic, Model: t.Model, Command: argv, PassEnv: pass, Cloud: true,
		Billing: judgev1.BillingAPI, Timeout: timeout, MaxConcurrency: conc,
	}
}

// childEnv is the minimal environment of a provider process: PATH, HOME and USER (a CLI
// needs them to find its own login), a fixed locale, and the named variables passed through
// unchanged. docs-platform never reads or logs their values.
func childEnv(pass []string) []string {
	env := []string{"LC_ALL=C.UTF-8"}
	for _, k := range append([]string{"PATH", "HOME", "USER", "TMPDIR"}, pass...) {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// dryRun answers a request without calling a model: verdict unknown, usage estimated.
func dryRun(req *judgev1.Request, m judgev1.Model, billing string, price *instance.Price) *judgev1.Response {
	in, out := EstimateUsage(req, 0)
	r := judgev1.Failure(req, m, judgev1.ErrCodeConfig, "dry run")
	r.Error = nil
	r.Usage = judgev1.Usage{InputTokens: in, OutputTokens: out, Estimated: true, Billing: billing}
	if price != nil {
		r.Usage.CostUSD = price.Cost(in, out, 0)
	}
	return r
}

// Output token assumptions for estimates (answer plus adaptive thinking, which current strong
// models cannot turn off). Deliberately on the high side.
const (
	estJudgeOutput = 700
	estDraftOutput = 1000
	// agentic: tokens per tool step (result plus the turn around it) and the final answer
	estToolTokens     = 2500
	estAgenticOutput  = 2500
	estDriftNewTokens = 1500
	estDriftOutput    = 6000
)

// EstimateUsage estimates input and output tokens of one request; overhead is the provider's
// own fixed prompt (e.g. a CLI's system context).
func EstimateUsage(req *judgev1.Request, overhead int) (int, int) {
	in := judgev1.EstimateTokens(req.Prompt.System) + judgev1.EstimateTokens(req.Prompt.User) +
		judgev1.EstimateTokens(string(req.Prompt.ReplySchema)) + overhead
	out := estJudgeOutput
	if req.Task.Type == judgev1.TaskDraftEdit {
		out = estDraftOutput
	}
	if req.Mode == judgev1.ModeAgentic {
		// every turn re-reads the conversation; tool results average a few thousand tokens
		in += agenticSteps(req) * estToolTokens
		out = estAgenticOutput
	}
	return in, out
}

// agenticEstimate splits an agentic request's expected tokens (for dry runs) into fresh
// input, cache reads (each turn re-reads the conversation so far, a cached prefix) and
// output. Calibrated against the prototype run the agentic design was evaluated with: about
// $0.04 per line and $0.15-0.25 per drift scan on Sonnet; a drift scan reads large commits
// (new context every turn) and writes a long list.
func agenticEstimate(req *judgev1.Request, overhead int) (int, int, int) {
	in := judgev1.EstimateTokens(req.Prompt.System) + judgev1.EstimateTokens(req.Prompt.User) +
		judgev1.EstimateTokens(string(req.Prompt.ReplySchema)) + overhead
	steps := agenticSteps(req)
	if req.Task.Type == judgev1.TaskRepoDrift {
		return in + steps*estDriftNewTokens, steps * estToolTokens, estDriftOutput
	}
	return in, steps * estToolTokens, estAgenticOutput
}

// agenticSteps is the step budget of an agentic request.
func agenticSteps(req *judgev1.Request) int {
	if req.Tools != nil && req.Tools.Limits.MaxSteps > 0 {
		return req.Tools.Limits.MaxSteps
	}
	if req.Limits.MaxTurns > 0 {
		return req.Limits.MaxTurns
	}
	return DefaultMaxSteps
}
