package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/MIERAI/docs-platform/internal/ledger"
)

// cmdStale checks the claim ledger against a newer revision, deterministically: a claim is
// stale when its line or any of its validated evidence changed; only those need a model.
func cmdStale(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("stale", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("ledger", "", "claim ledger (docs-platform/ledger/v1; required)")
	ref := fs.String("ref", "HEAD", "revision to check the claims against")
	repoName := fs.String("repo-name", "", "logical repository name of the claims (default: directory name)")
	out := fs.String("out", "", "write the stale/v1 JSON here (mode 0600) instead of stdout")
	format := fs.String("format", "text", "stdout format: text, json or none")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *file == "" || fs.NArg() > 1 {
		return exitUsage, errors.New("usage: docsp stale -ledger FILE [-ref REV] [-repo-name NAME] [repo]")
	}
	root := rootArg(fs)
	name := *repoName
	if name == "" {
		name = repoBase(root)
	}
	l, err := ledger.Load(*file)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	r, err := ledger.Check(ctx, l, root, name, *ref, time.Now())
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	switch *format {
	case "json":
		if *out == "" {
			return exitOK, writeJSON(r, "", stdout)
		}
	case "text":
		writeStale(stdout, r)
	case "none":
	default:
		return exitUsage, fmt.Errorf("-format %q: want text, json or none", *format)
	}
	if *out != "" {
		return exitOK, writeJSON(r, *out, stderr)
	}
	return exitOK, nil
}

// repoBase is the default logical name of a repository: its directory's name.
func repoBase(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Base(root)
	}
	return filepath.Base(abs)
}

func writeStale(w io.Writer, r *ledger.Report) {
	_, _ = fmt.Fprintf(w, "docsp stale: %s @%.12s: %d fresh, %d stale (judge again), %d recheck (no evidence)\n",
		r.Repo, r.Ref, r.Counts[ledger.Fresh], r.Counts[ledger.Stale], r.Counts[ledger.Recheck])
	for _, c := range r.Claims {
		if c.Status != ledger.Stale {
			continue
		}
		why := ""
		for i, rs := range c.Reasons {
			if i > 0 {
				why += "; "
			}
			why += rs.Kind
			if rs.Path != "" {
				why += " " + rs.Path
			}
		}
		_, _ = fmt.Fprintf(w, "stale  %s:%d [%s] %s\n", c.Path, c.Line, c.Category, why)
	}
}
