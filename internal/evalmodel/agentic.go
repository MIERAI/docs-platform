package evalmodel

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
)

// Agentic configures an agentic-mode eval: every judge item becomes a claim_facts request
// (or part of a doc_facts group) against a checkout of its repository, after one drift scan
// per repository.
type Agentic struct {
	// Worktrees maps a repository name to a checkout of it. Items whose revision is a commit
	// hash need the checkout at that commit.
	Worktrees map[string]string
	Class     instance.Classification
	CacheDir  string
	NoDrift   bool
	Settings  agentic.Settings
}

// DriftSummary reports the drift scan of one repository in an eval run.
type DriftSummary struct {
	Repo      string  `json:"repo"`
	Head      string  `json:"head"`
	Raw       int     `json:"raw"`
	Validated int     `json:"validated"`
	Cached    bool    `json:"cached"`
	Parse     string  `json:"parse,omitempty"`
	CostUSD   float64 `json:"cost_usd"`
	Error     string  `json:"error,omitempty"`
}

var reRevision = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// unit is one request's worth of items: one line, or a group of lines of one document.
type unit struct {
	repo, path string
	idx        []int // indexes into the item list
}

func runAgentic(ctx context.Context, o Options, items []Item, res *Result) {
	a := o.Agentic
	runners := map[string]*agentic.Runner{}
	broken := map[string]string{}
	for _, repo := range repoNames(items) {
		r, why := agenticRunner(ctx, o, repo, items)
		if why != "" {
			broken[repo] = why
			continue
		}
		runners[repo] = r
		if !a.NoDrift {
			res.Drift = append(res.Drift, driftSummary(ctx, repo, r))
		}
	}
	units := planUnits(items, a.Settings)
	sem := make(chan struct{}, max(1, o.Concurrency))
	var wg sync.WaitGroup
	for _, u := range units {
		if why, bad := broken[u.repo]; bad {
			for _, i := range u.idx {
				res.Items[i] = baseResult(&items[i])
				res.Items[i].Error = why
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(u unit) {
			defer func() { <-sem; wg.Done() }()
			runUnit(ctx, runners[u.repo], u, items, res.Items)
		}(u)
	}
	wg.Wait()
}

func repoNames(items []Item) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		r := it.Repo
		if r == "" {
			r = it.Claim.Repo
		}
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// agenticRunner checks the repository's checkout (present, at the items' revision) and
// returns its runner, or why its items cannot run.
func agenticRunner(ctx context.Context, o Options, repo string, items []Item) (*agentic.Runner, string) {
	dir, ok := o.Agentic.Worktrees[repo]
	if !ok {
		return nil, fmt.Sprintf("no worktree for repository %s (pass -worktree %s=DIR)", repo, repo)
	}
	head, err := agentic.Head(ctx, dir)
	if err != nil {
		return nil, fmt.Sprintf("worktree of %s: %v", repo, err)
	}
	for _, it := range items {
		if itemRepo(&it) == repo && reRevision.MatchString(it.Revision) && !strings.HasPrefix(head, it.Revision) {
			return nil, fmt.Sprintf("worktree of %s is at %s, item %s wants %s", repo, head[:12], it.ID, it.Revision)
		}
	}
	return agentic.New(agentic.Options{
		Guard: o.Guard, Repo: repo, Dir: dir, Class: o.Agentic.Class, CacheDir: o.Agentic.CacheDir,
		NoDrift: o.Agentic.NoDrift, DryRun: o.DryRun, Settings: o.Agentic.Settings,
	}), ""
}

func itemRepo(it *Item) string {
	if it.Repo != "" {
		return it.Repo
	}
	return it.Claim.Repo
}

func driftSummary(ctx context.Context, repo string, r *agentic.Runner) DriftSummary {
	s := DriftSummary{Repo: repo}
	d, err := r.Drift(ctx)
	if err != nil {
		s.Error = err.Error()
		return s
	}
	s.Head, s.Raw, s.Validated, s.Cached, s.Parse, s.CostUSD, s.Error = d.Head, len(d.Raw), len(d.Entries), d.Cached, d.Parse, d.CostUSD, d.Error
	return s
}

// planUnits groups items: one unit per item (line grouping), or per document in chunks of
// the group size, in line order (doc grouping).
func planUnits(items []Item, s agentic.Settings) []unit {
	if s.Group != agentic.GroupDoc {
		out := make([]unit, 0, len(items))
		for i := range items {
			out = append(out, unit{repo: itemRepo(&items[i]), path: items[i].Claim.Path, idx: []int{i}})
		}
		return out
	}
	by := map[[2]string][]int{}
	var keys [][2]string
	for i := range items {
		k := [2]string{itemRepo(&items[i]), items[i].Claim.Path}
		if _, ok := by[k]; !ok {
			keys = append(keys, k)
		}
		by[k] = append(by[k], i)
	}
	size := max(1, s.GroupSize)
	var out []unit
	for _, k := range keys {
		idx := by[k]
		sort.SliceStable(idx, func(a, b int) bool { return items[idx[a]].Claim.Line < items[idx[b]].Claim.Line })
		for i := 0; i < len(idx); i += size {
			out = append(out, unit{repo: k[0], path: k[1], idx: idx[i:min(len(idx), i+size)]})
		}
	}
	return out
}

func runUnit(ctx context.Context, r *agentic.Runner, u unit, items []Item, out []ItemResult) {
	var lines []int
	for _, i := range u.idx {
		lines = append(lines, items[i].Claim.Line)
	}
	if ctx.Err() != nil {
		for _, i := range u.idx {
			out[i] = baseResult(&items[i])
			out[i].Error = judge.ErrBudget.Error()
		}
		return
	}
	byLine := map[int]agentic.LineResult{}
	for _, lr := range r.Judge(ctx, u.path, lines) {
		byLine[lr.Line] = lr
	}
	for _, i := range u.idx {
		out[i] = fromLine(&items[i], byLine[items[i].Claim.Line])
	}
}

func baseResult(it *Item) ItemResult {
	return ItemResult{ID: it.ID, Source: it.Source, Split: it.Split, Expected: it.Expected, Verdict: "unknown", Category: agentic.Unknown}
}

func fromLine(it *Item, lr agentic.LineResult) ItemResult {
	r := baseResult(it)
	r.Verdict, r.Category, r.Claimed, r.Parse = lr.Verdict, lr.Category, lr.Claimed, lr.Parse
	r.Evidence, r.EvidenceOK, r.GroupSize, r.ToolSteps = lr.Derivation.Evidence, lr.EvidenceOK, lr.GroupSize, lr.ToolSteps
	r.Model, r.InputTokens, r.OutputTokens, r.CostUSD, r.LatencyMS = lr.Model, lr.InputTokens, lr.OutputTokens, lr.CostUSD, lr.LatencyMS
	r.Error, r.Facts, r.Reason = lr.Error, lr.Facts, lr.Reason
	if strings.Contains(lr.Error, judge.ErrBudget.Error()) {
		r.Error = judge.ErrBudget.Error()
	}
	return r
}
