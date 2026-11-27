package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/MIERAI/docs-platform/internal/consistency"
	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/prreview"
	"github.com/MIERAI/docs-platform/internal/prtext"
)

// prOutputs are the flags of what a PR check writes besides its comment (pr-comment and
// factory-pr share them).
type prOutputs struct {
	language, summaryURL, summaryOut, annotationsOut string
	maxProblems                                      int
}

func (o *prOutputs) register(fs *flag.FlagSet) {
	fs.StringVar(&o.language, "language", "zh", "language of the comment: zh, en or ja (the tick-box markers are the same in all)")
	fs.IntVar(&o.maxProblems, "max-problems", prtext.DefaultMaxProblems, "problems one comment shows; the rest are counted with a link to -summary-url")
	fs.StringVar(&o.summaryURL, "summary-url", "", "where the full list is (the job summary of the run)")
	fs.StringVar(&o.summaryOut, "summary-out", "", "write the job summary (Markdown, every problem) here")
	fs.StringVar(&o.annotationsOut, "annotations-out", "", "write GitHub workflow-command annotations (one warning per problem line) here")
}

func (o *prOutputs) lang() (prtext.Lang, error) {
	l, err := prtext.Parse(o.language)
	if err != nil {
		return "", fmt.Errorf("-language: %w", err)
	}
	return l, nil
}

// write saves the summary and the annotations.
func (o *prOutputs) write(summary string, annotations []string) error {
	if o.summaryOut != "" {
		if err := os.WriteFile(o.summaryOut, []byte(summary), 0o600); err != nil {
			return fmt.Errorf("summary: %w", err)
		}
	}
	if o.annotationsOut != "" {
		s := strings.Join(annotations, "\n")
		if s != "" {
			s += "\n"
		}
		if err := os.WriteFile(o.annotationsOut, []byte(s), 0o600); err != nil {
			return fmt.Errorf("annotations: %w", err)
		}
	}
	return nil
}

// cmdPRComment compares the default branch's ledger with a copy rechecked at a PR's head and
// reports the problems the change introduces, optionally as one PR comment (updated in place
// only when the findings change; the boxes people ticked are read back and kept).
func cmdPRComment(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("pr-comment", flag.ContinueOnError)
	fs.SetOutput(stderr)
	before := fs.String("before", "", "the default branch's ledger (required)")
	after := fs.String("after", "", "a copy of it after `docsp recheck` at the PR's head (required)")
	repoName := fs.String("repo-name", "", "logical repository name (default: directory name)")
	out := fs.String("out", "", "write the pr-review/v1 JSON here (mode 0600)")
	commentOut := fs.String("comment-out", "", "write the PR comment (Markdown) here")
	post := fs.Int("post-pr", 0, "post the comment to this pull request with gh (needs -github-repo)")
	ghRepo := fs.String("github-repo", "", "owner/name of the repository (links; -post-pr)")
	schedFile := fs.String("schedule", "", "the PR's schedule/v1: a required claim the budget left out makes the run incomplete")
	recheckFile := fs.String("recheck", "", "the recheck JSON: failed or missing judgments make the run incomplete")
	incomplete := fs.String("incomplete", "", "comma-separated reasons the run did not finish (budget, judge_failures, partial, aborted)")
	var po prOutputs
	po.register(fs)
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *before == "" || *after == "" || (*post > 0 && *ghRepo == "") {
		return exitUsage, errors.New("-before and -after are required; -post-pr needs -github-repo")
	}
	lang, err := po.lang()
	if err != nil {
		return exitUsage, err
	}
	reasons, err := incompleteReasons(*schedFile, *recheckFile, *incomplete)
	if err != nil {
		return exitUsage, err
	}
	rep, name, err := comparePR(ctx, *before, *after, *repoName, rootArg(fs))
	if err != nil {
		return exitUsage, err
	}
	rep.Repo, rep.GitHub, rep.Incomplete = name, *ghRepo, reasons
	rep.Lang, rep.MaxProblems, rep.SummaryURL = lang, po.maxProblems, po.summaryURL
	if err := emitPRReview(ctx, rep, *commentOut, *ghRepo, *post, stderr); err != nil {
		return exitUsage, err
	}
	if err := writePRReview(rep, &po, *out, stdout); err != nil {
		return exitUsage, err
	}
	return exitOK, nil
}

// comparePR compares the two ledgers at the checkout's HEAD.
func comparePR(ctx context.Context, before, after, repoName, root string) (*prreview.Report, string, error) {
	b, err := ledger.Load(before)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already descriptive
	}
	a, err := ledger.Load(after)
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already descriptive
	}
	name := repoName
	if name == "" {
		name = repoBase(root)
	}
	lb, err := ledger.NewBuilder(ctx, root, name, "HEAD")
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already descriptive
	}
	head, err := ledger.Resolve(ctx, root, "HEAD")
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already descriptive
	}
	return prreview.Compare(ctx, b, a, head, lb), name, nil
}

// writePRReview writes the summary, the annotations, the JSON and the one-line result.
func writePRReview(rep *prreview.Report, po *prOutputs, out string, stdout io.Writer) error {
	if err := po.write(rep.Summary(), rep.Annotations()); err != nil {
		return err
	}
	if out != "" {
		if err := writeJSON(rep, out, stdout); err != nil {
			return err
		}
	}
	state := ""
	if len(rep.Incomplete) > 0 {
		state = " (incomplete: " + strings.Join(rep.Incomplete, ", ") + ")"
	}
	_, _ = fmt.Fprintf(stdout, "docsp pr-comment: %d claims judged at the head, %d new problems%s\n", rep.Rechecked, len(rep.Problems), state)
	return nil
}

// incompleteReasons reads why a PR's run did not finish: the schedule's shortfall (the
// budget left claims this change made stale out), the recheck's failures and missing
// judgments, and the reasons the caller states.
func incompleteReasons(schedFile, recheckFile, stated string) ([]string, error) {
	var out []string
	add := func(r string) {
		if r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	for _, r := range strings.Split(stated, ",") {
		add(strings.TrimSpace(r))
	}
	picked := -1
	if schedFile != "" {
		s, err := readSchedule(schedFile)
		if err != nil {
			return nil, err
		}
		picked = len(s.Picked)
		if slices.ContainsFunc(s.Shortfall, func(p consistency.Pick) bool { return p.Priority == consistency.PriorityStale }) {
			add(prtext.ReasonBudget)
		}
	}
	if recheckFile == "" {
		return out, nil
	}
	r, err := readRecheck(recheckFile)
	if err != nil {
		return nil, err
	}
	if r.Failed > 0 {
		add(prtext.ReasonJudgeFailures)
	}
	if picked >= 0 && r.Judged+r.Retired+r.Failed < picked {
		add(prtext.ReasonPartial)
	}
	return out, nil
}

func readRecheck(p string) (*consistency.RecheckResult, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("recheck: %w", err)
	}
	var r consistency.RecheckResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("recheck: %w", err)
	}
	return &r, nil
}

func emitPRReview(ctx context.Context, rep *prreview.Report, commentOut, ghRepo string, post int, stderr io.Writer) error {
	var id, body string
	if post > 0 {
		var err error
		if id, body, err = findComment(ctx, ghRepo, post, prreview.Marker); err != nil {
			return err
		}
		rep.Feedback = factorypr.Ticked(body)
	}
	md := rep.Markdown(factorypr.TickedKeys(rep.Feedback))
	if commentOut != "" {
		if err := os.WriteFile(commentOut, []byte(md), 0o600); err != nil {
			return fmt.Errorf("comment: %w", err)
		}
	}
	switch {
	case post == 0:
	case id != "" && rep.Unchanged(body):
		_, _ = fmt.Fprintln(stderr, "docsp pr-comment: the posted comment already says this: not updated")
	default:
		return postComment(ctx, ghRepo, post, id, md, rep.NoProblemsComment())
	}
	return nil
}
