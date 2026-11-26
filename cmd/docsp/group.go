package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/group"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// groupFlags are the flags of `docsp group`.
type groupFlags struct {
	instanceFile, name, ledger, docSet, out, format, cache string
	checkouts                                              checkoutFlag
	lines                                                  lineFlag
}

// cmdGroup runs the continuous-consistency loop over a repository group:
// plan (resolve doc sets, targets and drift scans), judge (lines of a doc set against each
// declared target, into the ledger) and stale (invalidate the group's claims).
func cmdGroup(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	if len(args) == 0 || (args[0] != "plan" && args[0] != "judge" && args[0] != "stale") {
		return exitUsage, errors.New("usage: docsp group plan|judge|stale -instance-config FILE [-group NAME] [-checkout repo[@ref]=DIR ...]")
	}
	sub := args[0]
	fs := flag.NewFlagSet("group "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := groupFlags{checkouts: checkoutFlag{}}
	fs.StringVar(&f.instanceFile, "instance-config", "", "instance configuration with the group (required)")
	fs.StringVar(&f.name, "group", "", "group name (default: the only group)")
	fs.Var(f.checkouts, "checkout", "repo=DIR or repo@ref=DIR: a checkout (repeatable; a target needs one at its revision)")
	fs.StringVar(&f.ledger, "ledger", "", "claim ledger (judge, stale)")
	fs.StringVar(&f.docSet, "doc-set", "", "judge: the doc set")
	fs.Var(&f.lines, "line", "judge: PATH:LINE of the doc set to judge (repeatable)")
	fs.StringVar(&f.cache, "drift-cache", agentic.DefaultCacheDir(), "judge: drift lists cached by HEAD SHA (\"\" = none)")
	fs.StringVar(&f.out, "out", "", "write the JSON report here (mode 0600)")
	fs.StringVar(&f.format, "format", "text", "stdout format: text, json or none")
	if err := fs.Parse(args[1:]); err != nil {
		return flagErr(err)
	}
	in, err := instance.Load(f.instanceFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	g, err := group.Find(in, f.name)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	p := group.Resolve(ctx, g, group.Checkouts(f.checkouts))
	c := consFlags{out: f.out, format: f.format}
	switch sub {
	case "plan":
		return exitOK, c.emitReport(p, func(w io.Writer) { writePlan(w, p) }, stdout, stderr)
	case "stale":
		return groupStale(ctx, f, p, c, stdout, stderr)
	}
	return groupJudge(ctx, f, in, p, c, stdout, stderr)
}

func writePlan(w io.Writer, p *group.Plan) {
	_, _ = fmt.Fprintf(w, "docsp group plan: %s\n", p.Group)
	for _, d := range p.DocSets {
		var ts []string
		for _, t := range d.Targets {
			ts = append(ts, fmt.Sprintf("%s@%s (%.12s)", t.Repo, t.Ref, t.Commit))
		}
		_, _ = fmt.Fprintf(w, "  doc set %s (%s): %d documents, %d generated; documents %s\n", d.Name, d.Repo, len(d.Docs), len(d.Generated), strings.Join(ts, ", "))
		for _, gd := range d.Generated {
			_, _ = fmt.Fprintf(w, "    generated %s (%s) -> judge its input %q\n", gd.Path, gd.Why, gd.Input)
		}
	}
	for _, s := range p.DriftScans {
		_, _ = fmt.Fprintf(w, "  drift scan %s@%.12s (shared by every doc set documenting it)\n", s.Repo, s.Commit)
	}
	for _, x := range p.Problems {
		_, _ = fmt.Fprintln(w, "  problem: "+x)
	}
}

func groupStale(ctx context.Context, f groupFlags, p *group.Plan, c consFlags, stdout, stderr io.Writer) (int, error) {
	if f.ledger == "" {
		return exitUsage, errors.New("-ledger is required")
	}
	l, err := ledger.Load(f.ledger)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	r, err := ledger.CheckGroup(ctx, l, p.Group, p.Dirs(), time.Now())
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return exitOK, c.emitReport(r, func(w io.Writer) { writeStale(w, r) }, stdout, stderr)
}

func groupJudge(ctx context.Context, f groupFlags, in *instance.Instance, p *group.Plan, c consFlags, stdout, stderr io.Writer) (int, error) {
	if f.docSet == "" || len(f.lines) == 0 || f.ledger == "" {
		return exitUsage, errors.New("judge needs -doc-set, -line PATH:LINE and -ledger")
	}
	g, err := check.NewJudge(f.instanceFile, "judge", false)
	if err != nil || g == nil {
		return exitUsage, errors.Join(errors.New("judge needs ai.judge in the instance configuration"), err)
	}
	defer func() { _ = g.Close() }()
	l, err := ledger.Load(f.ledger)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	js, err := group.Judge(ctx, p, f.docSet, f.lines, group.JudgeOptions{Guard: g, Instance: in, CacheDir: f.cache, Ledger: l}, map[string]*agentic.Runner{})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if err := l.Save(f.ledger); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return exitOK, c.emitReport(js, func(w io.Writer) {
		for _, j := range js {
			cats := map[string]int{}
			for _, r := range j.Results {
				cats[r.Category]++
			}
			_, _ = fmt.Fprintf(w, "docsp group judge: %s against %s@%s (%.12s): %d lines, %d claims recorded; %v\n", f.docSet, j.Target.Repo, j.Target.Ref, j.Target.Commit, len(j.Results), j.Claims, cats)
		}
	}, stdout, stderr)
}

// checkoutFlag collects repo[@ref]=DIR pairs.
type checkoutFlag map[string]string

func (c checkoutFlag) String() string { return fmt.Sprint(map[string]string(c)) }

func (c checkoutFlag) Set(v string) error {
	k, dir, ok := strings.Cut(v, "=")
	if !ok || k == "" || dir == "" {
		return fmt.Errorf("want repo[@ref]=DIR, got %q", v)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("checkout %s: %w", k, err)
	}
	c[k] = abs
	return nil
}

// lineFlag collects PATH:LINE values.
type lineFlag []group.Line

func (l *lineFlag) String() string { return fmt.Sprint(*l) }

func (l *lineFlag) Set(v string) error {
	i := strings.LastIndex(v, ":")
	n, err := strconv.Atoi(v[i+1:])
	if i <= 0 || err != nil || n < 1 {
		return fmt.Errorf("want PATH:LINE, got %q", v)
	}
	*l = append(*l, group.Line{Path: v[:i], Line: n})
	return nil
}
