package evalmodel

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// SchemaVersion of docs-platform/eval/v1.
const SchemaVersion = "1.1"

// Options of one eval run.
type Options struct {
	Set         *Set
	Task        string // judge | draft
	Guard       *judge.Guard
	DryRun      bool
	Split       string // "" = every split
	Limit       int    // 0 = every item
	Concurrency int
	Thresholds  instance.Eval
	Now         func() time.Time
	// Agentic, when set, runs the judge task in agentic mode (drift scan + claim_facts).
	Agentic *Agentic
}

// Result is docs-platform/eval/v1.
type Result struct {
	SchemaVersion      string       `json:"schema_version"`
	Set                SetRef       `json:"set"`
	Task               string       `json:"task"`
	Provider           string       `json:"provider"`
	Model              string       `json:"model"`
	PromptTemplateHash string       `json:"prompt_template_hash"`
	StartedAt          string       `json:"started_at"`
	DryRun             bool         `json:"dry_run"`
	Scores             Scores       `json:"scores"`
	Switch             *Switch      `json:"switch,omitempty"`
	Items              []ItemResult `json:"items,omitempty"`
	// Mode (1.1) is snippet or agentic; Group is line or doc (agentic only).
	Mode  string `json:"mode,omitempty"`
	Group string `json:"group,omitempty"`
	// Drift (1.1, agentic) summarises the drift scan of each repository.
	Drift []DriftSummary `json:"drift,omitempty"`
	// LedgerError is set when writing the claim ledger failed.
	LedgerError string `json:"ledger_error,omitempty"`
}

// SetRef identifies the frozen set.
type SetRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Items  int    `json:"items"`
}

// Switch is the outcome of the switching rule.
type Switch struct {
	Qualifies bool     `json:"qualifies"`
	Failed    []string `json:"failed"`
}

// ItemResult is one item's outcome.
type ItemResult struct {
	ID            string   `json:"id"`
	Source        string   `json:"source"`
	Split         string   `json:"split"`
	Expected      string   `json:"expected,omitempty"`
	Verdict       string   `json:"verdict"`
	QuoteRejected bool     `json:"quote_rejected,omitempty"`
	Error         string   `json:"error,omitempty"`
	Model         string   `json:"model,omitempty"`
	Draft         string   `json:"draft,omitempty"`
	Unsupported   []string `json:"unsupported,omitempty"`
	Similarity    *float64 `json:"similarity,omitempty"`
	InputTokens   int      `json:"input_tokens"`
	OutputTokens  int      `json:"output_tokens"`
	CostUSD       float64  `json:"cost_usd"`
	LatencyMS     int64    `json:"latency_ms,omitempty"`
	// Agentic (1.1): the category the harness derived, the model's own claim before
	// validation, how the answer was parsed, evidence given / validated, request grouping.
	Category   string `json:"category,omitempty"`
	Claimed    string `json:"claimed,omitempty"`
	Parse      string `json:"parse,omitempty"`
	Evidence   int    `json:"evidence,omitempty"`
	EvidenceOK int    `json:"evidence_ok,omitempty"`
	GroupSize  int    `json:"group_size,omitempty"`
	ToolSteps  int    `json:"tool_steps,omitempty"`
	// Facts are the model's facts with the harness's check of every piece of evidence.
	Facts      []agentic.FactCheck `json:"facts,omitempty"`
	Reason     string              `json:"reason,omitempty"`
	Transcript []judgev1.Step      `json:"transcript,omitempty"`
}

// Run scores the guarded provider on the set.
func Run(ctx context.Context, o Options) *Result {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	res := &Result{
		SchemaVersion: SchemaVersion, Set: SetRef{Path: o.Set.Path, SHA256: o.Set.SHA256, Items: len(o.Set.Items)},
		Task: o.Task, Provider: o.Guard.Provider().Name(), Model: o.Guard.Provider().Model(),
		PromptTemplateHash: judge.TemplateHash(), StartedAt: now().UTC().Format(time.RFC3339), DryRun: o.DryRun,
	}
	items := selectItems(o)
	res.Items = make([]ItemResult, len(items))
	if o.Agentic != nil && o.Task == TaskJudge {
		res.Mode, res.Group, res.PromptTemplateHash = "agentic", o.Agentic.Settings.Group, agentic.TemplateHashFor(o.Agentic.Settings)
		if res.Group == "" {
			res.Group = agentic.GroupLine
		}
		runAgentic(ctx, o, items, res)
		finish(res, o)
		return res
	}
	res.Mode = "snippet"
	conc := max(1, o.Concurrency)
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var stop sync.Once
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			res.Items[i] = runItem(ctx, o, &items[i])
			if res.Items[i].Error == judge.ErrBudget.Error() {
				stop.Do(cancel)
			}
		}(i)
	}
	wg.Wait()
	finish(res, o)
	return res
}

func finish(res *Result, o Options) {
	res.Scores = score(o.Task, res.Items, o.Guard.Stats(), o.DryRun)
	if !o.DryRun {
		res.Switch = decide(o.Task, res.Scores, o.Thresholds)
	}
}

func selectItems(o Options) []Item {
	var out []Item
	for _, it := range o.Set.Items {
		if it.Task != o.Task || o.Split != "" && it.Split != o.Split {
			continue
		}
		out = append(out, it)
		if o.Limit > 0 && len(out) == o.Limit {
			break
		}
	}
	return out
}

func runItem(ctx context.Context, o Options, it *Item) ItemResult {
	r := ItemResult{ID: it.ID, Source: it.Source, Split: it.Split, Expected: it.Expected, Verdict: judgev1.Unknown}
	if ctx.Err() != nil {
		r.Error = judge.ErrBudget.Error()
		return r
	}
	if it.TaskType == judgev1.TaskClaimFacts {
		r.Error = "claim_facts items need the agentic mode (-mode agentic)"
		return r
	}
	req := judge.NewRequest(it.ID, it.TaskType, it.Claim, it.Evidence, "")
	req.DryRun = o.DryRun
	out, err := o.Guard.Judge(ctx, req)
	if err != nil {
		r.Error = err.Error()
		if errors.Is(err, judge.ErrBudget) {
			r.Error = judge.ErrBudget.Error()
		}
		return r
	}
	resp := out.Response
	r.Verdict, r.QuoteRejected = out.Verdict, out.QuoteRejected
	r.Model = resp.Model.ID
	r.InputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens
	r.OutputTokens, r.CostUSD, r.LatencyMS = resp.Usage.OutputTokens, resp.Usage.CostUSD, resp.LatencyMS
	if resp.Error != nil {
		r.Error = resp.Error.Code + ": " + resp.Error.Message
	}
	if o.Task == TaskDraft && resp.Draft != nil {
		r.Draft = resp.Draft.Text
		r.Unsupported = Unsupported(r.Draft, it.Claim.Text, it.Evidence)
		sim := Similarity(r.Draft, it.ReferenceFix+"\n"+it.ReferenceActual)
		r.Similarity = &sim
	}
	return r
}
