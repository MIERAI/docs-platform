package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/semantic"
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
	var af agenticFlags
	af.register(fs, false)
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	ag, err := auditAgentic(&af)
	if err != nil {
		return exitUsage, err
	}
	res, err := check.Audit(ctx, check.AuditOptions{
		Options: check.Options{
			Root: rootArg(fs), ConfigFile: f.config, RepoName: f.repoName, Instance: f.instance, Version: version,
		},
		InstanceFile: instanceFile, NoModel: noModel, MaxQuestions: maxQ, Agentic: ag,
	})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if err := emit(res.Document, f, stdout, stderr); err != nil {
		return exitUsage, err
	}
	st := res.Stats
	if err := auditSummary(stderr, res.Judge, &st); err != nil {
		return exitUsage, err
	}
	if ag != nil {
		_, err = fmt.Fprintf(stderr, "docsp audit (agentic): drift %d entries, %d validated (cached %v); categories %v; parse failures %d\n",
			st.DriftEntries, st.DriftValidated, st.DriftCached, st.AgenticCategories, st.ParseFailures)
		if err != nil {
			return exitUsage, fmt.Errorf("write summary: %w", err)
		}
	}
	return exitOK, nil
}

// auditAgentic turns the mode flags into the audit's agentic options (nil = snippet mode).
func auditAgentic(af *agenticFlags) (*check.AgenticAudit, error) {
	switch af.mode {
	case "snippet":
		return nil, nil
	case "agentic":
	default:
		return nil, fmt.Errorf("-mode %q: want snippet or agentic", af.mode)
	}
	if af.group != "" && af.group != "line" && af.group != "doc" {
		return nil, fmt.Errorf("-group %q: want line or doc", af.group)
	}
	cache := af.driftCache
	if cache == "" {
		cache = agentic.DefaultCacheDir()
	}
	if af.noCache {
		cache = ""
	}
	fp, err := ledger.ParseRules(af.footprint)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	return &check.AgenticAudit{Group: af.group, CacheDir: cache, NoDrift: af.noDrift, Ledger: af.ledger, Footprint: fp}, nil
}

// auditSummary writes the audit's summary lines (the judge's work and the rules stage's).
func auditSummary(w io.Writer, judgeName string, st *semantic.Stats) error {
	_, err := fmt.Fprintf(w, "docsp audit: judge %s, %d claims, %d questions, %d model requests (%d tokens, $%.4f), %d quotes rejected, %d refused (private), kept from the judge by audit.skip %v, %.1fs\n",
		judgeName, st.CheckableClaims, st.Questions, st.ModelRequests, st.ModelTokens, st.ModelUSD, st.QuotesRejected, st.Refused, st.Skipped, st.Seconds)
	if err == nil {
		_, err = fmt.Fprintf(w, "docsp audit (rules): %d lines decided without a model (%d confirmed, %d contradicted), %d questions saved, %d left to the judge\n",
			st.RulesDecided, st.RulesConfirmed, st.RulesContradicted, st.RulesSaved, st.Candidates)
	}
	if err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}
