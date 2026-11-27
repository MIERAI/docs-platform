package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/related"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/semantic"
)

// cmdRelated lists the documentation sections a change is about (related/v1), without a
// ledger and without a model: the zero-config PR mode judges only these
// (`docsp audit -sections`).
func cmdRelated(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("related", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base revision of the change (required), e.g. origin/main")
	head := fs.String("head", "HEAD", "head revision of the change")
	cfgFile := fs.String("config", "", "configuration file (default <repo>/.docsp.yaml)")
	maxN := fs.Int("max-sections", related.DefaultMaxSections, "sections to select at most")
	out := fs.String("out", "", "write the related/v1 JSON here (mode 0600)")
	format := fs.String("format", "text", "stdout format: text, json or none")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *base == "" {
		return exitUsage, errors.New("-base is required")
	}
	if *format != "text" && *format != "json" && *format != "none" {
		return exitUsage, fmt.Errorf("-format %q: want text, json or none", *format)
	}
	root := rootArg(fs)
	rep, err := relatedSections(ctx, root, *cfgFile, *base, *head, *maxN)
	if err != nil {
		return exitUsage, err
	}
	if *out != "" {
		if err := writeJSON(rep, *out, stdout); err != nil {
			return exitUsage, err
		}
	}
	switch *format {
	case "json":
		if err := writeJSON(rep, "", stdout); err != nil {
			return exitUsage, err
		}
	case "text":
		writeRelated(stdout, rep)
	}
	return exitOK, nil
}

func relatedSections(ctx context.Context, root, cfgFile, base, head string, maxN int) (*related.Report, error) {
	diffs, err := repo.DiffHunks(ctx, root, base, head)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	r, inv, err := check.ScanTree(root, cfgFile)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	docs := map[string][]string{}
	for _, d := range inv.DocFiles {
		if !semantic.Prose(d) {
			continue
		}
		b, err := r.Read(d)
		if err != nil {
			continue
		}
		docs[d] = strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	}
	secs, _ := check.Sections(r, inv)
	refs := impact.Derive(impact.Attach(secs, inv.References), diffs)
	rep := related.Select(related.Input{Docs: docs, Diffs: diffs, References: refs, Max: maxN})
	rep.Base, rep.Head = base, head
	return rep, nil
}

func writeRelated(w io.Writer, rep *related.Report) {
	_, _ = fmt.Fprintf(w, "docsp related: %d of %d sections in %d docs selected (%d more left out by -max-sections)\n",
		len(rep.Sections), rep.Considered, rep.Docs, rep.Dropped)
	for _, s := range rep.Sections {
		_, _ = fmt.Fprintf(w, "  %s:%d-%d %q (%s", s.Path, s.From, s.To, s.Heading, s.Reason)
		if len(s.Terms) > 0 {
			_, _ = fmt.Fprintf(w, ": %s", strings.Join(s.Terms, ", "))
		}
		_, _ = fmt.Fprintln(w, ")")
	}
}
