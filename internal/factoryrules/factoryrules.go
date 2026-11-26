// Package factoryrules puts the business rules of an API factory's escape layer into the
// claim ledger. The factory keeps generated code in step with its source by its own gates;
// what it cannot keep in step is a rule whose behaviour is hand-written (compute logic, a
// custom handler). Each such rule line becomes a claim judged against the repository, with
// the hand-written files pinned as its footprint (ledger Evidence.Pinned): when one of them
// changes, `docsp stale` re-opens the rule and the per-commit recheck judges it again.
package factoryrules

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// Reasons an item is judged.
const (
	ReasonNew     = "new"          // not in the ledger (or its line text changed)
	ReasonPins    = "pins_changed" // the manifest names other hand-written files than the claim pins
	ReasonCurrent = "current"      // in the ledger with the same pins: nothing to do
)

// Item is one escaped rule line and what to do with it.
type Item struct {
	API    string   `json:"api"`
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	ID     string   `json:"id"`
	Files  []string `json:"files"`
	Reason string   `json:"reason"`
}

// Plan is the rule lines of a manifest checked against the ledger.
type Plan struct {
	Items []Item `json:"items"`
	// Judge counts the items to judge (Reason new or pins_changed).
	Judge int `json:"judge"`
}

// Lines reads a line of a document at the builder's revision.
type Lines interface {
	LineText(ctx context.Context, path string, n int) (string, bool)
}

// MakePlan resolves each escaped rule to its line text and claim id and decides whether it
// needs judging. A rule whose line is missing or does not mention the rule's condition
// means the manifest was exported from another revision: that is an error, not a guess.
func MakePlan(ctx context.Context, m *factorymanifest.Manifest, l *ledger.Ledger, repo string, lines Lines) (*Plan, error) {
	p := &Plan{Items: []Item{}}
	for _, r := range m.EscapedRules() {
		text, ok := lines.LineText(ctx, r.Path, r.Line)
		if !ok {
			return nil, fmt.Errorf("factory rules: %s:%d (%s) does not exist at this revision: export the manifest from the same commit", r.Path, r.Line, r.API)
		}
		if w := firstLine(r.When); w != "" && !strings.Contains(text, w) {
			return nil, fmt.Errorf("factory rules: %s:%d (%s) reads %q, not the rule %q: export the manifest from the same commit", r.Path, r.Line, r.API, strings.TrimSpace(text), w)
		}
		files := slices.Clone(r.Files)
		sort.Strings(files)
		it := Item{API: r.API, Path: r.Path, Line: r.Line, Text: text, ID: ledger.ClaimID(repo, r.Path, text), Files: files, Reason: ReasonNew}
		if c := l.Find(it.ID); c != nil {
			pinned := ledger.PinnedPaths(c)
			sort.Strings(pinned)
			it.Reason = ReasonPins
			if slices.Equal(pinned, files) {
				it.Reason = ReasonCurrent
			}
		}
		if it.Reason != ReasonCurrent {
			p.Judge++
		}
		p.Items = append(p.Items, it)
	}
	return p, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// Hint is the agentic runner's hint for the plan's lines: the hand-written files of each rule.
func (p *Plan) Hint() func(path string, line int) string {
	h := map[string]string{}
	for _, it := range p.Items {
		if len(it.Files) > 0 {
			h[fmt.Sprintf("%s:%d", it.Path, it.Line)] = strings.Join(it.Files, ", ")
		}
	}
	return func(path string, line int) string { return h[fmt.Sprintf("%s:%d", path, line)] }
}

// Result summarises a run.
type Result struct {
	Judged  int            `json:"judged"`
	Failed  int            `json:"failed"`
	Current int            `json:"current"`
	ByCat   map[string]int `json:"categories"`
}

// Judge judges the plan's items that need it (grouped by document), pins each new claim's
// hand-written files and merges it into the ledger.
func Judge(ctx context.Context, run *agentic.Runner, b *ledger.Builder, l *ledger.Ledger, p *Plan) Result {
	res := Result{ByCat: map[string]int{}}
	byDoc := map[string][]Item{}
	for _, it := range p.Items {
		if it.Reason == ReasonCurrent {
			res.Current++
			continue
		}
		byDoc[it.Path] = append(byDoc[it.Path], it)
	}
	docs := make([]string, 0, len(byDoc))
	for d := range byDoc {
		docs = append(docs, d)
	}
	sort.Strings(docs)
	for _, d := range docs {
		files := map[int][]string{}
		lines := make([]int, 0, len(byDoc[d]))
		for _, it := range byDoc[d] {
			files[it.Line] = it.Files
			lines = append(lines, it.Line)
		}
		lrs := run.Judge(ctx, d, lines)
		for i := range lrs {
			c, ok := b.Claim(ctx, &lrs[i])
			if !ok {
				res.Failed++
				continue
			}
			b.Pin(&c, files[lrs[i].Line])
			l.Merge(c)
			res.Judged++
			res.ByCat[c.Category]++
		}
	}
	return res
}
