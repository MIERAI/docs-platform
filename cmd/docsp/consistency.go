package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/consistency"
	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/rulejudge"
)

// consFlags are the flags the continuous-consistency commands share.
type consFlags struct {
	ledger, instanceFile, state, ref, repoName, out, format string
}

func (c *consFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&c.ledger, "ledger", "", "claim ledger (docs-platform/ledger/v1; required)")
	fs.StringVar(&c.instanceFile, "instance-config", "", "instance configuration: consistency policy (and the judge, for recheck)")
	fs.StringVar(&c.state, "state", "", "consistency state file (budget decisions; mode 0600)")
	fs.StringVar(&c.ref, "ref", "HEAD", "revision the claims are checked against")
	fs.StringVar(&c.repoName, "repo-name", "", "logical repository name of the claims (default: directory name)")
	fs.StringVar(&c.out, "out", "", "write the JSON report here (mode 0600)")
	fs.StringVar(&c.format, "format", "text", "stdout format: text, json or none")
}

// load reads the ledger, instance and state and resolves the repository name.
func (c *consFlags) load(fs *flag.FlagSet) (*ledger.Ledger, *instance.Instance, *consistency.State, string, string, error) {
	if c.ledger == "" || fs.NArg() > 1 {
		return nil, nil, nil, "", "", errors.New("-ledger is required; at most one repository argument")
	}
	root := rootArg(fs)
	name := c.repoName
	if name == "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, nil, "", "", fmt.Errorf("repo: %w", err)
		}
		name = filepath.Base(abs)
	}
	l, err := ledger.Load(c.ledger)
	if err != nil {
		return nil, nil, nil, "", "", err //nolint:wrapcheck // already descriptive
	}
	in, err := instance.Load(c.instanceFile)
	if err != nil {
		return nil, nil, nil, "", "", err //nolint:wrapcheck // already descriptive
	}
	st := &consistency.State{Docs: map[string]consistency.DocState{}}
	if c.state != "" {
		if st, err = consistency.LoadState(c.state); err != nil {
			return nil, nil, nil, "", "", err //nolint:wrapcheck // already descriptive
		}
	}
	return l, in, st, root, name, nil
}

// emitReport writes a report as JSON (stdout or -out) and/or text.
func (c *consFlags) emitReport(v any, text func(io.Writer), stdout, stderr io.Writer) error {
	switch c.format {
	case "json":
		if c.out == "" {
			return writeJSON(v, "", stdout)
		}
	case "text":
		text(stdout)
	case "none":
	default:
		return fmt.Errorf("-format %q: want text, json or none", c.format)
	}
	if c.out != "" {
		return writeJSON(v, c.out, stderr)
	}
	return nil
}

// cmdSchedule is the cycle-count scheduler: which claims to judge again in this run.
func cmdSchedule(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	l, in, st, root, name, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	s, err := consistency.Plan(ctx, consistency.Input{Ledger: l, Repo: name, Dir: root, Ref: c.ref, Policy: in.Consistency, State: st, Now: time.Now()})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return exitOK, c.emitReport(s, func(w io.Writer) { writeSchedule(w, s) }, stdout, stderr)
}

func writeSchedule(w io.Writer, s *consistency.Schedule) {
	f := "n/a"
	if s.Freshness != nil {
		f = fmt.Sprintf("%.0f%%", 100**s.Freshness)
	}
	k := s.Counts
	_, _ = fmt.Fprintf(w, "docsp schedule: %s @%.12s: %d claims (freshness %s); stale %d, due %d, recheck %d, cycle quota %d; picked %d (est. $%.2f), shortfall %d\n",
		s.Repo, s.Ref, k.Claims, f, k.Stale, k.Due, k.Recheck, k.Quota, k.Picked, s.EstimatedUSD, k.Shortfall)
	for _, p := range s.Picked {
		_, _ = fmt.Fprintf(w, "pick   %s:%d [%s] %s\n", p.Path, p.Line, p.Class, p.Reason)
	}
	for _, p := range s.Shortfall {
		_, _ = fmt.Fprintf(w, "short  %s:%d [%s] %s (over budget)\n", p.Path, p.Line, p.Class, p.Reason)
	}
}

// cmdRecheck judges the claims of a schedule again (agentic mode) and updates the ledger.
func cmdRecheck(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("recheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	schedFile := fs.String("schedule", "", "schedule/v1 JSON from `docsp schedule -format json` (required)")
	cache := fs.String("drift-cache", agentic.DefaultCacheDir(), "drift lists cached by HEAD SHA (\"\" = none)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	l, in, st, root, name, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	s, err := readSchedule(*schedFile)
	if err != nil {
		return exitUsage, err
	}
	g, err := check.NewJudge(c.instanceFile, "judge", false)
	if err != nil || g == nil {
		return exitUsage, errors.Join(errors.New("recheck needs ai.judge in the instance configuration"), err)
	}
	defer func() { _ = g.Close() }()
	run := agentic.New(agentic.Options{Guard: g, Repo: name, Dir: root, Class: in.Classification, CacheDir: *cache, Settings: agentic.FromInstance(in.AI.Judge.Agentic)})
	if rules, err := rulejudge.LoadDir(root, nil); err == nil {
		run.SetRules(rules.Decider()) // deterministic first: lines a typed Go fact settles never reach the model
	}
	b, err := ledger.NewBuilder(ctx, root, name, "HEAD")
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	b.Class, b.Model = in.Classification, g.Name()
	if d, err := run.Drift(ctx); err == nil {
		b.Drift = d
	}
	res := consistency.Recheck(ctx, run, b, l, s.Picked)
	res.Reanchored = consistency.Reanchor(ctx, l, root, name, "HEAD")
	if err := l.Save(c.ledger); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if c.state != "" {
		consistency.Reaudited(st, fullyPicked(l, s, res.Done))
		if err := st.Save(c.state); err != nil {
			return exitUsage, err //nolint:wrapcheck // already descriptive
		}
	}
	return exitOK, c.emitReport(res, func(w io.Writer) {
		_, _ = fmt.Fprintf(w, "docsp recheck: %d judged, %d retired (line edited or document gone), %d failed, %d quotes re-anchored; %v; judge %s, $%.4f\n",
			res.Judged, res.Retired, res.Failed, res.Reanchored, res.ByCat, g.Name(), g.Stats().CostUSD)
	}, stdout, stderr)
}

func readSchedule(p string) (*consistency.Schedule, error) {
	if p == "" {
		return nil, errors.New("-schedule is required")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	var s consistency.Schedule
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return &s, nil
}

// fullyPicked keeps the documents whose every ledger claim was in the schedule.
func fullyPicked(l *ledger.Ledger, s *consistency.Schedule, done []string) []string {
	picked := map[string]bool{}
	for _, p := range s.Picked {
		picked[p.ID] = true
	}
	var out []string
	for _, d := range done {
		all := true
		for _, c := range l.Claims {
			if c.Path == d && c.Repo == s.Repo && !picked[c.ID] {
				all = false
			}
		}
		if all {
			out = append(out, d)
		}
	}
	return out
}

// cmdSample draws the accuracy sample (an eval set for a paid, operator-run re-audit).
func cmdSample(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("sample", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	n := fs.Int("n", 0, "sample size (default consistency.sample_size, 30)")
	seed := fs.Uint64("seed", uint64(time.Now().Unix()/(7*24*3600)), //nolint:gosec // Unix time is positive
		"random seed (default: the week number, so a week's sample is reproducible)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	l, in, _, root, name, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	if c.out == "" {
		return exitUsage, errors.New("-out FILE is required: the sample is an eval set")
	}
	size := *n
	if size <= 0 {
		size = in.Consistency.WithDefaults().SampleSize
	}
	items, err := consistency.Sample(ctx, l, root, name, c.ref, size, *seed, time.Now())
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if _, err := evalmodel.WriteSet(c.out, items); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	_, err = fmt.Fprintf(stdout, "docsp sample: %d claims of %s (seed %d) -> %s; re-audit with: docsp eval-model -set %s -mode agentic -worktree %s=%s ...\n",
		len(items), name, *seed, c.out, c.out, name, root)
	return exitOK, err //nolint:wrapcheck // stdout write
}

// cmdSLO reports the documentation-consistency SLIs, the error budget and its actions.
func cmdSLO(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("slo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c consFlags
	c.register(fs)
	sampleFile := fs.String("sample-result", "", "eval/v1 result of the accuracy sample (eval-model -out-dir)")
	apply := fs.Bool("apply", false, "record the budget's actions (full re-audit, shorter max age) in -state")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	l, in, st, root, name, err := c.load(fs)
	if err != nil {
		return exitUsage, err
	}
	now := time.Now()
	sched, err := consistency.Plan(ctx, consistency.Input{Ledger: l, Repo: name, Dir: root, Ref: c.ref, Policy: in.Consistency, State: st, Now: now})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	var sample *evalmodel.Result
	if *sampleFile != "" {
		if sample, err = readEvalResult(*sampleFile); err != nil {
			return exitUsage, err
		}
	}
	chk, err := check.Run(ctx, check.Options{Root: root, RepoName: name, Version: version})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	r := consistency.Report(consistency.SLOInput{
		Repo: name, Ref: sched.Ref, Policy: in.Consistency, Schedule: sched, Sample: sample,
		Check: chk.Document, Ledger: l, Now: now,
	})
	if *apply {
		if c.state == "" {
			return exitUsage, errors.New("-apply needs -state FILE")
		}
		consistency.Apply(st, r, now)
		if err := st.Save(c.state); err != nil {
			return exitUsage, err //nolint:wrapcheck // already descriptive
		}
	}
	return exitOK, c.emitReport(r, func(w io.Writer) { writeSLO(w, r) }, stdout, stderr)
}

func readEvalResult(p string) (*evalmodel.Result, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("sample result: %w", err)
	}
	var r evalmodel.Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("sample result: %w", err)
	}
	return &r, nil
}

func writeSLO(w io.Writer, r *consistency.SLOReport) {
	sli := func(name string, s consistency.SLI, higherBetter bool) {
		v := "not measured"
		if s.Value != nil {
			v = fmt.Sprintf("%.1f%%", 100**s.Value)
			if s.Low != nil {
				v += fmt.Sprintf(" (95%% CI %.0f-%.0f%%)", 100**s.Low, 100**s.High)
			}
		}
		op := "≥"
		if !higherBetter {
			op = "≤"
		}
		met := "MET"
		if !s.Met {
			met = "NOT MET"
		}
		if s.Value == nil {
			met = "-"
		}
		_, _ = fmt.Fprintf(w, "  %-10s %s, objective %s %.0f%%: %s (n %d, bad %d)\n", name, v, op, 100*s.Target, met, s.N, s.Bad)
	}
	_, _ = fmt.Fprintf(w, "docsp slo: %s @%.12s\n", r.Repo, r.Ref)
	sli("accuracy", r.Accuracy, true)
	sli("freshness", r.Freshness, true)
	sli("phantom", r.Phantom, false)
	if b := r.Budget; b != nil {
		_, _ = fmt.Fprintf(w, "  error budget: %.0f%% allowed, %.0f%% used, %.0f%% remaining%s\n", 100*b.Allowed, 100*b.Used, 100*b.Remaining, map[bool]string{true: " - BURNED", false: ""}[b.Burned])
	}
	for _, a := range r.Actions {
		_, _ = fmt.Fprintf(w, "  action: %s %s (max age %d days): %s\n", a.Action, a.Path, a.MaxAgeDays, a.Why)
	}
	_, _ = fmt.Fprintf(w, "  cost: accuracy sample $%.2f (%s); this run's schedule %d claims, est. $%.2f\n", r.Cost.SampleUSD, r.Cost.SampleBilling, r.Cost.ScheduledCount, r.Cost.ScheduledUSD)
}
