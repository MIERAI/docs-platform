package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/consistency"
	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/rulejudge"
)

// cmdReport reports the ledger's validated problems for people: contradicted and dangling
// at once, aged wording and incomplete lists in batches, suppressions honoured until they
// expire. It never fails: documentation updates are never forced.
func cmdReport(_ context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	flush := fs.Bool("flush", false, "release the batched findings now")
	drafts := fs.String("drafts", "", "drafts log: add 'generate or link instead' recommendations")
	dry := fs.Bool("dry-run", false, "do not record what was reported in -state")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	l, in, st, _, _, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	now := time.Now()
	r := consistency.BuildReport(l, in.Consistency, &st.Report, now, *flush)
	if *drafts != "" {
		dl, err := draft.LoadLog(*drafts)
		if err != nil {
			return exitUsage, err //nolint:wrapcheck // already descriptive
		}
		r.Recommendations = dl.Recommend(in.Consistency.WithDefaults().RecommendAfter, now)
	}
	if c.state != "" && !*dry {
		if err := st.Save(c.state); err != nil {
			return exitUsage, err //nolint:wrapcheck // already descriptive
		}
	}
	return exitOK, c.emitReport(r, func(w io.Writer) { writeReport(w, r) }, stdout, stderr)
}

func writeReport(w io.Writer, r *consistency.ConsistencyReport) {
	batch := "not due"
	if r.BatchDue {
		batch = "released"
	}
	_, _ = fmt.Fprintf(w, "docsp report: %d immediate, %d batched (batch %s; %d held, next %s), %d suppressed, %d expired suppressions\n",
		len(r.Immediate), len(r.Batched), batch, r.Held, r.NextBatch, len(r.Suppressed), len(r.ExpiredSuppressions))
	for _, f := range append(append([]consistency.Finding{}, r.Immediate...), r.Batched...) {
		_, _ = fmt.Fprintf(w, "%-9s %s:%d [%s] %s\n", f.Severity, f.Path, f.Line, f.Category, f.Text)
	}
	for _, x := range r.ExpiredSuppressions {
		_, _ = fmt.Fprintf(w, "expired suppression (%s, %s): %s%s\n", x.Expires, x.Reason, x.Claim, x.Path)
	}
	for _, rec := range r.Recommendations {
		_, _ = fmt.Fprintf(w, "recommend %s (%d drafts in 90 days): %s\n", rec.Path, rec.Drafts, rec.Advice)
	}
}

// cmdDraft proposes minimal fixes for the ledger's problems (the draft task's provider),
// checks them against the minimal-diff rules and logs them. Nothing is applied.
func cmdDraft(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) > 0 {
		if sub, ok := draftSubs[args[0]]; ok {
			return sub(ctx, args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("draft", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	logFile := fs.String("log", "", "drafts log (JSON Lines, mode 0600; required)")
	maxN := fs.Int("max", 20, "drafts per run")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	l, _, _, root, name, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	if *logFile == "" {
		return exitUsage, errors.New("-log is required")
	}
	dl, err := draft.LoadLog(*logFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	claims := draftable(l, dl, name, *maxN)
	var g *judge.Guard
	if needsModel(claims) { // rules verdicts get deterministic drafts: no provider needed for them
		g, err = check.NewJudge(c.instanceFile, "draft", false)
		if err != nil || g == nil {
			return exitUsage, errors.Join(errors.New("draft needs ai.draft in the instance configuration"), err)
		}
		defer func() { _ = g.Close() }()
	}
	rules, err := rulejudge.LoadDir(root, nil)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	made := makeDrafts(ctx, g, rules, root, claims)
	dl.Entries = append(dl.Entries, made...)
	if err := dl.Save(*logFile); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return exitOK, c.emitReport(made, func(w io.Writer) { writeDrafts(w, made, g) }, stdout, stderr)
}

// writeDrafts is draft's text report: one line per draft, then the run's cost.
func writeDrafts(w io.Writer, made []draft.Entry, g *judge.Guard) {
	for _, e := range made {
		_, _ = fmt.Fprintf(w, "%-19s %s:%d [%s, %d tokens] %s\n", e.Status, e.Path, e.Line, e.Action, e.DiffTokens, e.Draft)
		for _, p := range e.Problems {
			_, _ = fmt.Fprintln(w, "    "+p)
		}
	}
	if g == nil {
		_, _ = fmt.Fprintf(w, "docsp draft: %d drafts (proposals only, never applied); rules only, $0\n", len(made))
		return
	}
	_, _ = fmt.Fprintf(w, "docsp draft: %d drafts (proposals only, never applied); %s, $%.4f\n", len(made), g.Name(), g.Stats().CostUSD)
}

// draftable are the repository's problem claims without an open draft, immediate first.
func draftable(l *ledger.Ledger, dl *draft.Log, repoName string, n int) []*ledger.Claim {
	rank := map[string]int{"contradicted": 0, "dangling_reference": 0, "incomplete_list": 1, "outdated_wording": 1}
	var out []*ledger.Claim
	for i := range l.Claims {
		c := &l.Claims[i]
		if _, problem := rank[c.Category]; problem && c.Repo == repoName && c.Target == nil && !dl.Open(c.ID) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Category] < rank[out[j].Category] })
	return out[:min(n, len(out))]
}

func proposeDraft(ctx context.Context, g *judge.Guard, root string, c *ledger.Claim, now time.Time) draft.Entry {
	ev := draft.Evidence(ctx, root, c)
	e := draft.Entry{
		ClaimID: c.ID, Repo: c.Repo, Path: c.Path, Line: c.Line, Category: c.Category, Original: c.Text,
		ProposedAt: now.UTC().Format(time.RFC3339), ProposedRef: c.VerifiedRef, Model: g.Name(), Status: draft.RejectedByHarness,
		DocBlob: c.DocBlob,
	}
	out, err := g.Judge(ctx, draft.Request(c, ev))
	switch {
	case err != nil:
		e.Problems = []string{"no draft: " + err.Error()}
		return e
	case out.Response.Draft == nil:
		e.Problems = []string{"no draft: the model proposed no correction"}
		return e
	}
	texts := make([]string, 0, len(ev))
	for _, x := range ev {
		texts = append(texts, x.Text)
	}
	e.Draft = out.Response.Draft.Text
	chk := draft.Constrain(c.Text, e.Draft, texts)
	e.Action, e.DiffTokens, e.Problems = chk.Action, chk.DiffTokens, chk.Problems
	if chk.OK {
		e.Status = draft.Proposed
	}
	return e
}

// cmdDrafts tracks what became of the logged drafts (accepted, superseded, reverted) from
// the documents at a revision, and reports acceptance rate, revert rate, diff size and the
// documents to generate or link instead.
func cmdDrafts(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("drafts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	logFile := fs.String("log", "", "drafts log (required)")
	after := fs.Int("recommend-after", 3, "drafts of one document within 90 days after which to recommend generating or linking")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *logFile == "" {
		return exitUsage, errors.New("-log is required")
	}
	root := rootArg(fs)
	name := c.repoName
	if name == "" {
		name = repoBase(root)
	}
	dl, err := draft.LoadLog(*logFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	now := time.Now()
	if err := dl.Track(ctx, root, name, c.ref, now); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if err := dl.Save(*logFile); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	out := struct {
		Rates           draft.Rates            `json:"rates"`
		Recommendations []draft.Recommendation `json:"recommendations,omitempty"`
	}{dl.Stats(), dl.Recommend(*after, now)}
	return exitOK, c.emitReport(out, func(w io.Writer) {
		r := out.Rates
		_, _ = fmt.Fprintf(w, "docsp drafts: %d proposed (%d rejected by the harness), %d decided: acceptance %s, revert %s, mean diff %.1f tokens, by action %v\n",
			r.Proposed, r.RejectedByHarness, r.Decided, pct(r.AcceptanceRate), pct(r.RevertRate), r.MeanDiffTokens, r.ByAction)
		for _, rec := range out.Recommendations {
			_, _ = fmt.Fprintf(w, "recommend %s (%d drafts in 90 days): %s\n", rec.Path, rec.Drafts, rec.Advice)
		}
	}, stdout, stderr)
}

func pct(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100**v)
}
