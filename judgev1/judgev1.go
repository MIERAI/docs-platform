// Package judgev1 holds the Go types of the docs-platform/judge/v1 contract
// (contracts/judge-v1-request.schema.json and contracts/judge-v1-response.schema.json) and the
// helpers a provider needs: parsing a model's answer object and filling a response. It uses
// only the standard library so that out-of-process providers (any module, any version) can
// import it; providers in other languages read the JSON Schemas instead.
package judgev1

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SchemaVersion of judge/v1 produced by this package.
const SchemaVersion = "1.1"

// Task types.
const (
	TaskClaimContradiction  = "claim_contradiction"
	TaskClaimIncomplete     = "claim_incomplete"
	TaskChangeContradiction = "change_contradiction"
	TaskDraftEdit           = "draft_edit"
)

// Modes: how a provider gets its evidence.
const (
	// ModeSnippet: the evidence is in the request (fixed snippets chosen by the core).
	ModeSnippet = "snippet"
	// ModeAgentic: the provider explores the inspected worktree through the read-only tools of
	// docs-platform/judge-tools/v1, executed by the core, and returns an evidence list.
	ModeAgentic = "agentic"
)

// Verdicts.
const (
	Yes     = "yes"
	No      = "no"
	Unknown = "unknown"
)

// Error codes.
const (
	ErrCodeBudget         = "budget"
	ErrCodeRefused        = "refused"
	ErrCodeRateLimited    = "rate_limited"
	ErrCodeTransport      = "transport"
	ErrCodeBadReply       = "bad_reply"
	ErrCodeTimeout        = "timeout"
	ErrCodeConfig         = "config"
	ErrCodeClassification = "classification"
)

// Billing kinds.
const (
	BillingNone         = "none"
	BillingAPI          = "api"
	BillingSubscription = "subscription"
)

// Request is one judge/v1 request.
type Request struct {
	SchemaVersion string     `json:"schema_version"`
	ID            string     `json:"id"`
	Task          Task       `json:"task"`
	Claim         Claim      `json:"claim"`
	Evidence      []Evidence `json:"evidence"`
	RequireQuote  bool       `json:"require_quote"`
	Prompt        Prompt     `json:"prompt"`
	Model         string     `json:"model,omitempty"`
	Limits        Limits     `json:"limits"`
	DryRun        bool       `json:"dry_run,omitempty"`
	// Mode is snippet (default) or agentic.
	Mode string `json:"mode,omitempty"`
	// Tools describes the tools available in agentic mode.
	Tools *ToolSpec `json:"tools,omitempty"`
}

// ToolSpec announces the read-only tools of docs-platform/judge-tools/v1 for one request.
type ToolSpec struct {
	Contract string     `json:"contract"` // "judge-tools/v1"
	Allowed  []string   `json:"allowed"`
	Limits   ToolLimits `json:"limits"`
}

// ToolLimits bound one agentic request.
type ToolLimits struct {
	MaxSteps       int `json:"max_steps"`
	MaxResultBytes int `json:"max_result_bytes"`
	MaxTotalBytes  int `json:"max_total_bytes"`
}

// Task says what is asked.
type Task struct {
	Type     string `json:"type"`
	Question string `json:"question,omitempty"`
}

// Claim is the documentation text under test (untrusted data).
type Claim struct {
	Repo string `json:"repo,omitempty"`
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text"`
}

// Evidence is one code snippet (untrusted data) with a stable id (E1, E2, ...).
type Evidence struct {
	ID   string `json:"id"`
	Repo string `json:"repo,omitempty"`
	Path string `json:"path"`
	From int    `json:"from,omitempty"`
	To   int    `json:"to,omitempty"`
	Text string `json:"text"`
}

// Prompt is the exact text the core wants sent.
type Prompt struct {
	Version     string          `json:"version"`
	Hash        string          `json:"hash"`
	System      string          `json:"system"`
	User        string          `json:"user"`
	ReplySchema json.RawMessage `json:"reply_schema"`
}

// Limits for one request.
type Limits struct {
	MaxOutputTokens       int      `json:"max_output_tokens,omitempty"`
	TimeoutSeconds        int      `json:"timeout_seconds,omitempty"`
	BudgetRemainingUSD    *float64 `json:"budget_remaining_usd,omitempty"`
	BudgetRemainingTokens *int     `json:"budget_remaining_tokens,omitempty"`
}

// Response is one judge/v1 response.
type Response struct {
	SchemaVersion   string   `json:"schema_version"`
	ID              string   `json:"id"`
	Verdict         string   `json:"verdict"`
	Quote           string   `json:"quote"`
	QuoteEvidenceID string   `json:"quote_evidence_id,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	Rationale       string   `json:"rationale,omitempty"`
	Draft           *Draft   `json:"draft,omitempty"`
	Model           Model    `json:"model"`
	PromptHash      string   `json:"prompt_hash"`
	Usage           Usage    `json:"usage"`
	LatencyMS       int64    `json:"latency_ms,omitempty"`
	Error           *Error   `json:"error,omitempty"`
	// EvidenceRefs is the agentic answer's evidence list: every quote must be found by the
	// core at path:line±2 in the inspected worktree, or the verdict is downgraded to unknown.
	EvidenceRefs []EvidenceRef `json:"evidence,omitempty"`
	// Transcript records the tool steps of an agentic answer (arguments and result sizes,
	// never result contents).
	Transcript []Step `json:"transcript,omitempty"`
}

// EvidenceRef is one quoted line of the inspected worktree.
type EvidenceRef struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Quote string `json:"quote"`
	Rev   string `json:"rev,omitempty"` // a commit when the quote is from git_show; "" = worktree
}

// Step is one tool call of an agentic answer.
type Step struct {
	N           int             `json:"n"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args"`
	ResultBytes int             `json:"result_bytes"`
	Error       string          `json:"error,omitempty"`
}

// Tool names of docs-platform/judge-tools/v1.
const (
	ToolListDir  = "list_dir"
	ToolReadFile = "read_file"
	ToolGrep     = "grep"
	ToolGitLog   = "git_log"
	ToolGitShow  = "git_show"
)

// ToolCall is a provider's request to run one tool (a line on the provider's stdout, in
// agentic mode, before its final response).
type ToolCall struct {
	Type string          `json:"type"` // "tool_call"
	ID   string          `json:"id"`   // the judge request id
	Call string          `json:"call"` // unique per request
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// ToolResult is the core's answer to a ToolCall (a line on the provider's stdin).
type ToolResult struct {
	Type      string          `json:"type"` // "tool_result"
	ID        string          `json:"id"`
	Call      string          `json:"call"`
	OK        bool            `json:"ok"`
	Content   json.RawMessage `json:"content,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// Draft is a proposed replacement text (never applied automatically).
type Draft struct {
	Text string `json:"text"`
}

// Model identifies who answered.
type Model struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
}

// Usage is the token and cost accounting of one call.
type Usage struct {
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens,omitempty"`
	CostUSD                  float64 `json:"cost_usd"`
	Estimated                bool    `json:"estimated,omitempty"`
	Billing                  string  `json:"billing,omitempty"`
}

// Error explains why no answer was produced.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Answer is the model's own answer object (the structured output the prompt asks for).
type Answer struct {
	Verdict         string   `json:"verdict"`
	Quote           string   `json:"quote"`
	QuoteEvidenceID string   `json:"quote_evidence_id"`
	Confidence      *float64 `json:"confidence"`
	Rationale       string   `json:"rationale"`
	Draft           string   `json:"draft"`
}

// ErrNoAnswer is returned when a model reply holds no answer object.
var ErrNoAnswer = errors.New("judge/v1: no answer object in model reply")

var (
	reVerdict = regexp.MustCompile(`(?i)"?(?:verdict|answer)"?\s*:\s*"?(yes|no|unknown)`)
	reEvID    = regexp.MustCompile(`^E[0-9]{1,3}$`)
)

// ParseAnswer reads a model's answer object from its raw text reply. It accepts the object
// alone, the object inside a Markdown code fence, or (last resort) a bare "verdict: yes"; any
// other text is ErrNoAnswer. Draft answers carry no verdict: a non-empty draft means yes.
func ParseAnswer(task, text string) (Answer, error) {
	s := strings.TrimSpace(text)
	if i := strings.Index(s, "{"); i >= 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			var a Answer
			if err := json.Unmarshal([]byte(s[i:j+1]), &a); err == nil {
				return normalize(task, a), nil
			}
		}
	}
	if task != TaskDraftEdit {
		if m := reVerdict.FindStringSubmatch(s); m != nil {
			return normalize(task, Answer{Verdict: m[1]}), nil
		}
	}
	return Answer{}, ErrNoAnswer
}

func normalize(task string, a Answer) Answer {
	a.Verdict = strings.ToLower(strings.TrimSpace(a.Verdict))
	if task == TaskDraftEdit {
		a.Verdict = Unknown
		if strings.TrimSpace(a.Draft) != "" {
			a.Verdict = Yes
		}
	}
	switch a.Verdict {
	case Yes, No, Unknown:
	default:
		a.Verdict = Unknown
	}
	if !reEvID.MatchString(a.QuoteEvidenceID) {
		a.QuoteEvidenceID = ""
	}
	if a.Confidence != nil && (*a.Confidence < 0 || *a.Confidence > 1) {
		a.Confidence = nil
	}
	return a
}

// NewResponse fills a response for req from a parsed answer.
func NewResponse(req *Request, a Answer, m Model, u Usage) *Response {
	r := &Response{
		SchemaVersion: SchemaVersion, ID: req.ID, Verdict: a.Verdict, Quote: a.Quote,
		QuoteEvidenceID: a.QuoteEvidenceID, Confidence: a.Confidence, Rationale: cut(a.Rationale, 5000),
		Model: m, PromptHash: req.Prompt.Hash, Usage: u,
	}
	if a.Verdict != Yes || !req.RequireQuote {
		r.Quote, r.QuoteEvidenceID = "", ""
	}
	r.Quote = cut(r.Quote, 5000)
	if req.Task.Type == TaskDraftEdit && a.Verdict == Yes {
		r.Draft = &Draft{Text: cut(a.Draft, 20000)}
	}
	return r
}

// Failure is a response that carries an error and no answer.
func Failure(req *Request, m Model, code, msg string) *Response {
	return &Response{
		SchemaVersion: SchemaVersion, ID: req.ID, Verdict: Unknown, Model: m,
		PromptHash: req.Prompt.Hash, Error: &Error{Code: code, Message: cut(msg, 2000)},
	}
}

// EstimateTokens is a deliberately conservative offline token estimate for text (about one
// token per 3 bytes of ASCII, one per CJK character); used by dry runs and budget checks.
func EstimateTokens(s string) int {
	n := 0
	ascii := 0
	for _, r := range s {
		if r < 0x80 {
			ascii++
			continue
		}
		n++
	}
	return n + (ascii+2)/3
}

// Validate does the cheap structural checks every provider should do before answering.
func (r *Request) Validate() error {
	switch {
	case r.ID == "":
		return errors.New("judge/v1 request: id is required")
	case r.Prompt.User == "" || r.Prompt.Hash == "":
		return errors.New("judge/v1 request: prompt.user and prompt.hash are required")
	}
	switch r.Mode {
	case "", ModeSnippet, ModeAgentic:
	default:
		return fmt.Errorf("judge/v1 request: unknown mode %q", r.Mode)
	}
	switch r.Task.Type {
	case TaskClaimContradiction, TaskClaimIncomplete, TaskChangeContradiction, TaskDraftEdit:
		return nil
	}
	return fmt.Errorf("judge/v1 request: unknown task type %q", r.Task.Type)
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
