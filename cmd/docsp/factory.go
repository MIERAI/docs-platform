package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factoryrules"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// factoryRulesReport is the JSON output of factory-rules.
type factoryRulesReport struct {
	Plan   *factoryrules.Plan   `json:"plan"`
	Result *factoryrules.Result `json:"result,omitempty"`
	USD    float64              `json:"usd"`
}

// cmdFactoryRules puts the business rules of an API factory's escape layer into the ledger:
// each rule of an API with hand-written code is judged against the repository and pinned to
// that code (a change to it re-opens the rule). Rules already in the ledger with the same
// pinned files are left alone, so running it on every commit only pays for new rules.
func cmdFactoryRules(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("factory-rules", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	manifest := fs.String("factory-manifest", "", "the factory's API manifest (factory.api-manifest/v1, `factory export`) of the same commit (required)")
	dry := fs.Bool("dry-run", false, "print which rules would be judged; no model, ledger unchanged")
	cache := fs.String("drift-cache", agentic.DefaultCacheDir(), "drift lists cached by HEAD SHA (\"\" = none)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *manifest == "" {
		return exitUsage, errors.New("-factory-manifest is required")
	}
	m, err := factorymanifest.Load(*manifest)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	l, in, _, root, name, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	b, err := ledger.NewBuilder(ctx, root, name, c.ref)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	plan, err := factoryrules.MakePlan(ctx, m, l, name, b)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	rep := &factoryRulesReport{Plan: plan}
	text := func(w io.Writer) {
		_, _ = fmt.Fprintf(w, "docsp factory-rules: %d rules with hand-written code, %d to judge\n", len(plan.Items), plan.Judge)
		for _, it := range plan.Items {
			if it.Reason != factoryrules.ReasonCurrent {
				_, _ = fmt.Fprintf(w, "  %-12s %s:%d (%s) -> %v\n", it.Reason, it.Path, it.Line, it.API, it.Files)
			}
		}
		if r := rep.Result; r != nil {
			_, _ = fmt.Fprintf(w, "judged %d, failed %d, unchanged %d; %v; $%.4f\n", r.Judged, r.Failed, r.Current, r.ByCat, rep.USD)
		}
	}
	if *dry || plan.Judge == 0 {
		return exitOK, c.emitReport(rep, text, stdout, stderr)
	}
	if c.ref != "HEAD" {
		return exitUsage, errors.New("judging needs -ref HEAD: the judge reads the checked-out tree")
	}
	if err := judgeRules(ctx, &c, in, root, name, *cache, b, l, rep); err != nil {
		return exitUsage, err
	}
	return exitOK, c.emitReport(rep, text, stdout, stderr)
}

// judgeRules judges the plan's rules with the instance's judge and saves the ledger.
func judgeRules(ctx context.Context, c *consFlags, in *instance.Instance, root, name, cache string, b *ledger.Builder, l *ledger.Ledger, rep *factoryRulesReport) error {
	g, err := check.NewJudge(c.instanceFile, "judge", false)
	if err != nil || g == nil {
		return errors.Join(errors.New("factory-rules needs ai.judge in the instance configuration"), err)
	}
	defer func() { _ = g.Close() }()
	run := agentic.New(agentic.Options{
		Guard: g, Repo: name, Dir: root, Class: in.Classification, CacheDir: cache,
		Settings: agentic.FromInstance(in.AI.Judge.Agentic), Hint: rep.Plan.Hint(),
	})
	b.Class, b.Model = in.Classification, g.Name()
	if d, err := run.Drift(ctx); err == nil {
		b.Drift = d
	}
	res := factoryrules.Judge(ctx, run, b, l, rep.Plan)
	rep.Result, rep.USD = &res, g.Stats().CostUSD
	return l.Save(c.ledger) //nolint:wrapcheck // already descriptive
}
