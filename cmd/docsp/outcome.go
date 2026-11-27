package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/outcome"
	"github.com/MIERAI/docs-platform/internal/prreview"
)

// fetchComment finds a review comment on a pull request (a variable so tests fake gh).
var fetchComment = findComment

// outcomeNow is the clock of the outcome events (a variable so tests pin it).
var outcomeNow = time.Now

// cmdOutcome measures the precision of docs-platform's own problem reports
// (docs-platform/outcome/v1): diff, ticks, sample, label, report. No model is called.
func cmdOutcome(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	sub := map[string]command{
		"diff": cmdOutcomeDiff, "ticks": cmdOutcomeTicks, "sample": cmdOutcomeSample,
		"label": cmdOutcomeLabel, "report": cmdOutcomeReport,
	}
	if len(args) == 0 || sub[args[0]] == nil {
		_, _ = fmt.Fprintln(stderr, "usage: docsp outcome diff|ticks|sample|label|report [flags]")
		return exitUsage, nil
	}
	return sub[args[0]](ctx, args[1:], stdout, stderr)
}

func newOutcomeFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("outcome "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs, fs.String("stream", "", "the outcome stream (JSON Lines, appended; mode 0600) (required)")
}

func onlyRepo(l *ledger.Ledger, name string) {
	kept := l.Claims[:0]
	for _, c := range l.Claims {
		if c.Repo == name {
			kept = append(kept, c)
		}
	}
	l.Claims = kept
}

// cmdOutcomeDiff appends what one run did to the reported claims: -before is the ledger at
// the start of the run, -after the ledger after the recheck.
func cmdOutcomeDiff(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, stream := newOutcomeFlags("diff", stderr)
	before := fs.String("before", "", "the ledger before the run (a missing file is an empty ledger)")
	after := fs.String("after", "", "the ledger after the run (required)")
	repoName := fs.String("repo-name", "", "logical repository name (default: directory name)")
	ref := fs.String("ref", "HEAD", "the revision the run judged")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *after == "" || *stream == "" {
		return exitUsage, errors.New("-after and -stream are required")
	}
	b, err := ledger.Load(*before)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	a, err := ledger.Load(*after)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	root := rootArg(fs)
	name := *repoName
	if name == "" {
		name = repoBase(root)
	}
	onlyRepo(b, name)
	onlyRepo(a, name)
	tree, err := ledger.ReadTree(ctx, root, *ref)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	blobAt := func(p string) (string, bool) { s, ok := tree.Blobs[p]; return s, ok }
	docAt := func(p string) (string, bool) { return ledger.FileAt(ctx, root, tree.Ref, p) }
	ev := outcome.Diff(b, a, tree.Ref, outcomeNow(), blobAt, docAt)
	if err := outcome.Append(*stream, ev); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	n := map[string]int{}
	for _, e := range ev {
		n[e.Kind]++
		if e.How == outcome.HowFlip {
			n["flip"]++
		}
	}
	_, _ = fmt.Fprintf(stdout, "docsp outcome diff: %d reported, %d resolved, %d flips, %d stable re-judgements\n",
		n[outcome.KindReported], n[outcome.KindResolved], n["flip"], n[outcome.KindRejudged])
	return exitOK, nil
}

// cmdOutcomeTicks appends the boxes ticked in a pull request's review comment; a box already
// in the stream for that pull request is not appended again.
func cmdOutcomeTicks(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, stream := newOutcomeFlags("ticks", stderr)
	ghRepo := fs.String("github-repo", "", "owner/name of the repository (required)")
	pr := fs.Int("pr", 0, "the pull request (required)")
	ref := fs.String("ref", "", "the commit the ticks are recorded at (e.g. the merge commit)")
	repoName := fs.String("repo-name", "", "logical repository name (default: directory name)")
	ledgerPath := fs.String("ledger", "", "link the ticks to this ledger's claims (by path and line)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *ghRepo == "" || *pr <= 0 || *stream == "" {
		return exitUsage, errors.New("-github-repo, -pr and -stream are required")
	}
	name := *repoName
	if name == "" {
		name = repoBase(rootArg(fs))
	}
	_, body, err := fetchComment(ctx, *ghRepo, *pr, prreview.Marker)
	if err != nil {
		return exitUsage, err
	}
	ev := outcome.FromFeedback(name, *pr, *ref, factorypr.Ticked(body), outcomeNow())
	if *ledgerPath != "" {
		l, err := ledger.Load(*ledgerPath)
		if err != nil {
			return exitUsage, err //nolint:wrapcheck // already descriptive
		}
		outcome.Match(ev, l)
	}
	return appendUnseen(*stream, ev, "ticks", stdout)
}

func appendUnseen(stream string, ev []outcome.Event, what string, stdout io.Writer) (int, error) {
	have, err := outcome.Load(stream)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	fresh := outcome.Unseen(have, ev)
	if err := outcome.Append(stream, fresh); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	_, _ = fmt.Fprintf(stdout, "docsp outcome %s: %d read, %d new\n", what, len(ev), len(fresh))
	return exitOK, nil
}

// cmdOutcomeSample writes the audit sheet: a uniform sample of open problem claims for a
// person to judge (tp, fp or skip).
func cmdOutcomeSample(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, stream := newOutcomeFlags("sample", stderr)
	ledgerPath := fs.String("ledger", "", "the ledger (required)")
	n := fs.Int("n", 20, "claims to sample")
	seed := fs.Uint64("seed", 1, "random seed (the same seed draws the same claims)")
	ghRepo := fs.String("github-repo", "", "owner/name: links to the document and code lines")
	repoName := fs.String("repo-name", "", "sample only this repository's claims")
	out := fs.String("out", "", "write the CSV here (mode 0600; default stdout)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *ledgerPath == "" {
		return exitUsage, errors.New("-ledger is required")
	}
	l, err := ledger.Load(*ledgerPath)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if *repoName != "" {
		onlyRepo(l, *repoName)
	}
	var have []outcome.Event
	if *stream != "" {
		if have, err = outcome.Load(*stream); err != nil {
			return exitUsage, err //nolint:wrapcheck // already descriptive
		}
	}
	root := rootArg(fs)
	lines := func(ref, p string, line int) (string, bool) {
		text, ok := ledger.FileAt(ctx, root, ref, p)
		if !ok || line < 1 {
			return "", false
		}
		ls := strings.Split(text, "\n")
		if line > len(ls) {
			return "", false
		}
		return ls[line-1], true
	}
	claims := outcome.Sample(l, outcome.Labelled(have), *n, *seed)
	var b strings.Builder
	if err := outcome.WriteCSV(&b, claims, *ghRepo, lines); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return writeText(b.String(), *out, stdout)
}

func writeText(s, out string, stdout io.Writer) (int, error) {
	if out == "" {
		_, err := io.WriteString(stdout, s)
		return exitOK, err //nolint:wrapcheck // stdout write
	}
	if err := os.WriteFile(out, []byte(s), 0o600); err != nil {
		return exitUsage, fmt.Errorf("write %s: %w", out, err)
	}
	return exitOK, nil
}

// cmdOutcomeLabel appends the labels of a filled audit sheet (idempotent).
func cmdOutcomeLabel(_ context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, stream := newOutcomeFlags("label", stderr)
	csvPath := fs.String("csv", "", "the audit sheet with human_verdict filled in (required)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *csvPath == "" || *stream == "" {
		return exitUsage, errors.New("-csv and -stream are required")
	}
	f, err := os.Open(*csvPath)
	if err != nil {
		return exitUsage, fmt.Errorf("audit csv: %w", err)
	}
	defer func() { _ = f.Close() }()
	ev, err := outcome.LabelsFromCSV(f, outcomeNow())
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return appendUnseen(*stream, ev, "label", stdout)
}

// cmdOutcomeReport prints the precision SLI, the flip rate and the error budget.
func cmdOutcomeReport(_ context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs, stream := newOutcomeFlags("report", stderr)
	target := fs.Float64("target", outcome.DefaultTarget, "precision objective")
	maxFlip := fs.Float64("max-flip-rate", outcome.DefaultMaxFlipRate, "flip-rate objective (at most)")
	format := fs.String("format", "text", "text, json or markdown")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *stream == "" {
		return exitUsage, errors.New("-stream is required")
	}
	ev, err := outcome.Load(*stream)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	r := outcome.Report(ev, *target, *maxFlip)
	switch *format {
	case "text":
		r.Text(stdout)
	case "markdown":
		r.Markdown(stdout)
	case "json":
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return exitUsage, fmt.Errorf("encode: %w", err)
		}
		_, err = stdout.Write(append(b, '\n'))
		return exitOK, err //nolint:wrapcheck // stdout write
	default:
		return exitUsage, fmt.Errorf("-format %q: want text, json or markdown", *format)
	}
	return exitOK, nil
}
