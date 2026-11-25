package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/backlog"
	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/findings"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
)

type evalFlags struct {
	set, instanceFile, task, split, model, provider, outDir string
	limit, concurrency                                      int
	dryRun                                                  bool
}

// cmdEvalModel scores the configured provider of each task on a frozen eval set, or with
// -dry-run estimates calls, tokens and cost without calling any model.
func cmdEvalModel(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("eval-model", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f evalFlags
	fs.StringVar(&f.set, "set", "", "frozen eval set (JSON Lines, built by `bench replay -eval-out`)")
	fs.StringVar(&f.instanceFile, "instance-config", "", "instance configuration: provider, model, budget, classification, switching thresholds")
	fs.StringVar(&f.task, "task", "judge", "task to evaluate: judge, draft or both")
	fs.StringVar(&f.split, "split", "", "only items of this split (dev or test); default every split")
	fs.StringVar(&f.model, "model", "", "evaluate this model id instead of the configured one (candidate; production switches only via the instance config)")
	fs.StringVar(&f.provider, "provider", "", "evaluate this provider kind instead of the configured one")
	fs.StringVar(&f.outDir, "out-dir", "", "write one eval/v1 JSON per task here (mode 0600) for comparison over time")
	fs.IntVar(&f.limit, "limit", 0, "evaluate at most this many items per task")
	fs.IntVar(&f.concurrency, "concurrency", 2, "items in flight")
	fs.BoolVar(&f.dryRun, "dry-run", false, "estimate calls, tokens and cost; call no model")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if f.set == "" {
		return exitUsage, errors.New("-set is required")
	}
	set, err := evalmodel.LoadSet(f.set)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	in, err := instance.Load(f.instanceFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	tasks := []string{f.task}
	if f.task == "both" {
		tasks = []string{evalmodel.TaskJudge, evalmodel.TaskDraft}
	}
	code := exitOK
	for _, task := range tasks {
		res, err := evalTask(ctx, set, in, task, f)
		if err != nil {
			return exitUsage, err
		}
		if err := reportEval(res, f.outDir, stdout); err != nil {
			return exitUsage, err
		}
		if res.Switch != nil && !res.Switch.Qualifies {
			code = exitFindings
		}
	}
	return code, nil
}

func evalTask(ctx context.Context, set *evalmodel.Set, in *instance.Instance, task string, f evalFlags) (*evalmodel.Result, error) {
	t := in.AI.Judge
	switch task {
	case evalmodel.TaskJudge:
	case evalmodel.TaskDraft:
		t = in.AI.Draft
	default:
		return nil, fmt.Errorf("-task %q: want judge, draft or both", task)
	}
	if f.provider != "" {
		t.Provider = f.provider
	}
	if f.model != "" {
		t.Model = f.model
	}
	if err := (&instance.Instance{Version: 1, AI: instance.AI{Judge: t}}).Validate(); err != nil {
		return nil, fmt.Errorf("%s task: %w", task, err)
	}
	p, err := judge.New(t)
	if err != nil {
		return nil, fmt.Errorf("%s provider: %w", task, err)
	}
	if p == nil {
		return nil, fmt.Errorf("%s task: provider is none; configure ai.%s in the instance configuration or pass -provider/-model", task, task)
	}
	g, err := judge.NewGuard(task, p, in)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	defer func() { _ = g.Close() }()
	return evalmodel.Run(ctx, evalmodel.Options{
		Set: set, Task: task, Guard: g, DryRun: f.dryRun, Split: f.split, Limit: f.limit,
		Concurrency: f.concurrency, Thresholds: in.Eval,
	}), nil
}

var reUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func reportEval(res *evalmodel.Result, outDir string, w io.Writer) error {
	s := res.Scores
	mode := "run"
	if res.DryRun {
		mode = "dry run (estimate)"
	}
	_, err := fmt.Fprintf(w, "eval-model %s: task %s, %s:%s, %d items, %d answered, %d errors, %d refused, %d not run (budget)\n"+
		"  tokens in %d / out %d, cost $%.4f ($%.5f per item), p50 latency %d ms\n",
		mode, res.Task, res.Provider, res.Model, s.Items, s.Answered, s.Errors, s.Refused, s.BudgetCut,
		s.InputTokens, s.OutputTokens, s.CostUSD, s.USDPerItem, s.P50Latency)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if !res.DryRun {
		writeScores(w, res)
	}
	if outDir == "" {
		return nil
	}
	name := fmt.Sprintf("eval-%s-%s-%s-%s.json", res.Task, res.Provider, reUnsafe.ReplaceAllString(res.Model, "_"),
		time.Now().UTC().Format("20060102T150405Z"))
	if res.DryRun {
		name = strings.Replace(name, "eval-", "estimate-", 1)
	}
	return writeJSON(res, filepath.Join(outDir, name), w)
}

func writeScores(w io.Writer, res *evalmodel.Result) {
	s := res.Scores
	r := func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return fmt.Sprintf("%.2f", *v)
	}
	if res.Task == evalmodel.TaskDraft {
		_, _ = fmt.Fprintf(w, "  drafted %d, supported (no invented identifiers/values) %s, similarity to the reference fix %s\n",
			s.Drafted, r(s.SupportedRate), r(s.MeanSimilarity))
	} else {
		_, _ = fmt.Fprintf(w, "  precision %s, recall %s, unknown %s, quote rejection %s (tp %d fp %d fn %d tn %d)\n",
			r(s.Precision), r(s.Recall), r(s.UnknownRate), r(s.QuoteRejectionRate), s.TruePositives, s.FalsePositives, s.FalseNegatives, s.TrueNegatives)
		for _, sl := range s.BySplit {
			_, _ = fmt.Fprintf(w, "  split %s: recall %s on %d positives, false-positive rate %s on %d negatives\n", sl.Split, r(sl.Recall), sl.Positives, r(sl.FPRate), sl.Negatives)
		}
	}
	_, _ = fmt.Fprintln(w, "  note: "+s.Note)
	if res.Switch != nil {
		verdict := "QUALIFIES to replace the current provider for this task"
		if !res.Switch.Qualifies {
			verdict = "does not qualify: " + strings.Join(res.Switch.Failed, "; ")
		}
		_, _ = fmt.Fprintln(w, "  switching rule: "+verdict)
	}
}

// cmdImportFindings turns a reviewed backlog (JSON Lines, ground-truth format) into a
// findings/v1 document so the ledger tracks the existing backlog.
func cmdImportFindings(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("import-findings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoName := fs.String("repo-name", "", "logical repository name used in finding assets (required)")
	repoFull := fs.String("repo", "", "only import items whose \"repo\" field is this (e.g. owner/name); default all")
	owner := fs.String("owner", "unassigned", "finding owner")
	instanceName := fs.String("instance", "", "instance name recorded in run.instance")
	out := fs.String("out", "", "write the findings JSON to this file (mode 0600) instead of stdout")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if fs.NArg() != 1 || *repoName == "" {
		return exitUsage, errors.New("usage: docsp import-findings -repo-name NAME [-repo owner/name] FILE.jsonl")
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return exitUsage, fmt.Errorf("read backlog: %w", err)
	}
	items, err := backlog.Parse(b)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	now := findings.Timestamp(time.Now())
	doc, skipped := backlog.Build(findings.Params{
		Repo: *repoName, Version: version, Started: now, Finished: now, Instance: *instanceName, Owner: *owner,
	}, *repoFull, items)
	if err := writeJSON(doc, *out, stdout); err != nil {
		return exitUsage, err
	}
	_, err = fmt.Fprintf(stderr, "docsp import-findings: %d findings, %d items of other repositories skipped\n", len(doc.Findings), skipped)
	return exitOK, err //nolint:wrapcheck // stderr write
}
