package consistency

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// RecheckResult summarises one re-judgment run.
type RecheckResult struct {
	Judged  int            `json:"judged"`
	Retired int            `json:"retired"`
	Failed  int            `json:"failed"`
	ByCat   map[string]int `json:"categories"`
	// Done lists documents all of whose picked claims were judged (a pending full re-audit
	// of them is complete when every claim of the document was picked).
	Done []string `json:"documents_done"`
	// Reanchored counts quotes re-anchored without judging (their files changed outside the
	// claims' scopes).
	Reanchored int `json:"reanchored,omitempty"`
}

// Recheck judges the picked claims again (grouped by document), merges the new claims
// into the ledger and retires the claims whose line was edited or whose document is gone:
// their text no longer exists, so there is nothing to re-judge (new lines enter the ledger
// through an audit).
func Recheck(ctx context.Context, run *agentic.Runner, b *ledger.Builder, l *ledger.Ledger, picks []Pick) RecheckResult {
	res := RecheckResult{ByCat: map[string]int{}}
	byDoc := map[string][]Pick{}
	var retire []string
	for _, p := range picks {
		if strings.Contains(p.Reason, ledger.ReasonDocLineChanged) || strings.Contains(p.Reason, ledger.ReasonDocRemoved) {
			retire = append(retire, p.ID)
			continue
		}
		byDoc[p.Path] = append(byDoc[p.Path], p)
	}
	res.Retired = l.Remove(retire...)
	docs := make([]string, 0, len(byDoc))
	for d := range byDoc {
		docs = append(docs, d)
	}
	sort.Strings(docs)
	for _, d := range docs {
		lines := make([]int, 0, len(byDoc[d]))
		for _, p := range byDoc[d] {
			lines = append(lines, p.Line)
		}
		ok := true
		lrs := run.Judge(ctx, d, lines)
		for i := range lrs {
			c, good := b.Claim(ctx, &lrs[i])
			if !good {
				res.Failed++
				ok = false
				continue
			}
			if old := l.Find(c.ID); old != nil && len(old.Evidence.Pinned) > 0 {
				b.Pin(&c, ledger.PinnedPaths(old)) // the producer's footprint outlives the judgment
			}
			l.Merge(c)
			res.Judged++
			res.ByCat[c.Category]++
		}
		if ok {
			res.Done = append(res.Done, d)
		}
	}
	return res
}

// Reanchor re-anchors, in the ledger, the quotes of fresh claims whose files changed outside
// the claims' scopes (new line, blob and scope at ref; the verdicts are carried forward) and
// returns how many moved. Nothing is judged; a failed check re-anchors nothing.
func Reanchor(ctx context.Context, l *ledger.Ledger, dir, repoName, ref string) int {
	rep, err := ledger.Check(ctx, l, dir, repoName, ref, time.Now())
	if err != nil {
		return 0
	}
	return l.Reanchor(rep)
}
