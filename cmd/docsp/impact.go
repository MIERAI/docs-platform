package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/repo"
)

func cmdImpact(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("impact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", "", "base revision of the change (required), e.g. origin/main")
	head := fs.String("head", "HEAD", "head revision of the change")
	cfgFile := fs.String("config", "", "configuration file (default <repo>/.docsp.yaml)")
	bodyFile := fs.String("pr-body-file", "", "file holding the PR description (searched for the waiver trailer)")
	out := fs.String("out", "", "write the impact JSON to this file (mode 0600) instead of stdout")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *base == "" {
		return exitUsage, errors.New("--base is required")
	}
	root := rootArg(fs)
	cfg, err := config.Load(root, *cfgFile)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	in, err := impactInput(ctx, root, *base, *head, *bodyFile)
	if err != nil {
		return exitUsage, err
	}
	in.Cfg, in.Version = cfg.Impact, version
	rep := impact.Evaluate(in)
	if err := writeJSON(rep, *out, stdout); err != nil {
		return exitUsage, err
	}
	impactSummary(stderr, rep)
	if rep.Verdict == impact.VerdictFail {
		return exitFindings, nil
	}
	return exitOK, nil
}

func impactInput(ctx context.Context, root, base, head, bodyFile string) (impact.Input, error) {
	changed, err := repo.ChangedFiles(ctx, root, base, head)
	if err != nil {
		return impact.Input{}, err //nolint:wrapcheck // already descriptive
	}
	msgs, err := repo.CommitMessages(ctx, root, base, head)
	if err != nil {
		return impact.Input{}, err //nolint:wrapcheck // already descriptive
	}
	texts := strings.Split(msgs, "\x00")
	if bodyFile != "" {
		b, err := os.ReadFile(bodyFile)
		if err != nil {
			return impact.Input{}, fmt.Errorf("read PR body: %w", err)
		}
		texts = append(texts, string(b))
	}
	return impact.Input{Base: base, Head: head, Changed: changed, Texts: texts}, nil
}

func writeJSON(v any, out string, stdout io.Writer) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	b = append(b, '\n')
	if out != "" {
		if err := os.WriteFile(out, b, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		return nil
	}
	if _, err := stdout.Write(b); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func impactSummary(w io.Writer, rep *impact.Report) {
	for _, r := range rep.Rules {
		if r.Status == impact.MissingDocs || r.Status == impact.Waived {
			_, _ = fmt.Fprintf(w, "docsp impact: rule %q: code changed (%s) but none of %s\n",
				r.Name, strings.Join(r.CodeChanged, ", "), strings.Join(r.DocsExpected, ", "))
		}
	}
	msg := "docsp impact: " + rep.Verdict
	if rep.WaiverReason != "" {
		msg += " (waiver: " + rep.WaiverReason + ")"
	}
	_, _ = fmt.Fprintln(w, msg)
}
