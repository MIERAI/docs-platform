package semantic

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
)

// Rules of the agentic judge: one per problem category the harness derived from validated
// evidence (unverifiable_in_repo is counted, never reported).
const (
	RuleAgenticContradicted      = "agentic_contradicted"
	RuleAgenticDanglingReference = "agentic_dangling_reference"
	RuleAgenticIncompleteList    = "agentic_incomplete_list"
	RuleAgenticOutdatedWording   = "agentic_outdated_wording"
)

var agenticRule = map[string]string{
	agentic.Contradicted: RuleAgenticContradicted, agentic.DanglingReference: RuleAgenticDanglingReference,
	agentic.IncompleteList: RuleAgenticIncompleteList, agentic.OutdatedWording: RuleAgenticOutdatedWording,
}

func init() {
	for _, r := range agenticRule {
		subLayerOf[r] = SubModel
	}
}

// agenticConcurrency bounds the work units in flight (the provider has its own limit).
const agenticConcurrency = 4

// askAgentic puts the queued claims to the agentic judge: the drift scan first (cached by
// HEAD), then one request per line, or per document group.
func (c *checker) askAgentic(qs []question) error {
	start := time.Now()
	run := c.opt.Agentic
	if d, err := run.Drift(c.ctx); err == nil && d != nil {
		c.stats.DriftEntries, c.stats.DriftValidated, c.stats.DriftCached = len(d.Raw), len(d.Entries), d.Cached
	}
	units := c.agenticUnits(qs)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, agenticConcurrency)
	var refused []claim.Claim
	for _, u := range units {
		wg.Add(1)
		sem <- struct{}{}
		go func(u []question) {
			defer func() { <-sem; wg.Done() }()
			lines := make([]int, 0, len(u))
			for _, q := range u {
				lines = append(lines, q.cl.Line)
			}
			res := run.Judge(c.ctx, u[0].cl.Path, lines)
			mu.Lock()
			defer mu.Unlock()
			refused = append(refused, c.agenticResults(u, res)...)
			if c.opt.OnJudged != nil {
				c.opt.OnJudged(res)
			}
		}(u)
	}
	wg.Wait()
	c.stats.ModelSeconds = time.Since(start).Seconds()
	js := c.opt.Judge.Stats()
	c.stats.ModelRequests, c.stats.ModelUSD = js.Requests, js.CostUSD
	c.stats.ModelTokens = js.InputTokens + js.OutputTokens + js.CacheRead
	c.stats.ParseFailures = js.ParseFailures
	c.refusals(refused)
	if err := c.ctx.Err(); err != nil {
		return fmt.Errorf("judge: %w", err)
	}
	return nil
}

// agenticUnits: one unit per line, or (doc grouping) per document; Runner.Judge splits a
// document into groups itself.
func (c *checker) agenticUnits(qs []question) [][]question {
	if c.opt.AgenticGroup != agentic.GroupDoc {
		out := make([][]question, 0, len(qs))
		for _, q := range qs {
			out = append(out, []question{q})
		}
		return out
	}
	idx := map[string]int{}
	var out [][]question
	for _, q := range qs {
		i, ok := idx[q.cl.Path]
		if !ok {
			i = len(out)
			idx[q.cl.Path] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], q)
	}
	return out
}

// agenticResults records the issues of one unit and returns the claims refused by data
// classification. Callers hold the lock.
func (c *checker) agenticResults(u []question, res []agentic.LineResult) []claim.Claim {
	byLine := map[int]agentic.LineResult{}
	for _, r := range res {
		byLine[r.Line] = r
	}
	var refused []claim.Claim
	for i := range u {
		q := &u[i]
		r, ok := byLine[q.cl.Line]
		c.stats.Questions++
		if !ok {
			continue
		}
		if c.stats.AgenticCategories == nil {
			c.stats.AgenticCategories = map[string]int{}
		}
		c.stats.AgenticCategories[r.Category]++
		switch {
		case strings.Contains(r.Error, judge.ErrClassified.Error()):
			refused = append(refused, q.cl)
			c.stats.Refused++
			continue
		case r.Error != "":
			c.stats.ModelErrors++
		default:
			c.stats.Answers++
		}
		if rule, ok := agenticRule[r.Category]; ok {
			c.add(&q.cl, c.agenticIssue(rule, &q.cl, &r))
		}
	}
	return refused
}

func (c *checker) agenticIssue(rule string, cl *claim.Claim, r *agentic.LineResult) Issue {
	is := Issue{
		Rule: rule, Subject: truncate(strings.TrimSpace(cl.Text), 80), Confidence: model.ConfMedium,
		Suggestion: "Check the sentence against the validated evidence and fix whichever is wrong.",
		Model:      c.opt.Judge.Name(), PromptHash: r.PromptHash,
		Votes: fmt.Sprintf("agentic: %d/%d evidence validated", r.EvidenceOK, r.Derivation.Evidence),
	}
	var why []string
	for _, f := range r.Facts {
		if f.Status != r.Category || !f.Valid {
			continue
		}
		why = append(why, f.Fact)
		for _, e := range f.Evidence {
			if !e.OK {
				continue
			}
			if is.Quote == "" {
				is.Quote = c.evidenceText(e.Evidence)
			}
			if e.Evidence.Kind == agentic.KindQuote {
				is.Locations = append(is.Locations, Location{Path: e.Evidence.Path, Line: e.Evidence.Line, Role: "code"})
			}
		}
	}
	is.Detail = fmt.Sprintf("judge %s (agentic) derived %s from validated evidence: %s", c.opt.Judge.Name(), r.Category, truncate(strings.Join(why, "; "), 400))
	if r.Reason != "" {
		is.Detail += " — " + truncate(r.Reason, 300)
	}
	return is
}

func (c *checker) evidenceText(e agentic.Evidence) string {
	switch e.Kind {
	case agentic.KindQuote:
		return truncate(strings.TrimSpace(e.Quote), 300)
	case agentic.KindAbsence:
		return fmt.Sprintf("no match for %q in %s", e.SearchPattern, orStar(e.SearchScope))
	case agentic.KindDrift:
		if d, err := c.opt.Agentic.Drift(c.ctx); err == nil && d != nil {
			for _, x := range d.Entries {
				if x.ID == strings.TrimSpace(e.DriftID) {
					return truncate(fmt.Sprintf("drift %s [%s] %s -> %s", x.ID, x.Kind, x.Old, x.New), 300)
				}
			}
		}
		return "drift entry " + e.DriftID
	case agentic.KindPathMissing:
		return e.Path + " does not exist"
	}
	return ""
}

func orStar(s string) string {
	if strings.TrimSpace(s) == "" {
		return "*"
	}
	return s
}

// errAgenticWithoutJudge guards a misconfiguration.
var errAgenticWithoutJudge = errors.New("semantic: agentic mode needs a judge")
