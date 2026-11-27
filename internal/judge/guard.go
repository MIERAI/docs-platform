package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/judgev1"
)

// Sentinel errors of the Guard.
var (
	// ErrBudget: the task's per-run budget (requests, tokens or USD) is spent; no further
	// request is sent in this run.
	ErrBudget = errors.New("model budget exhausted")
	// ErrClassified: the request holds private content and the provider is a cloud provider.
	ErrClassified = errors.New("private content may not be sent to a cloud provider")
)

// Stats account for one task in one run.
type Stats struct {
	Requests       int `json:"requests"`
	Responses      int `json:"responses"`
	Errors         int `json:"errors"`
	BudgetStops    int `json:"budget_stops"`
	Refused        int `json:"classification_refused"`
	Yes            int `json:"yes"`
	No             int `json:"no"`
	Unknown        int `json:"unknown"`
	QuotesRejected int `json:"quotes_rejected"`
	// ParseFailures counts structured answers that could not be read at all (also counted in
	// Errors); ParseFallbacks those read only by the tolerant parser (fallback or repaired).
	ParseFailures  int     `json:"parse_failures"`
	ParseFallbacks int     `json:"parse_fallbacks"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CacheRead      int     `json:"cache_read_input_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	Estimated      bool    `json:"estimated"`
	latencies      []int64
}

// P50LatencyMS is the median latency of answered requests.
func (s Stats) P50LatencyMS() int64 {
	if len(s.latencies) == 0 {
		return 0
	}
	l := append([]int64{}, s.latencies...)
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	return l[len(l)/2]
}

// Refusal is a request refused by data classification.
type Refusal struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
}

// Outcome is a verified answer.
type Outcome struct {
	Response *judgev1.Response
	// Verdict after verification: a yes whose quote failed verification becomes unknown.
	Verdict       string
	QuoteRejected bool
	Quote         QuoteCheck
}

// Guard wraps a provider with the rules that hold for every provider.
type Guard struct {
	task     string
	p        Provider
	class    instance.Classification
	budget   instance.Budget
	price    *instance.Price
	overhead int
	schema   *jsonschema.Schema
	mu       sync.Mutex
	st       Stats
	refused  []Refusal
	// spendLogged: the spend was appended to SpendLogEnv (once per guard).
	spendLogged bool
}

// NewGuard wraps p (nil = none → nil guard) for task ("judge" or "draft").
func NewGuard(task string, p Provider, in *instance.Instance) (*Guard, error) {
	if p == nil {
		return nil, nil
	}
	sch, err := jsonschema.Compile(contracts.JudgeResponse)
	if err != nil {
		return nil, fmt.Errorf("judge response schema: %w", err)
	}
	t := in.AI.Judge
	if task == "draft" {
		t = in.AI.Draft
	}
	g := &Guard{task: task, p: p, class: in.Classification, budget: t.Budget, schema: sch}
	if pr, ok := in.PriceOf(p.Model()); ok {
		g.price = &pr
	}
	if g.budget.MaxRequests == 0 {
		g.budget.MaxRequests = DefaultMaxRequests
	}
	if g.budget.MaxTokens == 0 {
		g.budget.MaxTokens = DefaultMaxTokens
	}
	if g.budget.MaxUSD == 0 && p.Billing() == judgev1.BillingAPI {
		g.budget.MaxUSD = DefaultAPIMaxUSD
	}
	if cc, ok := p.(*ClaudeCode); ok {
		g.overhead = cc.Overhead()
	}
	return g, nil
}

// Name is provider:model.
func (g *Guard) Name() string { return g.p.Name() + ":" + g.p.Model() }

// Provider returns the wrapped provider.
func (g *Guard) Provider() Provider { return g.p }

// Stats returns a snapshot of the accounting.
func (g *Guard) Stats() Stats {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.st
	s.latencies = append([]int64{}, g.st.latencies...)
	return s
}

// Refusals lists the requests refused by classification.
func (g *Guard) Refusals() []Refusal {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Refusal{}, g.refused...)
}

// Close closes the provider.
func (g *Guard) Close() error {
	g.logSpend()
	if err := g.p.Close(); err != nil {
		return fmt.Errorf("close %s: %w", g.Name(), err)
	}
	return nil
}

// SpendLogEnv names a file every guard appends its spend to when it closes (one line: the
// provider and the USD), so a caller running several commands - a CI job against a monthly
// budget - can add up what they cost.
const SpendLogEnv = "DOCSP_SPEND_LOG"

func (g *Guard) logSpend() {
	p := os.Getenv(SpendLogEnv)
	if p == "" {
		return
	}
	g.mu.Lock()
	usd, logged := g.st.CostUSD, g.spendLogged
	g.spendLogged = true
	g.mu.Unlock()
	if logged {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the operator names the log
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s %.6f\n", g.Name(), usd)
	_ = f.Close()
}

// Judge sends one request through the rules: classification, budget, the provider, contract
// validation, accounting and quote verification.
func (g *Guard) Judge(ctx context.Context, req *judgev1.Request) (Outcome, error) {
	if err := g.checkMode(ctx, req); err != nil {
		return Outcome{}, err
	}
	if err := g.classify(req); err != nil {
		return Outcome{}, err
	}
	if req.DryRun {
		return g.estimate(req), nil
	}
	if err := g.reserve(req); err != nil {
		return Outcome{}, err
	}
	start := time.Now()
	resp, err := g.p.Judge(ctx, req)
	if err == nil {
		err = g.validate(req, resp)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.st.Errors++
		return Outcome{}, err
	}
	g.account(resp, time.Since(start))
	if req.Mode == judgev1.ModeAgentic {
		// In-process tools (exec, ollama) record here; an out-of-process tool server
		// (claude-code) hands its transcript back in the response.
		if tr := ToolsFrom(ctx).Transcript(); len(tr) > 0 || len(resp.Transcript) == 0 {
			resp.Transcript = tr
		}
	}
	if judgev1.Structured(req.Task.Type) {
		return g.structured(resp)
	}
	if resp.Error != nil {
		g.st.Errors++
		if resp.Error.Code == judgev1.ErrCodeBudget {
			g.st.BudgetStops++
			return Outcome{Response: resp, Verdict: judgev1.Unknown}, ErrBudget
		}
		return Outcome{Response: resp, Verdict: judgev1.Unknown}, nil
	}
	return g.verify(ctx, req, resp), nil
}

// structured accounts the answer of a structured task: the verdict stays unknown here (the
// caller derives it from the validated answer); parse outcomes are counted separately.
func (g *Guard) structured(resp *judgev1.Response) (Outcome, error) {
	o := Outcome{Response: resp, Verdict: judgev1.Unknown}
	switch resp.AnswerParse {
	case judgev1.ParseFailed:
		g.st.ParseFailures++
	case judgev1.ParseFallback, judgev1.ParseRepaired:
		g.st.ParseFallbacks++
	}
	if resp.Error != nil {
		g.st.Errors++
		if resp.AnswerParse == "" && resp.Error.Code == judgev1.ErrCodeBadReply {
			g.st.ParseFailures++
		}
		if resp.Error.Code == judgev1.ErrCodeBudget {
			g.st.BudgetStops++
			return o, ErrBudget
		}
		return o, nil
	}
	if len(resp.Answer) == 0 {
		g.st.Errors++
		g.st.ParseFailures++
		resp.Error = &judgev1.Error{Code: judgev1.ErrCodeBadReply, Message: "structured task without an answer object"}
	}
	g.st.Unknown++
	return o, nil
}

// checkMode refuses a mode the provider does not declare; agentic requests need tools in ctx.
func (g *Guard) checkMode(ctx context.Context, req *judgev1.Request) error {
	mode := req.Mode
	if mode == "" {
		mode = judgev1.ModeSnippet
	}
	if !slices.Contains(g.p.Modes(), mode) {
		return fmt.Errorf("judge: provider %s does not support %s mode (supports %v)", g.Name(), mode, g.p.Modes())
	}
	if mode == judgev1.ModeAgentic {
		t := ToolsFrom(ctx)
		if t == nil {
			return errors.New("judge: agentic mode needs the worktree tools (judge.WithTools)")
		}
		req.Tools = t.Spec()
	}
	return nil
}

func (g *Guard) classify(req *judgev1.Request) error {
	if !g.p.Cloud() {
		return nil
	}
	check := func(repo, p string) error {
		if p == "" || g.class.ClassOf(repo, p) == instance.Public {
			return nil
		}
		g.mu.Lock()
		g.st.Refused++
		g.refused = append(g.refused, Refusal{Repo: repo, Path: p})
		g.mu.Unlock()
		return fmt.Errorf("%w: %s/%s is classified private (provider %s)", ErrClassified, repo, p, g.Name())
	}
	if err := check(req.Claim.Repo, req.Claim.Path); err != nil {
		return err
	}
	for _, c := range req.Claims {
		repo := c.Repo
		if repo == "" {
			repo = req.Claim.Repo
		}
		if err := check(repo, c.Path); err != nil {
			return err
		}
	}
	for _, e := range req.Evidence {
		repo := e.Repo
		if repo == "" {
			repo = req.Claim.Repo
		}
		if err := check(repo, e.Path); err != nil {
			return err
		}
	}
	return nil
}

// reserve checks the budget, projecting the worst case of this request, and counts it.
func (g *Guard) reserve(req *judgev1.Request) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	in, _ := EstimateUsage(req, g.overhead)
	maxOut := req.Limits.MaxOutputTokens
	if maxOut == 0 {
		maxOut = DefaultMaxOutputTokens
	}
	used := g.st.InputTokens + g.st.OutputTokens
	over := g.st.Requests >= g.budget.MaxRequests || used+in+maxOut > g.budget.MaxTokens
	if g.budget.MaxUSD > 0 && g.price != nil {
		over = over || g.st.CostUSD+g.price.Cost(in, maxOut, 0) > g.budget.MaxUSD
	}
	if over {
		g.st.BudgetStops++
		return ErrBudget
	}
	g.st.Requests++
	if g.budget.MaxUSD > 0 {
		rem := g.budget.MaxUSD - g.st.CostUSD
		req.Limits.BudgetRemainingUSD = &rem
	}
	remT := g.budget.MaxTokens - used
	req.Limits.BudgetRemainingTokens = &remT
	req.Limits.MaxOutputTokens = maxOut
	return nil
}

func (g *Guard) validate(req *judgev1.Request, resp *judgev1.Response) error {
	if resp == nil {
		return errors.New("judge: provider returned no response")
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("judge: encode response: %w", err)
	}
	if errs := g.schema.ValidateJSON(b); len(errs) > 0 {
		return fmt.Errorf("judge: response violates judge/v1: %v", errs[:min(3, len(errs))])
	}
	if resp.ID != req.ID {
		return fmt.Errorf("judge: response id %q does not match request %q", resp.ID, req.ID)
	}
	return nil
}

func (g *Guard) account(resp *judgev1.Response, d time.Duration) {
	u := resp.Usage
	if u.CostUSD == 0 && g.price != nil && g.p.Billing() != judgev1.BillingNone {
		u.CostUSD = g.price.Cost(u.InputTokens, u.OutputTokens, u.CacheReadInputTokens)
		resp.Usage.CostUSD = u.CostUSD
	}
	g.st.Responses++
	g.st.InputTokens += u.InputTokens + u.CacheCreationInputTokens
	g.st.OutputTokens += u.OutputTokens
	g.st.CacheRead += u.CacheReadInputTokens
	g.st.CostUSD += u.CostUSD
	g.st.Estimated = g.st.Estimated || u.Estimated
	if resp.LatencyMS == 0 {
		resp.LatencyMS = d.Milliseconds()
	}
	g.st.latencies = append(g.st.latencies, resp.LatencyMS)
}

func (g *Guard) verify(ctx context.Context, req *judgev1.Request, resp *judgev1.Response) Outcome {
	o := Outcome{Response: resp, Verdict: resp.Verdict}
	if req.Mode == judgev1.ModeAgentic {
		g.verifyAgentic(ctx, &o)
	} else if resp.Verdict == judgev1.Yes && req.RequireQuote {
		o.Quote = VerifyQuote(req, resp.Quote)
		if !o.Quote.OK {
			o.QuoteRejected, o.Verdict = true, judgev1.Unknown
			g.st.QuotesRejected++
		}
	}
	g.count(o)
	return o
}

// verifyAgentic checks every evidence reference of an agentic answer at path:line±2; a yes
// without evidence, or with any unverifiable reference, becomes unknown.
func (g *Guard) verifyAgentic(ctx context.Context, o *Outcome) {
	refs := o.Response.EvidenceRefs
	if o.Verdict != judgev1.Yes && len(refs) == 0 {
		return
	}
	n, err := ToolsFrom(ctx).Verify(ctx, refs)
	if err == nil && n > 0 {
		o.Quote = QuoteCheck{OK: true, Quote: refs[0].Quote, Line: refs[0].Line, Evidence: judgev1.Evidence{Path: refs[0].Path}}
		return
	}
	reason := "a yes needs at least one evidence reference"
	if err != nil {
		reason = err.Error()
	}
	o.Quote = QuoteCheck{Reason: reason}
	if o.Verdict != judgev1.Unknown {
		o.QuoteRejected, o.Verdict = true, judgev1.Unknown
		g.st.QuotesRejected++
	}
}

func (g *Guard) count(o Outcome) {
	switch o.Verdict {
	case judgev1.Yes:
		g.st.Yes++
	case judgev1.No:
		g.st.No++
	default:
		g.st.Unknown++
	}
}

// estimate accounts a dry-run request without calling the provider.
func (g *Guard) estimate(req *judgev1.Request) Outcome {
	in, out := EstimateUsage(req, g.overhead)
	cache := 0
	if req.Mode == judgev1.ModeAgentic {
		in, cache, out = agenticEstimate(req, g.overhead)
	}
	cost := 0.0
	if g.price != nil {
		cost = g.price.Cost(in, out, cache)
	}
	resp := dryRun(req, judgev1.Model{Provider: g.p.Name(), ID: g.p.Model()}, g.p.Billing(), nil)
	resp.Usage.InputTokens, resp.Usage.CacheReadInputTokens, resp.Usage.OutputTokens, resp.Usage.CostUSD = in, cache, out, cost
	g.mu.Lock()
	defer g.mu.Unlock()
	g.st.Requests++
	g.st.InputTokens += in
	g.st.CacheRead += cache
	g.st.OutputTokens += out
	g.st.CostUSD += cost
	g.st.Estimated = true
	g.st.Unknown++
	return Outcome{Response: resp, Verdict: judgev1.Unknown}
}
