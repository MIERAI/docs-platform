package outcome

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/consistency"
)

// DefaultTarget is the precision objective when none is given: four reports in five are real.
const DefaultTarget = 0.8

// DefaultMaxFlipRate is the flip-rate objective when none is given. It is tighter than the
// precision allowance: a flip happens on unchanged inputs, so it is pure judge noise, and a
// verdict that changes without cause erodes trust faster than a reasoned false positive.
const DefaultMaxFlipRate = 0.1

// MinLabels is the audit sample size below which the error budget is not judged.
const MinLabels = 10

// Labels counts the labels of one source (the latest per claim or box).
type Labels struct {
	TP   int `json:"tp"`
	FP   int `json:"fp"`
	Skip int `json:"skip,omitempty"`
}

// Stats is the outcome of one repository (or all, Repo "").
type Stats struct {
	Repo     string `json:"repo,omitempty"`
	Reported int    `json:"reported"`
	// Open: reported claims whose last event is not a resolution.
	Open     int            `json:"open"`
	Resolved map[string]int `json:"resolved"` // by how
	Flips    int            `json:"flips"`
	// Rejudged: judgements again on unchanged inputs (flips included), the flip rate's n.
	Rejudged int               `json:"rejudged_unchanged"`
	Labels   map[string]Labels `json:"labels"` // by source
	// Precision is computed from the random audit only; FlipRate needs no person at all.
	Precision consistency.SLI     `json:"precision"`
	FlipRate  consistency.SLI     `json:"flip_rate"`
	Budget    *consistency.Budget `json:"error_budget,omitempty"`
}

// Summary is the precision report of a stream.
type Summary struct {
	SchemaVersion string  `json:"schema_version"`
	Target        float64 `json:"target"`
	MaxFlipRate   float64 `json:"max_flip_rate"`
	Events        int     `json:"events"`
	Total         Stats   `json:"total"`
	Repos         []Stats `json:"repos"`
}

// Report computes the SLIs per repository and in total (target <= 0: DefaultTarget).
// Precision is the share of true positives among the audited claims: the audit is a uniform
// sample of open reports, so it is an unbiased estimate (Wilson 95% bounds); PR ticks and the
// doc-edited proxy are counted next to it, never in it - people tick the reports they act on.
// The flip rate is the share of judgements again on unchanged inputs that changed their
// verdict: a flip proves one of two verdicts wrong, so it is held to at most maxFlip
// (maxFlip <= 0 or > 1: DefaultMaxFlipRate).
func Report(events []Event, target, maxFlip float64) *Summary {
	if target <= 0 || target > 1 {
		target = DefaultTarget
	}
	if maxFlip <= 0 || maxFlip > 1 {
		maxFlip = DefaultMaxFlipRate
	}
	r := &Summary{SchemaVersion: SchemaVersion, Target: target, MaxFlipRate: maxFlip, Events: len(events), Repos: []Stats{}}
	byRepo := map[string][]Event{}
	for _, e := range events {
		byRepo[e.Repo] = append(byRepo[e.Repo], e)
	}
	r.Total = stats("", events, target, maxFlip)
	names := make([]string, 0, len(byRepo))
	for n := range byRepo {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r.Repos = append(r.Repos, stats(n, byRepo[n], target, maxFlip))
	}
	return r
}

func stats(repo string, events []Event, target, maxFlip float64) Stats {
	s := Stats{Repo: repo, Resolved: map[string]int{}, Labels: map[string]Labels{}}
	open := map[string]bool{}
	latest := map[string]map[string]string{} // source -> subject -> label
	for _, e := range events {
		switch e.Kind {
		case KindReported:
			s.Reported++
			open[e.Repo+"\x00"+e.ClaimID] = true
		case KindResolved:
			s.Resolved[e.How]++
			delete(open, e.Repo+"\x00"+e.ClaimID)
		case KindRejudged:
			s.Rejudged++
		}
		if e.How == HowFlip {
			s.Flips++
			s.Rejudged++
		}
		if e.Label != "" && e.Source != "" {
			if latest[e.Source] == nil {
				latest[e.Source] = map[string]string{}
			}
			latest[e.Source][subject(e)] = e.Label
		}
	}
	s.Open = len(open)
	for src, m := range latest {
		var c Labels
		for _, l := range m {
			switch l {
			case LabelTP:
				c.TP++
			case LabelFP:
				c.FP++
			default:
				c.Skip++
			}
		}
		s.Labels[src] = c
	}
	s.precision(target)
	s.flipRate(maxFlip)
	return s
}

// subject is what one label is about: a later label of the same subject and source replaces
// an earlier one (a box ticked again, a claim audited twice). A proxy label is one fix of one
// report: every such event counts.
func subject(e Event) string {
	switch e.Source {
	case SourcePRTick:
		return e.Repo + "\x00" + strconv.Itoa(e.PR) + "\x00" + e.Path + ":" + strconv.Itoa(e.Line)
	case SourceProxy:
		return e.Repo + "\x00" + e.ClaimID + "\x00" + e.At + "\x00" + e.Ref
	}
	return e.Repo + "\x00" + e.ClaimID
}

func (s *Stats) precision(target float64) {
	a := s.Labels[SourceAudit]
	n := a.TP + a.FP
	s.Precision = consistency.SLI{Target: target, N: n, Bad: a.FP}
	if n == 0 {
		s.Precision.Note = "no audit labels: run `docsp outcome sample`, judge the sheet, `docsp outcome label` it"
		return
	}
	v := float64(a.TP) / float64(n)
	lo, hi := consistency.Wilson(a.TP, n)
	s.Precision.Value, s.Precision.Low, s.Precision.High, s.Precision.Met = &v, &lo, &hi, v >= target
	s.Precision.Note = fmt.Sprintf("random audit: %d labelled, %d skipped", n, a.Skip)
	if n < MinLabels {
		s.Precision.Note += fmt.Sprintf("; not enough labels for the error budget (%d of %d)", n, MinLabels)
		return
	}
	allowed := 1 - target
	b := &consistency.Budget{Allowed: allowed, Used: 1 - v, Burned: v < target}
	if allowed > 0 {
		b.Remaining = (allowed - b.Used) / allowed
	}
	s.Budget = b
}

func (s *Stats) flipRate(maxFlip float64) {
	s.FlipRate = consistency.SLI{Target: maxFlip, N: s.Rejudged, Bad: s.Flips}
	if s.Rejudged == 0 {
		s.FlipRate.Note = "no claim judged again on unchanged inputs yet"
		return
	}
	v := float64(s.Flips) / float64(s.Rejudged)
	lo, hi := consistency.Wilson(s.Flips, s.Rejudged)
	s.FlipRate.Value, s.FlipRate.Low, s.FlipRate.High, s.FlipRate.Met = &v, &lo, &hi, v <= maxFlip
}

// Text writes the report for a terminal.
func (r *Summary) Text(w io.Writer) {
	for _, s := range append(r.Repos, r.Total) {
		name := s.Repo
		if name == "" {
			name = "total"
		}
		_, _ = fmt.Fprintf(w, "%s: precision %s (target %.0f%%), flip rate %s; reported %d, open %d, resolved %s; budget %s\n",
			name, sli(s.Precision), 100*r.Target, sli(s.FlipRate), s.Reported, s.Open, counts(s.Resolved), budget(s))
		_, _ = fmt.Fprintf(w, "  biased signals (self-selected, not in the SLI): %s\n", biased(s))
	}
}

// Markdown writes the report for a job summary.
func (r *Summary) Markdown(w io.Writer) {
	_, _ = fmt.Fprintf(w, "### docs-platform: precision of its own reports\n\n")
	_, _ = fmt.Fprintf(w, "| repo | precision (audit) | error budget | flip rate | reported | open | resolved | biased signals |\n|---|---|---|---|---|---|---|---|\n")
	for _, s := range append(r.Repos, r.Total) {
		name := s.Repo
		if name == "" {
			name = "**total**"
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %d | %d | %s | %s |\n",
			name, sli(s.Precision), budget(s), sli(s.FlipRate), s.Reported, s.Open, counts(s.Resolved), biased(s))
	}
	_, _ = fmt.Fprintf(w, "\nPrecision target %.0f%%, measured only on a random audit sample (Wilson 95%%); "+
		"PR ticks and edited lines are self-selected and shown as supporting signals only. "+
		"A flip is a verdict that changed on unchanged inputs; flip-rate objective at most %.0f%%.\n", 100*r.Target, 100*r.MaxFlipRate)
}

func sli(s consistency.SLI) string {
	if s.Value == nil {
		return fmt.Sprintf("- (n=%d)", s.N)
	}
	return fmt.Sprintf("%.0f%% [%.0f-%.0f] (%d/%d)", 100**s.Value, 100**s.Low, 100**s.High, s.N-s.Bad, s.N)
}

func budget(s Stats) string {
	switch b := s.Budget; {
	case b == nil:
		return "not enough labels"
	case b.Burned:
		return fmt.Sprintf("burned (%.0f%% wrong, %.0f%% allowed)", 100*b.Used, 100*b.Allowed)
	default:
		return fmt.Sprintf("ok (%.0f%% left)", 100*b.Remaining)
	}
}

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "0"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func biased(s Stats) string {
	t, p := s.Labels[SourcePRTick], s.Labels[SourceProxy]
	return fmt.Sprintf("PR ticks %d tp / %d fp, doc edited after report %d", t.TP, t.FP, p.TP)
}
