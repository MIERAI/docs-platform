package main

import (
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
