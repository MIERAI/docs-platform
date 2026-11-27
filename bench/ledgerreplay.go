package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// ledgerReplay validates evidence-based invalidation on real history, without any model:
//
//   - cost: every first-parent commit of each repository is replayed against the ledger's
//     claims (a claim counts from the first commit whose parent has its line): how many
//     claims would a commit send back to the judge?
//   - recall: for each ground-truth positive of the eval set, the commits after its line was
//     written that touched the ground truth's evidence paths are its causing commits (as in
//     `bench replay`); is the claim invalidated by its evidence at the first causing commit,
//     or at any? The claim's evidence footprint comes from the ledger (judged at the set's
//     revision), which is a proxy for the footprint a claim judged earlier would have had.
type ledgerReplay struct {
	Repos []lrRepo `json:"repos"`
}

type lrRepo struct {
	Repo    string     `json:"repo"`
	Claims  int        `json:"claims"`
	Commits []lrCommit `json:"commits"`
	Items   []lrItem   `json:"items"`
}

// lrCommit: Stale and Reasons without quote scopes (ledgers before 1.1), StaleScoped and
// Carried (carried-forward quotes by kind) with them.
type lrCommit struct {
	SHA           string         `json:"sha"`
	Alive         int            `json:"alive_claims"`
	Stale         int            `json:"stale_claims"`
	Reasons       map[string]int `json:"reasons,omitempty"`
	StaleScoped   int            `json:"stale_claims_scoped"`
	ReasonsScoped map[string]int `json:"reasons_scoped,omitempty"`
	Carried       map[string]int `json:"carried_quotes_scoped,omitempty"`
}

type lrItem struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Line      int      `json:"line"`
	Status    string   `json:"status"` // scored | no_claim | no_causing_commit
	Why       string   `json:"why,omitempty"`
	Causing   int      `json:"causing_commits"`
	First     bool     `json:"flagged_at_first_causing_commit"`
	Any       bool     `json:"flagged_at_any_causing_commit"`
	Flagging  int      `json:"commits_flagging_after_doc_commit"`
	FirstFlag string   `json:"first_flagging_commit,omitempty"`
	Reasons   []string `json:"reasons_at_first_flag,omitempty"`
	// with quote scopes
	FirstScoped    bool `json:"flagged_at_first_causing_commit_scoped"`
	AnyScoped      bool `json:"flagged_at_any_causing_commit_scoped"`
	FlaggingScoped int  `json:"commits_flagging_after_doc_commit_scoped"`
}

func runLedgerReplay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ledger-replay", flag.ContinueOnError)
	file := fs.String("ledger", "", "claim ledger built at the eval set's revisions")
	gtDir := fs.String("gt", "", "directory of ground-truth *.jsonl files")
	setFile := fs.String("set", "", "eval set whose gt_positive items are scored")
	js := fs.String("json", "", "write the JSON report here")
	repos := dirFlag{}
	fs.Var(repos, "repo", "name=DIR: a checkout with history (repeatable)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("ledger-replay flags: %w", err)
	}
	if *file == "" || *gtDir == "" || *setFile == "" || len(repos) == 0 {
		return errors.New("ledger-replay: -ledger, -gt, -set and -repo are required")
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
	rep := ledgerReplay{}
	for _, name := range sortedKeys(repos) {
		rr, err := replayLedgerRepo(ctx, l, name, repos[name], gtFor(gt, name), set)
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

func gtFor(gt map[string][]GTItem, name string) []GTItem {
	for k, v := range gt {
		if path.Base(k) == name {
			return v
		}
	}
	return nil
}

func replayLedgerRepo(ctx context.Context, l *ledger.Ledger, name, dir string, gt []GTItem, set *evalmodel.Set) (lrRepo, error) {
	rr := lrRepo{Repo: name}
	var claims []*ledger.Claim
	for i := range l.Claims {
		if l.Claims[i].Repo == name {
			claims = append(claims, &l.Claims[i])
		}
	}
	rr.Claims = len(claims)
	out, err := repo.Git(ctx, dir, "log", "--first-parent", "--reverse", "--format=%H", "HEAD")
	if err != nil {
		return rr, fmt.Errorf("%s: %w", name, err)
	}
	commits := strings.Fields(out)
	changes := map[string]*ledger.Changes{}
	diff := func(c string) (*ledger.Changes, error) {
		if ch, ok := changes[c]; ok {
			return ch, nil
		}
		ch, err := ledger.Diff(ctx, dir, c+"^", c)
		if err != nil {
			return nil, err //nolint:wrapcheck // already descriptive
		}
		changes[c] = ch // only successes: a cached failure would come back as a nil change set
		return ch, nil
	}
	an := &anchor{ctx: ctx, dir: dir, src: map[string]string{}}
	if rr.Commits, err = commitCosts(ctx, dir, claims, commits[1:], diff, an); err != nil {
		return rr, err
	}
	for _, it := range set.Items {
		if it.Source != evalmodel.SourceGTPositive || it.Repo != name {
			continue
		}
		rr.Items = append(rr.Items, scoreLedgerItem(ctx, dir, it, claims, gt, diff, an))
	}
	return rr, nil
}

// commitCosts counts, per commit, the claims whose line existed before it and that it would
// invalidate.
func commitCosts(ctx context.Context, dir string, claims []*ledger.Claim, commits []string, diff func(string) (*ledger.Changes, error), an *anchor) ([]lrCommit, error) {
	alive := map[string]bool{}
	first := true
	var out []lrCommit
	for _, c := range commits {
		ch, err := diff(c)
		if err != nil {
			return nil, err
		}
		docAt := func(p string) (string, bool) { return ledger.FileAt(ctx, dir, c, p) }
		for _, cl := range claims {
			if _, touched := ch.Paths[cl.Path]; first || touched {
				text, ok := ledger.FileAt(ctx, dir, c+"^", cl.Path)
				alive[cl.ID] = ok && hasLine(text, cl.Text)
			}
		}
		first = false
		out = append(out, staleAt(c, claims, alive, ch, docAt, an))
	}
	return out, nil
}

// staleAt counts the alive claims one commit invalidates, with their reasons: without quote
// scopes (every change of a quoted file re-opens the claim) and with them (re-anchored at the
// commit's parent; a change outside the scope carries the verdict forward).
func staleAt(c string, claims []*ledger.Claim, alive map[string]bool, ch *ledger.Changes, docAt func(string) (string, bool), an *anchor) lrCommit {
	lc := lrCommit{SHA: c[:12], Reasons: map[string]int{}, ReasonsScoped: map[string]int{}, Carried: map[string]int{}}
	for _, cl := range claims {
		if !alive[cl.ID] {
			continue
		}
		lc.Alive++
		if s := ledger.Invalidate(unscoped(cl), ch, docAt); s.Status == ledger.Stale {
			lc.Stale++
			for _, r := range s.Reasons {
				lc.Reasons[r.Kind]++
			}
		}
		s := ledger.Invalidate(an.at(cl, ch), ch, docAt)
		if s.Status == ledger.Stale {
			lc.StaleScoped++
			for _, r := range s.Reasons {
				lc.ReasonsScoped[r.Kind]++
			}
		}
		for _, k := range s.Carried {
			lc.Carried[k.Kind]++
		}
	}
	return lc
}

func hasLine(text, want string) bool {
	return nearestLine(text, want, 0) > 0
}

// nearestLine is the number of the line equal (whitespace-normalised) to want nearest hint; 0
// when there is none.
func nearestLine(text, want string, hint int) int {
	w := strings.Join(strings.Fields(want), " ")
	best := 0
	for i, l := range strings.Split(text, "\n") {
		if strings.Join(strings.Fields(l), " ") != w {
			continue
		}
		if n := i + 1; best == 0 || abs(n-hint) < abs(best-hint) {
			best = n
		}
	}
	return best
}

// unscoped is the claim as a ledger written before quote scopes holds it.
func unscoped(cl *ledger.Claim) *ledger.Claim {
	c := *cl
	c.Evidence.Quotes = append([]ledger.Quote(nil), cl.Evidence.Quotes...)
	for i := range c.Evidence.Quotes {
		c.Evidence.Quotes[i].Scope = nil
	}
	return &c
}

// anchor re-anchors claims for a replayed commit. The ledger's claims were judged at the
// set's revision, later than the commit; a claim recorded before the commit would have quoted
// the same text at the commit's parent. So each quote whose file the commit modifies is looked
// up there (its line's text, nearest its line) and gets that blob and the scope computed
// there; a quote whose text the parent does not have keeps no scope (any change re-opens it,
// as without scopes).
type anchor struct {
	ctx context.Context
	dir string
	src map[string]string
}

func (a *anchor) at(cl *ledger.Claim, ch *ledger.Changes) *ledger.Claim {
	c := unscoped(cl)
	for i := range c.Evidence.Quotes {
		q := &c.Evidence.Quotes[i]
		if ch.Paths[q.Path] != 'M' {
			continue
		}
		lines := strings.Split(a.read(q.Blob, ""), "\n")
		if q.Line < 1 || q.Line > len(lines) {
			continue
		}
		src := a.read(ch.From, q.Path)
		blob, ok := ledger.BlobAt(a.ctx, a.dir, ch.From, q.Path)
		n := nearestLine(src, lines[q.Line-1], q.Line)
		if !ok || n == 0 {
			continue
		}
		q.Line, q.Blob, q.Scope = n, blob, ledger.ComputeScope(q.Path, src, n)
	}
	return c
}

// read is a blob's content (p empty) or p's content at a revision ("" when unreadable).
func (a *anchor) read(rev, p string) string {
	k := rev + "\x00" + p
	if s, ok := a.src[k]; ok {
		return s
	}
	var s string
	if p == "" {
		s, _ = repo.Git(a.ctx, a.dir, "show", rev)
	} else {
		s, _ = ledger.FileAt(a.ctx, a.dir, rev, p)
	}
	a.src[k] = s
	return s
}

// flags tallies the commits that flag one claim by its evidence.
type flags struct {
	first, any bool
	n          int
	firstFlag  string
	reasons    []string
}

func (f *flags) add(c string, rs []string, causing map[string]bool, firstCausing string) {
	if len(rs) == 0 {
		return
	}
	f.n++
	if f.firstFlag == "" {
		f.firstFlag, f.reasons = c[:12], rs
	}
	if causing[c] {
		f.any = true
		f.first = f.first || c == firstCausing
	}
}

func scoreLedgerItem(ctx context.Context, dir string, it evalmodel.Item, claims []*ledger.Claim, gt []GTItem, diff func(string) (*ledger.Changes, error), an *anchor) lrItem {
	li := lrItem{ID: it.ID, Path: it.Claim.Path, Line: it.Claim.Line, Status: "scored"}
	var cl *ledger.Claim
	for _, c := range claims {
		if c.Path == it.Claim.Path && c.Line == it.Claim.Line {
			cl = c
		}
	}
	if cl == nil {
		li.Status, li.Why = "no_claim", "the ledger has no claim for this line"
		return li
	}
	tr := traceItem(ctx, dir, gtItem(gt, it))
	if len(tr.causing) == 0 {
		li.Status, li.Why = "no_causing_commit", tr.why
		return li
	}
	li.Causing = len(tr.causing)
	causing := map[string]bool{}
	for _, c := range tr.causing {
		causing[c] = true
	}
	noDoc := func(string) (string, bool) { return "", false }
	var plain, scoped flags
	out, _ := repo.Git(ctx, dir, "log", "--first-parent", "--reverse", "--format=%H", tr.docCommit+"..HEAD")
	for _, c := range strings.Fields(out) {
		ch, err := diff(c)
		if err != nil {
			continue
		}
		plain.add(c, evidenceReasons(ledger.Invalidate(unscoped(cl), ch, noDoc)), causing, tr.causing[0])
		scoped.add(c, evidenceReasons(ledger.Invalidate(an.at(cl, ch), ch, noDoc)), causing, tr.causing[0])
	}
	li.First, li.Any, li.Flagging, li.FirstFlag, li.Reasons = plain.first, plain.any, plain.n, plain.firstFlag, plain.reasons
	li.FirstScoped, li.AnyScoped, li.FlaggingScoped = scoped.first, scoped.any, scoped.n
	return li
}

// evidenceReasons drops the document's own changes: the line was not edited between its
// commit and the set's revision, so only evidence can have flagged it.
func evidenceReasons(s ledger.ClaimStatus) []string {
	var out []string
	for _, r := range s.Reasons {
		if r.Kind != ledger.ReasonDocLineChanged && r.Kind != ledger.ReasonDocRemoved && r.Kind != ledger.ReasonNoEvidence {
			out = append(out, r.Kind)
		}
	}
	return out
}

func gtItem(gt []GTItem, it evalmodel.Item) GTItem {
	for _, g := range gt {
		if g.File == it.Claim.Path && g.Line != nil && *g.Line == it.Claim.Line {
			return g
		}
	}
	n := it.Claim.Line
	return GTItem{File: it.Claim.Path, Line: &n}
}

func (r ledgerReplay) markdown() string {
	var b strings.Builder
	b.WriteString("# Ledger replay: evidence-based invalidation on real history\n\n")
	b.WriteString("Quote scopes off: any change of a quoted file re-opens the claim (ledgers before 1.1). On: only a change touching the claim's scope (Go declaration, YAML key, line ±3).\n\n")
	b.WriteString("| repo | scopes | claims | commits | stale total | mean stale/commit | median | p90 | max | commits staling nothing | mean % of alive claims |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, rr := range r.Repos {
		costRow(&b, rr, false)
		costRow(&b, rr, true)
	}
	b.WriteString("\n| repo | scopes | positives | scored | flagged at first causing commit | at any causing commit | flagged by some later commit | median commits flagging |\n|---|---|---|---|---|---|---|---|\n")
	for _, rr := range r.Repos {
		itemRow(&b, rr, false)
		itemRow(&b, rr, true)
	}
	return b.String()
}

func onOff(scoped bool) string {
	if scoped {
		return "on"
	}
	return "off"
}

// costRow writes one repository's re-judgement cost row.
func costRow(b *strings.Builder, rr lrRepo, scoped bool) {
	var st []int
	zero, pct, total := 0, 0.0, 0
	for _, c := range rr.Commits {
		s := c.Stale
		if scoped {
			s = c.StaleScoped
		}
		st = append(st, s)
		total += s
		if s == 0 {
			zero++
		}
		if c.Alive > 0 {
			pct += float64(s) / float64(c.Alive)
		}
	}
	sort.Ints(st)
	n := max(1, len(st))
	fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %.2f | %d | %d | %d | %d%% | %.1f%% |\n", rr.Repo, onOff(scoped), rr.Claims, len(st), total, mean(st), pick(st, 0.5), pick(st, 0.9), pick(st, 1), 100*zero/n, 100*pct/float64(n))
}

// itemRow writes one repository's recall row.
func itemRow(b *strings.Builder, rr lrRepo, scoped bool) {
	scored, first, anyc, some := 0, 0, 0, 0
	var fl []int
	for _, it := range rr.Items {
		if it.Status != "scored" {
			continue
		}
		f, a, n := it.First, it.Any, it.Flagging
		if scoped {
			f, a, n = it.FirstScoped, it.AnyScoped, it.FlaggingScoped
		}
		scored++
		fl = append(fl, n)
		first += b2i(f)
		anyc += b2i(a)
		some += b2i(n > 0)
	}
	sort.Ints(fl)
	fmt.Fprintf(b, "| %s | %s | %d | %d | %s | %s | %s | %d |\n", rr.Repo, onOff(scoped), len(rr.Items), scored, frac(first, scored), frac(anyc, scored), frac(some, scored), pick(fl, 0.5))
}

func mean(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0
	for _, x := range xs {
		s += x
	}
	return float64(s) / float64(len(xs))
}

func pick(sorted []int, q float64) int {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}

func frac(a, b int) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%d/%d (%d%%)", a, b, 100*a/b)
}

// dirFlag collects name=DIR pairs.
type dirFlag map[string]string

func (d dirFlag) String() string { return fmt.Sprint(map[string]string(d)) }

func (d dirFlag) Set(v string) error {
	name, dir, ok := strings.Cut(v, "=")
	if !ok || name == "" || dir == "" {
		return fmt.Errorf("want name=DIR, got %q", v)
	}
	d[name] = dir
	return nil
}
