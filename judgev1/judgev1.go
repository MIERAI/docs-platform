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
const SchemaVersion = "1.3"

// Task types.
const (
	TaskClaimContradiction  = "claim_contradiction"
	TaskClaimIncomplete     = "claim_incomplete"
	TaskChangeContradiction = "change_contradiction"
	TaskDraftEdit           = "draft_edit"
	// TaskRepoDrift (1.2, agentic): build the repository's drift list from its history.
	TaskRepoDrift = "repo_drift"
	// TaskClaimFacts (1.2, agentic): check one documentation line fact by fact.
	TaskClaimFacts = "claim_facts"
	// TaskDocFacts (1.2, agentic): check up to a few lines of one document (claims[]) at once.
	TaskDocFacts = "doc_facts"
	// TaskRuleMutant (1.3, structured): break one business rule in its hand-written code with
	// the smallest edit; the core applies it and lets the code's owner run its examples.
	TaskRuleMutant = "rule_mutant"
	// TaskDraftExample (1.3, structured): propose an example for a design that pins a rule
	// down; the core keeps it only when the owner's acceptance proves it.
	TaskDraftExample = "draft_example"
	// TaskDraftRule (1.3, structured): propose the smallest rewording of a design's rule
	// that the code contradicts; the core keeps it only when the owner's acceptance passes.
	TaskDraftRule = "draft_rule"
)

// Structured reports whether a task's answer is a structured object (Response.Answer) from
// which the core derives the verdict itself, rather than a yes/no verdict with a quote.
func Structured(task string) bool {
	switch task {
	case TaskRepoDrift, TaskClaimFacts, TaskDocFacts, TaskRuleMutant, TaskDraftExample, TaskDraftRule:
		return true
	}
	return false
}

// Answer parse outcomes (Response.AnswerParse).
const (
	// ParseSchema: the provider's structured output (JSON Schema enforced) was used as is.
	ParseSchema = "schema"
	// ParseFallback: the object was found in free text by the tolerant parser.
	ParseFallback = "fallback"
	// ParseRepaired: the tolerant parser had to repair the text (stray closing braces).
	ParseRepaired = "repaired"
	// ParseFailed: no answer object could be read (the response carries error bad_reply).
	ParseFailed = "failed"
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
	// Claims (1.2, doc_facts) are the lines under test of one document; Claim is the first.
	Claims []Claim `json:"claims,omitempty"`
}

// ToolSpec announces the read-only tools of docs-platform/judge-tools/v1 for one request.
type ToolSpec struct {
	Contract string     `json:"contract"` // "judge-tools/v1"
	Allowed  []string   `json:"allowed"`
	Limits   ToolLimits `json:"limits"`
	// Definitions (1.1 of judge-tools/v1) describe each allowed tool for the model: name,
	// description and the JSON Schema of its arguments.
	Definitions []ToolDef `json:"definitions,omitempty"`
}

// ToolDef describes one tool to a model.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
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
	// MaxTurns (1.2, agentic) bounds the model turns of one request.
	MaxTurns int `json:"max_turns,omitempty"`
	// MaxRequestUSD (1.2, agentic) bounds the cost of this one request; reaching it ends the
	// request without an answer, never the run.
	MaxRequestUSD *float64 `json:"max_request_usd,omitempty"`
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
	// Answer (1.2) is the model's structured answer object for structured tasks (repo_drift,
	// claim_facts, doc_facts). The provider passes it through; the core validates every piece
	// of evidence in it and derives the verdict itself.
	Answer json.RawMessage `json:"answer,omitempty"`
	// AnswerParse (1.2) says how Answer was obtained: schema, fallback, repaired or failed.
	AnswerParse string `json:"answer_parse,omitempty"`
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
	// judge-tools/v1 1.1
	ToolSearch         = "search"
	ToolGlob           = "glob"
	ToolGitChanges     = "git_changes"
	ToolChangesSince   = "changes_since"
	ToolFindReferences = "find_references"
	ToolCheckQuote     = "check_quote"
	// judge-tools/v1 1.2
	ToolOutline     = "outline"
	ToolConfigValue = "config_value"
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
	// Text (judge-tools/v1 1.2) is the same result rendered for a model: numbered file lines
	// ("12: text"), "path:line: text" matches, one entry per line, no escaping. Providers
	// should show the model Text, not Content: line numbers must be read, never counted.
	Text string `json:"text,omitempty"`
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
	case TaskRepoDrift, TaskClaimFacts, TaskDocFacts:
		if r.Mode != ModeAgentic {
			return fmt.Errorf("judge/v1 request: task %s needs mode agentic", r.Task.Type)
		}
		if r.Task.Type == TaskDocFacts && len(r.Claims) == 0 {
			return errors.New("judge/v1 request: doc_facts needs claims")
		}
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
