package main

import (
	"archive/tar"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// devRepos are the repositories whose ground truth may be looked at while designing (the dev
// split); every other repository is the held-out test split.
var devRepos = map[string]bool{"api-factory": true}

type replayOpts struct {
	gt, work, cfgDir, md, js, evalOut string
	pins                              pins
	maxCommits, parallel              int
}

// runReplay is `bench replay`: for every ground-truth item, find the commit(s) that made the
// documentation stale and check whether `docsp impact` (deterministic, no model) flagged the
// stale section at that moment.
func runReplay(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	o := replayOpts{pins: pins{}}
	fs.StringVar(&o.gt, "gt", "", "directory of ground-truth *.jsonl files")
	fs.StringVar(&o.work, "work", "", "scratch directory for read-only clones and temporary trees")
	fs.StringVar(&o.cfgDir, "config-dir", "", "optional directory of instance configs named <repo>.docsp.yaml")
	fs.StringVar(&o.md, "md", "", "write a Markdown report here")
	fs.StringVar(&o.js, "json", "", "write a JSON report here")
	fs.StringVar(&o.evalOut, "eval-out", "", "write the frozen model eval set (JSON Lines) here")
	fs.IntVar(&o.maxCommits, "max-commits", 25, "causing commits replayed per item (the first is always replayed)")
	fs.IntVar(&o.parallel, "parallel", 6, "commits replayed concurrently")
	fs.Var(o.pins, "pin", "check out a revision: name=rev (repeatable)")
	manifest := fs.String("manifest", "", "take the pins of a benchmark manifest (bench/data/manifest.json); -pin wins")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("replay flags: %w", err)
	}
	if o.gt == "" || o.work == "" {
		return errors.New("replay: -gt and -work are required")
	}
	if err := o.pins.addManifest(*manifest); err != nil {
		return err
	}
	gt, err := loadGT(o.gt)
	if err != nil {
		return err
	}
	rep := &ReplayReport{}
	var evalItems []EvalItem
	for _, name := range sortedKeys(gt) {
		rr, ev, err := replayRepo(ctx, name, gt[name], o)
		if err != nil {
			return err
		}
		rep.Repos = append(rep.Repos, rr)
		evalItems = append(evalItems, ev...)
	}
	rep.summarize()
	fmt.Print(rep.Markdown())
	if err := writeEvalSet(o.evalOut, evalItems); err != nil {
		return err
	}
	return rep.write(o.md, o.js)
}

// ReplayItem is the replay of one ground-truth item.
type ReplayItem struct {
	File        string          `json:"file"`
	Line        int             `json:"line"`
	Type        string          `json:"type"`
	Status      string          `json:"status"` // scored | no_causing_commit | no_doc_commit
	Why         string          `json:"why,omitempty"`
	DocCommit   string          `json:"doc_commit,omitempty"`
	Causing     []string        `json:"causing_commits,omitempty"`
	Replayed    int             `json:"replayed"`
	First       bool            `json:"flagged_at_first_causing_commit"`
	Any         bool            `json:"flagged_at_any_causing_commit"`
	FlaggedAt   string          `json:"flagged_at,omitempty"`
	Reasons     []impact.Reason `json:"reasons,omitempty"`
	LineMissing int             `json:"line_not_found_in,omitempty"`
}

// ReplayCommit is the cost side: how much one change asks people to re-read.
type ReplayCommit struct {
	SHA      string `json:"sha"`
	Reread   int    `json:"reread_sections"`
	Updated  int    `json:"updated_sections"`
	Sections int    `json:"sections_total"`
	Err      string `json:"error,omitempty"`
}

// ReplayRepo is one repository's replay.
type ReplayRepo struct {
	Repo     string         `json:"repo"`
	Split    string         `json:"split"`
	Revision string         `json:"revision"`
	Items    []ReplayItem   `json:"items"`
	Commits  []ReplayCommit `json:"commits"`
}

type commitEval struct {
	sections []impact.SectionResult
	file     map[string][]string // doc lines at the commit (for locating the GT line)
	summary  ReplayCommit
}

func replayRepo(ctx context.Context, name string, items []GTItem, o replayOpts) (ReplayRepo, []EvalItem, error) {
	if !reRepo.MatchString(name) {
		return ReplayRepo{}, nil, fmt.Errorf("bad repo name %q", name)
	}
	base := path.Base(name)
	dir := filepath.Join(o.work, base)
	rev, err := ensureClone(ctx, name, dir, o.pins[base])
	if err != nil {
		return ReplayRepo{}, nil, err
	}
	split := "test"
	if devRepos[base] {
		split = "dev"
	}
	rr := ReplayRepo{Repo: name, Split: split, Revision: rev}
	traces := make([]itemTrace, len(items))
	need := map[string]bool{}
	for i, it := range items {
		traces[i] = traceItem(ctx, dir, it)
		for k, c := range traces[i].causing {
			if k < o.maxCommits {
				need[c] = true
			}
		}
	}
	evals := evalCommits(ctx, dir, base, o, sortedKeys(need))
	for _, c := range sortedKeys(evals) {
		rr.Commits = append(rr.Commits, evals[c].summary)
	}
	var ev []EvalItem
	for i, it := range items {
		rr.Items = append(rr.Items, scoreItem(it, traces[i], evals, o.maxCommits))
		ev = append(ev, evalItemsFor(ctx, dir, base, split, i, it, traces[i])...)
	}
	return rr, ev, nil
}

// itemTrace is what git history says about one item.
type itemTrace struct {
	docCommit string
	lineText  string
	causing   []string
	why       string
}

// traceItem finds the commit that last wrote the doc line (blame at HEAD) and the later
// commits that touched the evidence paths (excluding the doc file itself).
func traceItem(ctx context.Context, dir string, it GTItem) itemTrace {
	var t itemTrace
	if it.Line != nil && *it.Line > 0 {
		out, err := gitOut(ctx, dir, "blame", "--porcelain", "-L", fmt.Sprintf("%d,%d", *it.Line, *it.Line), "HEAD", "--", it.File)
		if err != nil {
			t.why = "blame failed: " + firstLine(err.Error())
			return t
		}
		t.docCommit, t.lineText = parseBlame(out)
	} else {
		out, err := gitOut(ctx, dir, "log", "-1", "--format=%H", "HEAD", "--", it.File)
		if err != nil {
			t.why = "log failed"
			return t
		}
		t.docCommit = strings.TrimSpace(out)
	}
	if t.docCommit == "" || strings.HasPrefix(t.docCommit, "0000000") {
		t.why = "doc line has no committed author"
		return t
	}
	var ev []string
	for _, p := range it.EvidencePaths {
		if p != it.File && p != "" {
			ev = append(ev, p)
		}
	}
	if len(ev) == 0 {
		t.why = "no evidence path other than the doc itself"
		return t
	}
	out, err := gitOut(ctx, dir, append([]string{"log", "--format=%H", "--reverse", "--first-parent", t.docCommit + "..HEAD", "--"}, ev...)...)
	if err != nil {
		t.why = "log on evidence failed"
		return t
	}
	t.causing = strings.Fields(out)
	if len(t.causing) == 0 {
		t.why = "no commit touched the evidence paths after the doc line was written (stale since written, or evidence elsewhere)"
	}
	return t
}

func parseBlame(out string) (string, string) {
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		return "", ""
	}
	sha, _, _ := strings.Cut(lines[0], " ")
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "\t") {
			return sha, l[1:]
		}
	}
	return sha, ""
}

// evalCommits replays `docsp impact` (deterministic) on every needed commit, concurrently.
func evalCommits(ctx context.Context, dir, name string, o replayOpts, shas []string) map[string]*commitEval {
	out := map[string]*commitEval{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, o.parallel))
	for _, sha := range shas {
		wg.Add(1)
		sem <- struct{}{}
		go func(sha string) {
			defer func() { <-sem; wg.Done() }()
			ce := evalCommit(ctx, dir, name, o, sha)
			mu.Lock()
			out[sha] = ce
			mu.Unlock()
		}(sha)
	}
	wg.Wait()
	return out
}

func evalCommit(ctx context.Context, dir, name string, o replayOpts, sha string) *commitEval {
	ce := &commitEval{summary: ReplayCommit{SHA: sha[:12]}, file: map[string][]string{}}
	tree, err := os.MkdirTemp(o.work, "tree-")
	if err != nil {
		ce.summary.Err = err.Error()
		return ce
	}
	defer func() { _ = os.RemoveAll(tree) }()
	diffText, err := gitOut(ctx, dir, "diff", "--no-color", "--no-ext-diff", "--no-renames", "-U0", sha+"^", sha, "--")
	if err == nil {
		err = materialize(ctx, dir, sha, tree)
	}
	if err != nil {
		ce.summary.Err = firstLine(err.Error())
		return ce
	}
	res, err := check.DeriveImpact(ctx, check.ImpactOptions{
		Tree: tree, ConfigFile: instanceConfig(o.cfgDir, name), RepoName: name, Diffs: repo.ParseUnifiedDiff(diffText),
	})
	if err != nil {
		ce.summary.Err = firstLine(err.Error())
		return ce
	}
	ce.sections, ce.file, ce.summary.Sections = res.Sections, res.Lines, res.Total
	for _, s := range res.Sections {
		if s.Status == impact.SectionReread {
			ce.summary.Reread++
		} else {
			ce.summary.Updated++
		}
	}
	return ce
}

// materialize extracts the tree of a commit into dst (regular files only).
func materialize(ctx context.Context, dir, sha, dst string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "archive", "--format=tar", sha) //nolint:gosec // argv vector; sha from git log
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open tree: %w", err)
	}
	defer func() { _ = root.Close() }()
	terr := untar(root, out)
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	return terr
}

func untar(root *os.Root, r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("untar: %w", err)
		}
		if h.Typeflag != tar.TypeReg || h.Size > 8<<20 {
			continue
		}
		if err := root.MkdirAll(path.Dir(h.Name), 0o750); err != nil {
			return fmt.Errorf("untar: %w", err)
		}
		f, err := root.OpenFile(h.Name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("untar: %w", err)
		}
		_, cerr := io.Copy(f, io.LimitReader(tr, h.Size))
		if err := errors.Join(cerr, f.Close()); err != nil {
			return fmt.Errorf("untar: %w", err)
		}
	}
}

// scoreItem: was the section holding the stale line listed by impact at the causing commits?
func scoreItem(it GTItem, t itemTrace, evals map[string]*commitEval, maxCommits int) ReplayItem {
	ri := ReplayItem{File: it.File, Type: it.Type, DocCommit: short(t.docCommit), Why: t.why}
	if it.Line != nil {
		ri.Line = *it.Line
	}
	switch {
	case t.docCommit == "":
		ri.Status = "no_doc_commit"
		return ri
	case len(t.causing) == 0:
		ri.Status = "no_causing_commit"
		return ri
	}
	ri.Status = "scored"
	for k, c := range t.causing {
		ri.Causing = append(ri.Causing, short(c))
		ce := evals[c]
		if k >= maxCommits || ce == nil {
			continue
		}
		ri.Replayed++
		hit, reasons, found := flagged(ce, it.File, t.lineText, ri.Line)
		if !found {
			ri.LineMissing++
		}
		if hit && !ri.Any {
			ri.Any, ri.FlaggedAt, ri.Reasons = true, short(c), reasons
		}
		if hit && k == 0 {
			ri.First = true
		}
	}
	return ri
}

// flagged reports whether a listed section of file contains the stale line at that commit.
func flagged(ce *commitEval, file, lineText string, hint int) (bool, []impact.Reason, bool) {
	n := 0
	if lines, ok := ce.file[file]; ok && lineText != "" {
		n = locate(lines, lineText, hint)
	}
	for _, s := range ce.sections {
		if s.Path != file || s.Status != impact.SectionReread {
			continue
		}
		if lineText == "" || n > 0 && s.Contains(n) {
			return true, s.Reasons, true
		}
	}
	_, listed := ce.file[file]
	return false, nil, !listed || n > 0
}

// locate finds the line equal to text closest to hint (1-based; 0 = not found).
func locate(lines []string, text string, hint int) int {
	best, dist := 0, 1<<30
	want := strings.TrimSpace(text)
	for i, l := range lines {
		if strings.TrimSpace(l) != want {
			continue
		}
		if d := abs(i + 1 - hint); d < dist {
			best, dist = i+1, d
		}
	}
	return best
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-pager"}, args...)...) //nolint:gosec // argv vector; validated names
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return string(out), nil
}
