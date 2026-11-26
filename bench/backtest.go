package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// ledgerBacktest scores evidence-based invalidation without hindsight: the ledger's claims
// were judged at revisions BEFORE the ground truth's causing commits (where the lines still
// held), and the real history after each claim's verified revision is replayed. A positive
// is caught at its first causing commit when that commit itself changes the claim's
// evidence; at any causing commit when one of them does.
type ledgerBacktest struct {
	Repos []btRepo `json:"repos"`
}

type btRepo struct {
	Repo    string     `json:"repo"`
	Claims  int        `json:"claims"`
	Commits []lrCommit `json:"commits"`
	Items   []btItem   `json:"items"`
}

type btItem struct {
	ID           string   `json:"id"`
	Path         string   `json:"path"`
	Line         int      `json:"line"`
	Status       string   `json:"status"` // scored | no_claim | no_causing_commit | base_not_before_first_causing
	Why          string   `json:"why,omitempty"`
	BaseCategory string   `json:"base_category,omitempty"`
	VerifiedRef  string   `json:"verified_ref,omitempty"`
	Causing      int      `json:"causing_commits_after_base"`
	First        bool     `json:"caught_at_first_causing_commit"`
	Any          bool     `json:"caught_at_any_causing_commit"`
	StaledAt     string   `json:"first_staled_at,omitempty"`
	StaledBy     []string `json:"first_staled_by,omitempty"`
	Flagging     int      `json:"commits_staling_it"`
	// For a claim never staled: its evidence and what the causing commits changed.
	Evidence       []string `json:"evidence,omitempty"`
	CausingChanged []string `json:"causing_commits_changed,omitempty"`
}

func runLedgerBacktest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ledger-backtest", flag.ContinueOnError)
	file := fs.String("ledger", "", "claim ledger judged at revisions before the causing commits")
	gtDir := fs.String("gt", "", "directory of ground-truth *.jsonl files")
	setFile := fs.String("set", "", "eval set (at the final revision) whose gt_positive items are scored")
	js := fs.String("json", "", "write the JSON report here")
	repos := dirFlag{}
	fs.Var(repos, "repo", "name=DIR: a checkout at the final revision, with history (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("ledger-backtest flags: %w", err)
	}
	if *file == "" || *gtDir == "" || *setFile == "" || len(repos) == 0 {
		return errors.New("ledger-backtest: -ledger, -gt, -set and -repo are required")
	}
	l, err := ledger.Load(*file)
	if err != nil {
		return err //nolint:wrapcheck // already descriptive
	}
	gt, err := loadGT(*gtDir)
	if err != nil {
		return err
	}
	set, err := evalmodel.LoadSet(*setFile)
	if err != nil {
		return err //nolint:wrapcheck // already descriptive
	}
	rep := ledgerBacktest{}
	for _, name := range sortedKeys(repos) {
		rr, err := backtestRepo(ctx, l, name, repos[name], gtFor(gt, name), set)
		if err != nil {
			return err
		}
		rep.Repos = append(rep.Repos, rr)
	}
	fmt.Print(rep.markdown())
	if *js == "" {
		return nil
	}
	b, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	return os.WriteFile(*js, b, 0o600) //nolint:wrapcheck // operator-named output
}

type history struct {
	ctx     context.Context
	dir     string
	order   []string
	idx     map[string]int
	changes map[string]*ledger.Changes
}

func newHistory(ctx context.Context, dir string) (*history, error) {
	out, err := repo.Git(ctx, dir, "log", "--first-parent", "--reverse", "--format=%H", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	h := &history{ctx: ctx, dir: dir, order: strings.Fields(out), idx: map[string]int{}, changes: map[string]*ledger.Changes{}}
	for i, c := range h.order {
		h.idx[c] = i
	}
	return h, nil
}

func (h *history) at(i int) (*ledger.Changes, error) {
	c := h.order[i]
	if ch, ok := h.changes[c]; ok {
		return ch, nil
	}
	ch, err := ledger.Diff(h.ctx, h.dir, c+"^", c)
	h.changes[c] = ch
	return ch, err //nolint:wrapcheck // already descriptive
}

func backtestRepo(ctx context.Context, l *ledger.Ledger, name, dir string, gt []GTItem, set *evalmodel.Set) (btRepo, error) {
	rr := btRepo{Repo: name}
	h, err := newHistory(ctx, dir)
	if err != nil {
		return rr, err
	}
	var claims []*ledger.Claim
	for i := range l.Claims {
		if c := &l.Claims[i]; c.Repo == name && c.Target == nil {
			if _, ok := h.idx[c.VerifiedRef]; ok {
				claims = append(claims, c)
			}
		}
	}
	rr.Claims = len(claims)
	if rr.Commits, err = backtestCosts(h, claims); err != nil {
		return rr, err
	}
	byID := map[string]*ledger.Claim{}
	for _, c := range claims {
		byID[c.ID] = c
	}
	for _, it := range set.Items {
		if it.Source == evalmodel.SourceGTPositive && it.Repo == name {
			rr.Items = append(rr.Items, backtestItem(ctx, h, it, byID[ledger.ClaimID(name, it.Claim.Path, it.Claim.Text)], gt))
		}
	}
	return rr, nil
}

// backtestCosts counts, per commit, the claims already judged (verified at an earlier
// commit) that it invalidates by itself.
func backtestCosts(h *history, claims []*ledger.Claim) ([]lrCommit, error) {
	var out []lrCommit
	for i := 1; i < len(h.order); i++ {
		ch, err := h.at(i)
		if err != nil {
			return nil, err
		}
		lc := lrCommit{SHA: h.order[i][:12], Reasons: map[string]int{}}
		for _, c := range claims {
			if h.idx[c.VerifiedRef] >= i {
				continue
			}
			lc.Alive++
			if rs := evidenceReasons(ledger.Invalidate(c, ch, func(string) (string, bool) { return "", false })); len(rs) > 0 {
				lc.Stale++
				for _, r := range rs {
					lc.Reasons[r]++
				}
			}
		}
		if lc.Alive > 0 {
			out = append(out, lc)
		}
	}
	return out, nil
}

func backtestItem(ctx context.Context, h *history, it evalmodel.Item, c *ledger.Claim, gt []GTItem) btItem {
	bi := btItem{ID: it.ID, Path: it.Claim.Path, Line: it.Claim.Line, Status: "scored"}
	if c == nil {
		bi.Status, bi.Why = "no_claim", "not judged at a base revision (no revision before its first causing commit was affordable)"
		return bi
	}
	bi.BaseCategory, bi.VerifiedRef = c.Category, c.VerifiedRef[:12]
	tr := traceItem(ctx, h.dir, gtItem(gt, it))
	if len(tr.causing) == 0 {
		bi.Status, bi.Why = "no_causing_commit", tr.why
		return bi
	}
	base := h.idx[c.VerifiedRef]
	if first, ok := h.idx[tr.causing[0]]; !ok || first <= base {
		bi.Status, bi.Why = "base_not_before_first_causing", "the claim was judged at or after its first causing commit"
		return bi
	}
	causing := map[int]bool{}
	for _, s := range tr.causing {
		if i, ok := h.idx[s]; ok && i > base {
			causing[i] = true
			bi.Causing++
		}
	}
	first := h.idx[tr.causing[0]]
	for i := base + 1; i < len(h.order); i++ {
		ch, err := h.at(i)
		if err != nil {
			continue
		}
		rs := evidenceReasons(ledger.Invalidate(c, ch, func(string) (string, bool) { return "", false }))
		if len(rs) == 0 {
			continue
		}
		bi.Flagging++
		if bi.StaledAt == "" {
			bi.StaledAt, bi.StaledBy = h.order[i][:12], rs
		}
		bi.Any = bi.Any || causing[i]
		bi.First = bi.First || i == first
	}
	if !bi.Any {
		bi.Evidence = describeEvidence(c)
		bi.CausingChanged = causingChanges(h, causing)
	}
	return bi
}

func describeEvidence(c *ledger.Claim) []string {
	var out []string
	for _, q := range c.Evidence.Quotes {
		out = append(out, fmt.Sprintf("quote %s:%d", q.Path, q.Line))
	}
	for _, d := range c.Evidence.Drift {
		var fs []string
		for _, f := range d.Files {
			fs = append(fs, f.Path)
		}
		out = append(out, fmt.Sprintf("drift %s (files %s; removed %s)", d.ID, strings.Join(fs, ", "), d.RemovedPath))
	}
	for _, a := range c.Evidence.Absence {
		out = append(out, fmt.Sprintf("absence %q in %s (%d files)", a.Pattern, a.Scope, a.Files))
	}
	for _, p := range c.Evidence.PathMissing {
		out = append(out, "path_missing "+p.Path)
	}
	if len(out) == 0 {
		out = append(out, "no evidence ("+c.Category+")")
	}
	return out
}

func causingChanges(h *history, causing map[int]bool) []string {
	set := map[string]bool{}
	for i := range causing {
		if ch, err := h.at(i); err == nil {
			for p, st := range ch.Paths {
				set[string(st)+" "+p] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out[:min(len(out), 15)]
}

func (r ledgerBacktest) markdown() string {
	var b strings.Builder
	b.WriteString("# Ledger backtest: claims judged before their causing commits, history replayed forward\n\n")
	b.WriteString("| repo | claims | commits replayed | mean stale/commit | median | p90 | max | commits staling nothing | mean % of judged claims |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, rr := range r.Repos {
		var st []int
		zero, pct := 0, 0.0
		for _, c := range rr.Commits {
			st = append(st, c.Stale)
			if c.Stale == 0 {
				zero++
			}
			pct += float64(c.Stale) / float64(c.Alive)
		}
		sort.Ints(st)
		n := max(1, len(st))
		fmt.Fprintf(&b, "| %s | %d | %d | %.2f | %d | %d | %d | %d%% | %.1f%% |\n", rr.Repo, rr.Claims, len(st), mean(st), pick(st, 0.5), pick(st, 0.9), pick(st, 1), 100*zero/n, 100*pct/float64(n))
	}
	b.WriteString("\n| repo | positives | scored | caught at first causing commit | at any causing commit | staled at all before the end | base verdict confirmed |\n|---|---|---|---|---|---|---|\n")
	for _, rr := range r.Repos {
		sc, first, anyc, some, conf := 0, 0, 0, 0, 0
		for _, it := range rr.Items {
			if it.Status != "scored" {
				continue
			}
			sc++
			first += b2i(it.First)
			anyc += b2i(it.Any)
			some += b2i(it.Flagging > 0)
			conf += b2i(it.BaseCategory == "confirmed")
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s | %s |\n", rr.Repo, len(rr.Items), sc, frac(first, sc), frac(anyc, sc), frac(some, sc), frac(conf, sc))
	}
	return b.String()
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}
