package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
)

type extractDoc struct {
	SchemaVersion string `json:"schema_version"`
	Extractor     struct {
		Name    string      `json:"name"`
		Version string      `json:"version"`
		Side    plugin.Side `json:"side"`
	} `json:"extractor"`
	model.Result
	Gaps []repo.Gap `json:"gaps,omitempty"`
}

func cmdExtract(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgFile := fs.String("config", "", "configuration file (default <repo>/.docsp.yaml)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	root := rootArg(fs)
	cfg, err := config.Load(root, *cfgFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	r, err := repo.Open(root, cfg.Exclude)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	inv := scan.Run(r, check.Builtins(cfg), scan.Options{Docs: cfg.Docs})
	d := extractDoc{SchemaVersion: "1.0", Result: inv.Result, Gaps: inv.Gaps}
	d.Extractor.Name, d.Extractor.Version, d.Extractor.Side = "builtin", version, plugin.SideCode
	d.Symbols, d.References = nonNil(d.Symbols), nonNilRefs(d.References)
	sort.Slice(d.Mentions, func(i, j int) bool { return d.Mentions[i].Token < d.Mentions[j].Token })
	return exitOK, writeJSON(d, "", stdout)
}

func nonNil(s []model.Symbol) []model.Symbol {
	if s == nil {
		return []model.Symbol{}
	}
	return s
}

func nonNilRefs(s []model.Reference) []model.Reference {
	if s == nil {
		return []model.Reference{}
	}
	return s
}

func cmdValidate(args []string, stdout io.Writer) (int, error) {
	if len(args) != 1 {
		return exitUsage, errors.New("usage: docsp validate FILE")
	}
	b, err := os.ReadFile(args[0]) //nolint:gosec // G703: the operator names the document to validate
	if err != nil {
		return exitUsage, fmt.Errorf("read: %w", err)
	}
	sch, err := jsonschema.Compile(contracts.Findings)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	errs := sch.ValidateJSON(b)
	for _, e := range errs {
		_, _ = fmt.Fprintln(stdout, e)
	}
	if len(errs) > 0 {
		return exitFindings, nil
	}
	_, err = fmt.Fprintln(stdout, "valid findings/v1")
	return exitOK, err //nolint:wrapcheck // stdout write
}

// cmdJudgeToolsMCP serves the read-only judge-tools of one agentic request over MCP (stdio).
// The core starts it for a CLI provider (claude-code); the spec file, written by the core
// with mode 0600, fixes the worktree, the classification policy, the limits and the
// transcript file. It is not meant to be run by hand.
func cmdJudgeToolsMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("judge-tools-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	spec := fs.String("spec", "", "tool spec written by docsp (required)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *spec == "" {
		return exitUsage, errors.New("-spec is required")
	}
	t, err := judge.OpenMCPSpec(*spec)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	defer func() { _ = t.Close() }()
	if err := judge.ServeMCP(ctx, stdin, stdout, t); err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	return exitOK, nil
}
