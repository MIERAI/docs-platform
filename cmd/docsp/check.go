package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/findings"
)

var severityRank = map[string]int{"info": 1, "low": 2, "medium": 3, "high": 4, "critical": 5}

type checkFlags struct {
	config, repoName, instance, out, format, minConf, failOn, denylist string
}

func cmdCheck(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f checkFlags
	fs.StringVar(&f.config, "config", "", "configuration file (default <repo>/.docsp.yaml)")
	fs.StringVar(&f.repoName, "repo-name", "", "logical repository name used in finding assets (default: directory name)")
	fs.StringVar(&f.instance, "instance", "", "instance name recorded in run.instance")
	fs.StringVar(&f.out, "out", "", "write the findings JSON to this file (mode 0600) instead of stdout")
	fs.StringVar(&f.format, "format", "json", "stdout format: json, text or none")
	fs.StringVar(&f.minConf, "min-confidence", "", "report findings at or above this confidence: low, medium, high (default from config: medium)")
	fs.StringVar(&f.failOn, "fail-on", "none", "exit 1 if a finding has this severity or higher: info, low, medium, high, none")
	fs.StringVar(&f.denylist, "denylist", os.Getenv("DOCSP_DENYLIST"), "private file (mode 0600) of terms that must not appear (env DOCSP_DENYLIST)")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if f.failOn != "none" && severityRank[f.failOn] == 0 {
		return exitUsage, fmt.Errorf("--fail-on %q: want info, low, medium, high or none", f.failOn)
	}
	res, err := check.Run(ctx, check.Options{
		Root: rootArg(fs), ConfigFile: f.config, RepoName: f.repoName, Instance: f.instance,
		Version: version, MinConfidence: f.minConf, DenylistFile: f.denylist,
	})
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if err := emit(res.Document, f, stdout, stderr); err != nil {
		return exitUsage, err
	}
	return failCode(res.Document, f.failOn), nil
}

// flagErr maps a flag parsing error to an exit code: -h is success, anything else is usage.
func flagErr(err error) (int, error) {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK, nil
	}
	return exitUsage, err
}

func rootArg(fs *flag.FlagSet) string {
	if fs.NArg() > 0 {
		return fs.Arg(0)
	}
	return "."
}

func emit(doc *findings.Document, f checkFlags, stdout, stderr io.Writer) error {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode findings: %w", err)
	}
	b = append(b, '\n')
	if f.out != "" {
		if err := os.WriteFile(f.out, b, 0o600); err != nil {
			return fmt.Errorf("write findings: %w", err)
		}
	}
	switch {
	case f.format == "json" && f.out == "":
		_, err = stdout.Write(b)
	case f.format == "text":
		err = summary(stdout, doc)
	case f.format == "none" || f.format == "json":
		err = nil
	default:
		return fmt.Errorf("--format %q: want json, text or none", f.format)
	}
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if f.format != "text" {
		return summaryLine(stderr, doc)
	}
	return nil
}

func failCode(doc *findings.Document, failOn string) int {
	if failOn == "none" {
		return exitOK
	}
	for _, f := range doc.Findings {
		if severityRank[f.Severity] >= severityRank[failOn] {
			return exitFindings
		}
	}
	return exitOK
}

func summaryLine(w io.Writer, doc *findings.Document) error {
	counts := map[string]int{}
	for _, f := range doc.Findings {
		if a, ok := f.Attributes["docs"].(findings.DocsAttributes); ok {
			counts[a.FindingType]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	_, err := fmt.Fprintf(w, "docsp: %d findings %s\n", len(doc.Findings), strings.Join(parts, " "))
	if err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

func summary(w io.Writer, doc *findings.Document) error {
	var b strings.Builder
	for _, f := range doc.Findings {
		fmt.Fprintf(&b, "%-6s %-45s %s\n       %s\n", f.Severity, f.Check, f.Title, f.Evidence)
	}
	for _, m := range doc.Metrics {
		if strings.HasSuffix(m.Name, "_ratio") {
			fmt.Fprintf(&b, "%s = %.3f\n", m.Name, m.Value)
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return summaryLine(w, doc)
}
