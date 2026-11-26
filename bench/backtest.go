package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
	// Variants: the evidence ledger alone, then with each footprint rule and all of them.
	Variants []btVariant `json:"variants"`
	// Union is the ledger alone OR the change-time impact selection (-impact).
	Union  *btScore `json:"union_with_impact,omitempty"`
	Impact *btScore `json:"impact,omitempty"`
}

type btVariant struct {
	Name  string   `json:"name"`
	Rules string   `json:"rules"`
	Repos []btRepo `json:"repos"`
	Score btScore  `json:"score"`
}

// btScore is a variant's catch rates and per-commit cost over every repository.
type btScore struct {
	Scored        int     `json:"scored"`
	First         int     `json:"first_causing"`
	Any           int     `json:"any_causing"`
	MeanStale     float64 `json:"mean_stale_per_commit"`
	MeanStalePct  float64 `json:"mean_pct_of_judged_claims"`
	Commits       int     `json:"commits"`
	ZeroCommitPct int     `json:"commits_staling_nothing_pct"`
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
	impactFile := fs.String("impact", "", "bench replay -json report: also score ledger OR impact selection on the same items")
	trGlob := fs.String("transcripts", "", "glob of eval/v1 results whose transcripts add footprints to claims recorded without them")
	trSets := fs.String("transcript-sets", "", "glob of the eval sets those results came from (to match items to claims)")
	repos := dirFlag{}
	fs.Var(repos, "repo", "name=DIR: a checkout at the final revision, with history (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("ledger-backtest flags: %w", err)
	}
	if *file == "" || *gtDir == "" || *setFile == "" || len(repos) == 0 {
		return errors.New("ledger-backtest: -ledger, -gt, -set and -repo are required")
	}
	l, gt, set, err := backtestInputs(*file, *gtDir, *setFile, *trGlob, *trSets)
	if err != nil {
		return err
	}
	rep, err := backtestVariants(ctx, l, repos, gt, set)
	if err != nil {
		return err
	}
	if *impactFile != "" {
		if err := rep.union(*impactFile); err != nil {
			return err
		}
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

// backtestInputs loads the ledger (adding footprints from transcripts when asked), the
// ground truth and the eval set.
func backtestInputs(file, gtDir, setFile, trGlob, trSets string) (*ledger.Ledger, map[string][]GTItem, *evalmodel.Set, error) {
	l, err := ledger.Load(file)
	if err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // already descriptive
	}
	gt, err := loadGT(gtDir)
	if err != nil {
		return nil, nil, nil, err
	}
	set, err := evalmodel.LoadSet(setFile)
	if err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // already descriptive
	}
	if trGlob != "" {
		n, err := enrichFootprints(l, trGlob, trSets)
		if err != nil {
			return nil, nil, nil, err
		}
		fmt.Fprintf(os.Stderr, "footprints added to %d claims from transcripts\n", n)
	}
	return l, gt, set, nil
}

// backtestVariants runs the backtest with the ledger alone, each footprint rule and all.
func backtestVariants(ctx context.Context, l *ledger.Ledger, repos dirFlag, gt map[string][]GTItem, set *evalmodel.Set) (*ledgerBacktest, error) {
	rep := &ledgerBacktest{}
	for _, v := range []struct {
		name  string
		rules ledger.Rules
	}{{"ledger", ledger.Rules{}}, {"ledger+weak", ledger.Rules{Weak: true}}, {"ledger+siblings", ledger.Rules{Siblings: true}},
		{"ledger+members", ledger.Rules{Members: true}}, {"ledger+all", ledger.Rules{Weak: true, Siblings: true, Members: true}}} {
		bv := btVariant{Name: v.name, Rules: v.rules.String()}
		for _, name := range sortedKeys(repos) {
			rr, err := backtestRepo(ctx, l, name, repos[name], gtFor(gt, name), set, v.rules)
			if err != nil {
				return nil, err
			}
			bv.Repos = append(bv.Repos, rr)
		}
		bv.Score = scoreVariant(bv.Repos)
		rep.Variants = append(rep.Variants, bv)
	}
	return rep, nil
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

func backtestRepo(ctx context.Context, l *ledger.Ledger, name, dir string, gt []GTItem, set *evalmodel.Set, rules ledger.Rules) (btRepo, error) {
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
	if rr.Commits, err = backtestCosts(h, claims, rules); err != nil {
		return rr, err
	}
	byID := map[string]*ledger.Claim{}
	for _, c := range claims {
		byID[c.ID] = c
	}
	for _, it := range set.Items {
		if it.Source == evalmodel.SourceGTPositive && it.Repo == name {
			rr.Items = append(rr.Items, backtestItem(ctx, h, it, byID[ledger.ClaimID(name, it.Claim.Path, it.Claim.Text)], gt, rules))
		}
	}
	return rr, nil
}

// backtestCosts counts, per commit, the claims already judged (verified at an earlier
// commit) that it invalidates by itself.
func backtestCosts(h *history, claims []*ledger.Claim, rules ledger.Rules) ([]lrCommit, error) {
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
			if rs := evidenceReasons(ledger.InvalidateWith(c, ch, ch, noDoc, rules)); len(rs) > 0 {
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

func noDoc(string) (string, bool) { return "", false }

func backtestItem(ctx context.Context, h *history, it evalmodel.Item, c *ledger.Claim, gt []GTItem, rules ledger.Rules) btItem {
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
		rs := evidenceReasons(ledger.InvalidateWith(c, ch, ch, noDoc, rules))
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

func scoreVariant(repos []btRepo) btScore {
	var s btScore
	stale, pct, zero := 0, 0.0, 0
	for _, rr := range repos {
		for _, it := range rr.Items {
			if it.Status == "scored" {
				s.Scored++
				s.First += b2i(it.First)
				s.Any += b2i(it.Any)
			}
		}
		for _, c := range rr.Commits {
			s.Commits++
			stale += c.Stale
			pct += float64(c.Stale) / float64(c.Alive)
			zero += b2i(c.Stale == 0)
		}
	}
	if s.Commits > 0 {
		s.MeanStale = float64(stale) / float64(s.Commits)
		s.MeanStalePct = 100 * pct / float64(s.Commits)
		s.ZeroCommitPct = 100 * zero / s.Commits
	}
	return s
}

// union scores the impact selection, and the ledger alone OR impact, on the ledger's scored
// items (impact items matched by repository, file and line at the final revision).
func (r *ledgerBacktest) union(file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("impact report: %w", err)
	}
	var imp ReplayReport
	if err := json.Unmarshal(b, &imp); err != nil {
		return fmt.Errorf("impact report: %w", err)
	}
	type fa struct{ first, any bool }
	byItem := map[string]fa{}
	for _, rr := range imp.Repos {
		for _, it := range rr.Items {
			if it.Status == "scored" {
				byItem[fmt.Sprintf("%s|%s|%d", path.Base(rr.Repo), it.File, it.Line)] = fa{it.First, it.Any}
			}
		}
	}
	u, im := &btScore{}, &btScore{}
	for _, rr := range r.Variants[0].Repos {
		for _, it := range rr.Items {
			i, ok := byItem[fmt.Sprintf("%s|%s|%d", rr.Repo, it.Path, it.Line)]
			if it.Status != "scored" || !ok {
				continue
			}
			u.Scored++
			im.Scored++
			u.First += b2i(it.First || i.first)
			u.Any += b2i(it.Any || i.any)
			im.First += b2i(i.first)
			im.Any += b2i(i.any)
		}
	}
	r.Union, r.Impact = u, im
	return nil
}

// enrichFootprints adds weak and member footprints from eval transcripts to claims that were
// recorded without them (e.g. a ledger built before the rules existed).
func enrichFootprints(l *ledger.Ledger, resultsGlob, setsGlob string) (int, error) {
	texts := map[string]evalmodel.Item{}
	setFiles, _ := filepath.Glob(setsGlob)
	for _, f := range setFiles {
		s, err := evalmodel.LoadSet(f)
		if err != nil {
			return 0, err //nolint:wrapcheck // already descriptive
		}
		for _, it := range s.Items {
			texts[it.ID] = it
		}
	}
	byID := map[string]*ledger.Claim{}
	for i := range l.Claims {
		byID[l.Claims[i].ID] = &l.Claims[i]
	}
	files, _ := filepath.Glob(resultsGlob)
	n := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return n, fmt.Errorf("results: %w", err)
		}
		var res evalmodel.Result
		if err := json.Unmarshal(b, &res); err != nil {
			return n, fmt.Errorf("results %s: %w", f, err)
		}
		for _, ir := range res.Items {
			it, ok := texts[ir.ID]
			if !ok {
				continue
			}
			c := byID[ledger.ClaimID(it.Repo, it.Claim.Path, it.Claim.Text)]
			if c != nil && ledger.AddFootprints(c, ir.Transcript, ledger.Rules{Weak: true, Members: true}) == nil {
				n++
			}
		}
	}
	return n, nil
}

func (r ledgerBacktest) markdown() string {
	var b strings.Builder
	b.WriteString("# Ledger backtest: claims judged before their causing commits, history replayed forward\n\n")
	b.WriteString("| variant | scored | caught at first causing commit | at any causing commit | mean stale claims / commit | mean % of judged claims | commits staling nothing |\n|---|---|---|---|---|---|---|\n")
	row := func(name string, s btScore, cost bool) {
		c := "| - | - | - |"
		if cost {
			c = fmt.Sprintf("| %.2f | %.1f%% | %d%% |", s.MeanStale, s.MeanStalePct, s.ZeroCommitPct)
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s %s\n", name, s.Scored, frac(s.First, s.Scored), frac(s.Any, s.Scored), c)
	}
	for _, v := range r.Variants {
		row(v.Name, v.Score, true)
	}
	if r.Impact != nil {
		row("impact selection (same items)", *r.Impact, false)
		row("ledger OR impact", *r.Union, false)
	}
	b.WriteString("\nPer repository (ledger alone):\n\n| repo | claims | commits | mean stale/commit | base verdict confirmed |\n|---|---|---|---|---|\n")
	for _, rr := range r.Variants[0].Repos {
		sc, conf := 0, 0
		for _, it := range rr.Items {
			if it.Status == "scored" {
				sc++
				conf += b2i(it.BaseCategory == "confirmed")
			}
		}
		st := 0
		for _, c := range rr.Commits {
			st += c.Stale
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %.2f | %s |\n", rr.Repo, rr.Claims, len(rr.Commits), float64(st)/float64(max(1, len(rr.Commits))), frac(conf, sc))
	}
	return b.String()
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}
