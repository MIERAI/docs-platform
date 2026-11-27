package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/prreview"
)

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
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *before == "" || *after == "" || (*post > 0 && *ghRepo == "") {
		return exitUsage, errors.New("-before and -after are required; -post-pr needs -github-repo")
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
	lb, err := ledger.NewBuilder(ctx, root, name, "HEAD")
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	head, err := ledger.Resolve(ctx, root, "HEAD")
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	rep := prreview.Compare(ctx, b, a, head, lb)
	rep.Repo, rep.GitHub = name, *ghRepo
	if err := emitPRReview(ctx, rep, *commentOut, *ghRepo, *post, stderr); err != nil {
		return exitUsage, err
	}
	if *out != "" {
		if err := writeJSON(rep, *out, stdout); err != nil {
			return exitUsage, err
		}
	}
	_, _ = fmt.Fprintf(stdout, "docsp pr-comment: %d claims judged at the head, %d new problems\n", rep.Rechecked, len(rep.Problems))
	return exitOK, nil
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
		return postComment(ctx, ghRepo, post, id, md, prreview.NoProblemsComment())
	}
	return nil
}
