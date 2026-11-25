// Command bench measures docsp against human-confirmed ground truth.
//
//	go run ./bench -gt DIR -work DIR [-pin name=rev ...] [-denylist FILE] [-md report.md] [-json report.json]
//
// For every repository named in the ground-truth JSON Lines it makes a read-only clone
// (https://github.com/<repo>) under -work, runs docsp check in-process at the lowest confidence,
// and reports recall and precision per finding type, per detector layer and per confidence
// threshold. Ground truth is not exhaustive, so precision is a lower bound; unmatched findings
// are listed for manual triage.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/check"
)

type pins map[string]string

func (p pins) String() string { return fmt.Sprint(map[string]string(p)) }

func (p pins) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || !reRev.MatchString(v) {
		return fmt.Errorf("want name=revision, got %q", s)
	}
	p[k] = v
	return nil
}

var (
	reRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	reRev  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,100}$`)
)

func main() {
	gt := flag.String("gt", "", "directory of ground-truth *.jsonl files")
	work := flag.String("work", "", "scratch directory for read-only clones")
	deny := flag.String("denylist", "", "optional private denylist file (mode 0600) for the residue check")
	cfgDir := flag.String("config-dir", "", "optional directory of instance configs named <repo>.docsp.yaml")
	md := flag.String("md", "", "write a Markdown report here")
	js := flag.String("json", "", "write a JSON report here")
	p := pins{}
	flag.Var(p, "pin", "check out a revision: name=rev (repeatable)")
	flag.Parse()
	if *gt == "" || *work == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(context.Background(), opts{gt: *gt, work: *work, deny: *deny, cfgDir: *cfgDir, md: *md, js: *js, pins: p}); err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

type opts struct {
	gt, work, deny, cfgDir, md, js string
	pins                           pins
}

func run(ctx context.Context, o opts) error {
	gt, err := loadGT(o.gt)
	if err != nil {
		return err
	}
	repos := make([]string, 0, len(gt))
	for r := range gt {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	var results []repoResult
	for _, r := range repos {
		res, err := benchRepo(ctx, r, gt[r], o)
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	rep := buildReport(results)
	fmt.Print(rep.Markdown())
	return rep.write(o.md, o.js)
}

func benchRepo(ctx context.Context, name string, items []GTItem, o opts) (repoResult, error) {
	if !reRepo.MatchString(name) {
		return repoResult{}, fmt.Errorf("bad repo name %q", name)
	}
	dir := filepath.Join(o.work, path.Base(name))
	rev, err := ensureClone(ctx, name, dir, o.pins[path.Base(name)])
	if err != nil {
		return repoResult{}, err
	}
	res, err := check.Run(ctx, check.Options{
		Root: dir, RepoName: path.Base(name), Version: "bench", MinConfidence: "low", DenylistFile: o.deny,
		ConfigFile: instanceConfig(o.cfgDir, path.Base(name)),
	})
	if err != nil {
		return repoResult{}, fmt.Errorf("check %s: %w", name, err)
	}
	out := repoResult{Repo: name, Revision: rev, GT: items}
	for _, f := range res.Document.Findings {
		out.Findings = append(out.Findings, view(f))
	}
	return out, nil
}

func instanceConfig(dir, name string) string {
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, name+".docsp.yaml")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// ensureClone clones (once) and optionally checks out a pinned revision. The clone lives in
// the bench scratch directory; the inspected repository itself is only ever read.
func ensureClone(ctx context.Context, name, dir, rev string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := git(ctx, "", "clone", "--quiet", "https://github.com/"+name+".git", dir); err != nil {
			return "", err
		}
	}
	if rev != "" {
		if err := git(ctx, dir, "checkout", "--quiet", "--detach", rev); err != nil {
			return "", err
		}
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--short", "HEAD").Output() //nolint:gosec // argv vector, validated dir
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func git(ctx context.Context, dir string, args ...string) error {
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // argv vector; repo names and revisions validated
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
