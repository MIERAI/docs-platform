package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/onboard"
)

// cmdInit inspects a repository and writes a proposed .docsp.yaml (every guess commented with
// its reason). It is the one command that writes into the repository it inspects, and only
// that file, never over an existing one unless -force.
func cmdInit(_ context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print the proposal instead of writing it")
	out := fs.String("out", "", "write the proposal here (default <repo>/.docsp.yaml)")
	force := fs.Bool("force", false, "overwrite an existing configuration file")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	root := rootArg(fs)
	p, err := onboard.Inspect(root)
	if err != nil {
		return exitUsage, err //nolint:wrapcheck // already descriptive
	}
	if err := p.Validate(); err != nil {
		return exitUsage, fmt.Errorf("the proposal does not parse (a bug): %w", err)
	}
	y := p.YAML()
	if *dryRun {
		_, err := io.WriteString(stdout, y)
		return exitOK, err //nolint:wrapcheck // stdout
	}
	dest := *out
	if dest == "" {
		dest = filepath.Join(root, config.FileName)
	}
	if _, err := os.Stat(dest); err == nil && !*force {
		return exitUsage, fmt.Errorf("%s exists: not overwritten (-force replaces it, -dry-run prints the proposal)", dest)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return exitUsage, fmt.Errorf("stat %s: %w", dest, err)
	}
	if err := os.WriteFile(dest, []byte(y), 0o644); err != nil { //nolint:gosec // G306: a configuration file meant to be committed
		return exitUsage, fmt.Errorf("write %s: %w", dest, err)
	}
	_, _ = fmt.Fprintf(stdout, "docsp init: wrote %s (%d documentation files); review it, then run `docsp doctor`\n", dest, p.DocFiles)
	return exitOK, nil
}

// cmdDoctor checks offline that the first CI run will be green. Exit 0: no check failed
// (warnings allowed unless -strict); 1: a check failed; 2: usage error.
func cmdDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgFile := fs.String("config", "", "configuration file (default <repo>/.docsp.yaml)")
	instanceFile := fs.String("instance-config", "", "instance configuration to check (judge program, passed variables, budget)")
	tokenEnv := fs.String("token-env", onboard.DefaultTokenEnv, "name of the variable holding the model token in CI (only its presence is checked)")
	strict := fs.Bool("strict", false, "warnings fail too (exit 1)")
	format := fs.String("format", "text", "stdout format: text or json")
	if err := fs.Parse(args); err != nil {
		return flagErr(err)
	}
	if *format != "text" && *format != "json" {
		return exitUsage, fmt.Errorf("-format %q: want text or json", *format)
	}
	root := rootArg(fs)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return exitUsage, fmt.Errorf("%s is not a directory", root)
	}
	rep := onboard.Doctor(ctx, onboard.DoctorOptions{
		Root: root, ConfigFile: *cfgFile, InstanceFile: *instanceFile, TokenEnv: *tokenEnv,
	})
	if *format == "json" {
		if err := writeJSON(rep, "", stdout); err != nil {
			return exitUsage, err
		}
	} else {
		writeDoctor(stdout, rep)
	}
	if rep.Count(onboard.Fail) > 0 || *strict && rep.Count(onboard.Warn) > 0 {
		return exitFindings, nil
	}
	return exitOK, nil
}

func writeDoctor(w io.Writer, rep *onboard.Report) {
	for _, c := range rep.Checks {
		_, _ = fmt.Fprintf(w, "%-4s  %-11s  %s\n", c.Level, c.Name, c.Message)
	}
	_, _ = fmt.Fprintf(w, "docsp doctor: %d ok, %d info, %d warnings, %d failed\n",
		rep.Count(onboard.OK), rep.Count(onboard.Info), rep.Count(onboard.Warn), rep.Count(onboard.Fail))
}
