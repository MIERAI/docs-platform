package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/MIERAI/docs-platform/internal/check"
)

// cmdAudit is layer ④(c): the semantic audit. Its findings are proposals for a human
// (needs_human, severity at most low); it never fails a build on findings.
func cmdAudit(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f checkFlags
	var instanceFile string
	var noModel bool
	var maxQ int
	fs.StringVar(&f.config, "config", "", "configuration file (default <repo>/.docsp.yaml)")
	fs.StringVar(&f.repoName, "repo-name", "", "logical repository name used in finding assets (default: directory name)")
	fs.StringVar(&f.instance, "instance", "", "instance name recorded in run.instance")
	fs.StringVar(&f.out, "out", "", "write the findings JSON to this file (mode 0600) instead of stdout")
	fs.StringVar(&f.format, "format", "json", "stdout format: json, text or none")
	fs.StringVar(&instanceFile, "instance-config", "", "instance configuration (docs-platform/instance/v1): judge provider, model, budget, data classification")
	fs.BoolVar(&noModel, "no-model", false, "deterministic checks only, whatever the instance configuration says")
	fs.IntVar(&maxQ, "max-questions", 0, "claims put to the judge (default 150)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	res, err := check.Audit(ctx, check.AuditOptions{
		Options: check.Options{
			Root: rootArg(fs), ConfigFile: f.config, RepoName: f.repoName, Instance: f.instance, Version: version,
		},
		InstanceFile: instanceFile, NoModel: noModel, MaxQuestions: maxQ,
	})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if err := emit(res.Document, f, stdout, stderr); err != nil {
		return exitUsage, err
	}
	st := res.Stats
	_, err = fmt.Fprintf(stderr, "docsp audit: judge %s, %d claims, %d questions, %d model requests (%d tokens, $%.4f), %d quotes rejected, %d refused (private), %.1fs\n",
		res.Judge, st.CheckableClaims, st.Questions, st.ModelRequests, st.ModelTokens, st.ModelUSD, st.QuotesRejected, st.Refused, st.Seconds)
	if err != nil {
		return exitUsage, fmt.Errorf("write summary: %w", err)
	}
	return exitOK, nil
}
