package draftverify

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/sandbox"
)

// Where the sandbox sees the repository and the host-built binaries.
const (
	repoMount = "/repo"
	binMount  = "/opt/docsp/bin"
)

// buildTimeout bounds one host-side go build.
const buildTimeout = 5 * time.Minute

// example is one runnable fenced block the draft changes.
type example struct {
	line   int
	lang   string
	script string   // shell
	goSrc  string   // a runnable Go program
	needs  []string // repository binaries the script calls
	expect int
	why    string
	reason string // not runnable here (unknown language)
}

// examplesGate runs the runnable examples the draft changes in the sandbox: fenced shell
// blocks marked "runnable" (or "docsp:run"), shell blocks whose every command is a binary the
// repository builds (cmd/<name>), and Go blocks marked runnable. "norun" opts a block out,
// "exit=N" expects another exit code. Repository binaries and Go examples are compiled on
// the host (compiling runs no example code), everything runs in the sandbox only; without a
// sandbox the gate is skipped.
func examplesGate(ctx context.Context, in *Input, changed map[int]bool) Gate {
	g := Gate{Name: GateExamples}
	if docKind(in.Path) != "markdown" {
		g.Status, g.Detail = NotApplicable, "only Markdown documents carry fenced examples"
		return g
	}
	mod, bins := repoBinaries(in.Root)
	var exs []example
	for _, f := range fences(in.New) {
		if f.touches(changed) {
			if ex, ok := runnable(f, bins); ok {
				exs = append(exs, ex)
			}
		}
	}
	switch {
	case len(exs) == 0:
		g.Status, g.Detail = NotApplicable, "the draft changes no runnable example"
		return g
	case in.Sandbox == nil:
		g.Status = Skipped
		g.Detail = fmt.Sprintf("skipped: no sandbox (docker not available): %d changed example(s) not run; examples never run on the host", len(exs))
		return g
	}
	g.Items = runExamples(ctx, in, exs, mod, bins)
	g.Status = worst(g.Items, NotApplicable)
	g.Detail = fmt.Sprintf("%d changed example(s) run in the %s sandbox (no network, read-only root, non-root)", len(exs), in.Sandbox.Name())
	return g
}

var shellLangs = map[string]bool{"sh": true, "shell": true, "bash": true, "console": true, "shell-session": true}

// runnable decides whether a changed block is run, and how.
func runnable(f Fence, bins map[string]string) (example, bool) {
	if f.has("norun") || f.has("docsp:norun") {
		return example{}, false
	}
	marked := f.has("runnable") || f.has("docsp:run")
	ex := example{line: f.Line, lang: f.Lang, why: "marked runnable", expect: expectedExit(f)}
	src := strings.Join(f.Lines, "\n")
	switch {
	case shellLangs[f.Lang]:
		return shellExample(ex, src, bins, marked)
	case !marked:
		return example{}, false
	case f.Lang == "go" || f.Lang == "golang":
		ex.goSrc = src
	default:
		ex.reason = "no runner for " + f.Lang + " blocks"
	}
	return ex, true
}

// shellExample: a marked shell block, or one whose every command is a repository binary.
func shellExample(ex example, src string, bins map[string]string, marked bool) (example, bool) {
	ex.script = shellScript(ex.lang, src)
	cmds := commandWords(ex.script)
	auto := len(cmds) > 0
	for _, c := range cmds {
		if _, ok := bins[c]; ok {
			ex.needs = append(ex.needs, c)
		} else {
			auto = false
		}
	}
	switch {
	case marked:
		return ex, true
	case auto:
		ex.why = "every command is a binary the repository builds"
		return ex, true
	}
	return example{}, false
}

// expectedExit is the block's exit=N (default 0).
func expectedExit(f Fence) int {
	for _, w := range f.Info {
		if v, ok := strings.CutPrefix(w, "exit="); ok {
			if n, err := strconv.Atoi(v); err == nil {
				return n
			}
		}
	}
	return 0
}

// commandWords are the first words of a script's simple commands (comments, continuation
// lines and leading VAR=value assignments skipped).
func commandWords(script string) []string {
	var out []string
	cont := false
	for _, l := range strings.Split(script, "\n") {
		t := strings.TrimSpace(l)
		was := cont
		cont = strings.HasSuffix(t, "\\")
		if was || t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		for _, seg := range strings.FieldsFunc(t, func(r rune) bool { return r == '|' || r == ';' || r == '&' }) {
			for _, w := range strings.Fields(seg) {
				if !strings.Contains(w, "=") || strings.HasPrefix(w, "-") {
					out = append(out, w)
					break
				}
			}
		}
	}
	return out
}

// repoBinaries are the main packages of a Go repository: cmd/<name> → "./cmd/<name>". It
// also returns the module path ("" when there is no go.mod).
func repoBinaries(root string) (string, map[string]string) {
	bins := map[string]string{}
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", bins
	}
	var mod string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			mod = strings.Trim(strings.TrimSpace(v), `"`)
			break
		}
	}
	ents, _ := os.ReadDir(filepath.Join(root, "cmd"))
	for _, e := range ents {
		if e.IsDir() && isMain(filepath.Join(root, "cmd", e.Name())) {
			bins[e.Name()] = "./cmd/" + e.Name()
		}
	}
	return mod, bins
}

func isMain(dir string) bool {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && (bytes.Contains(b, []byte("\npackage main")) || bytes.HasPrefix(b, []byte("package main"))) {
			return true
		}
	}
	return false
}

// runExamples builds what the examples need on the host and runs each in the sandbox.
func runExamples(ctx context.Context, in *Input, exs []example, mod string, bins map[string]string) []Item {
	tmp, err := os.MkdirTemp("", "docsp-examples-")
	if err != nil {
		return []Item{{Kind: "example", Status: Error, Detail: err.Error()}}
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	bindir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bindir, 0o755); err != nil { //nolint:gosec // the sandbox's non-root user must read the binaries
		return []Item{{Kind: "example", Status: Error, Detail: err.Error()}}
	}
	_ = os.Chmod(tmp, 0o755) //nolint:gosec // as above
	root, err := filepath.Abs(in.Root)
	if err != nil {
		return []Item{{Kind: "example", Status: Error, Detail: err.Error()}}
	}
	staged := filepath.Join(tmp, "repo")
	if err := stageTree(ctx, root, staged, in.Path, in.New); err != nil {
		return []Item{{Kind: "example", Status: Error, Detail: err.Error()}}
	}
	built := map[string]error{}
	var out []Item
	for i, ex := range exs {
		it := Item{Line: ex.line, Kind: "example", Subject: ex.lang + " block (" + ex.why + ")"}
		script, err := prepare(ctx, ex, i, prepEnv{root: root, tmp: tmp, bindir: bindir, mod: mod, bins: bins, built: built, arch: in.Sandbox.Arch()})
		switch {
		case ex.reason != "":
			it.Status, it.Detail = Skipped, ex.reason
		case err != nil:
			it.Status, it.Detail = Error, "build: "+err.Error()
		default:
			it = runOne(ctx, in, it, script, ex.expect, staged, bindir)
		}
		out = append(out, it)
	}
	return out
}

type prepEnv struct {
	root, tmp, bindir, mod, arch string
	bins                         map[string]string
	built                        map[string]error
}

// prepare builds the binaries an example needs and returns the script run in the sandbox.
func prepare(ctx context.Context, ex example, i int, e prepEnv) (string, error) {
	if ex.reason != "" {
		return "", nil
	}
	if ex.goSrc != "" {
		name := "example-" + strconv.Itoa(i+1)
		if err := buildGoExample(ctx, e, ex.goSrc, name); err != nil {
			return "", err
		}
		return "cd " + repoMount + " || exit 1\n" + binMount + "/" + name + "\n", nil
	}
	needs := append([]string{}, ex.needs...)
	sort.Strings(needs)
	for _, n := range needs {
		err, done := e.built[n]
		if !done {
			err = goBuild(ctx, e.root, e.bins[n], filepath.Join(e.bindir, n), e.arch)
			e.built[n] = err
		}
		if err != nil {
			return "", err
		}
	}
	return "set -e\ncd " + repoMount + " || exit 1\n" + ex.script + "\n", nil
}

func buildGoExample(ctx context.Context, e prepEnv, src, name string) error {
	if !strings.Contains(src, "package main") || !strings.Contains(src, "func main()") {
		return errors.New("a runnable go block must be a complete main package")
	}
	dir := filepath.Join(e.tmp, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("example: %w", err)
	}
	gomod := "module docspexample\n\ngo 1.22\n"
	if e.mod != "" && strings.Contains(src, `"`+e.mod) {
		gomod += "\nrequire " + e.mod + " v0.0.0\n\nreplace " + e.mod + " => " + e.root + "\n"
		if sum, err := os.ReadFile(filepath.Join(e.root, "go.sum")); err == nil {
			if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o600); err != nil { //nolint:gosec // G703: dir is this run's own temporary directory
				return fmt.Errorf("example: %w", err)
			}
		}
	}
	for f, c := range map[string]string{"go.mod": gomod, "main.go": src} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o600); err != nil {
			return fmt.Errorf("example: %w", err)
		}
	}
	return goBuild(ctx, dir, ".", filepath.Join(e.bindir, name), e.arch)
}

// goBuild compiles a package for the sandbox (linux, static) without network access.
func goBuild(ctx context.Context, dir, pkg, out, arch string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return errors.New("no go toolchain on this host")
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gobin, "build", "-trimpath", "-o", out, pkg) //nolint:gosec // go toolchain from PATH, argv vector; compiling runs no example code
	cmd.Dir = dir
	cmd.Env = buildEnv(arch)
	var errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &errb, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build %s: %s", pkg, clip(errb.String(), 600))
	}
	if err := os.Chmod(out, 0o755); err != nil { //nolint:gosec // executable for the sandbox's non-root user
		return fmt.Errorf("go build: %w", err)
	}
	return nil
}

func buildEnv(arch string) []string {
	env := []string{
		"GOOS=linux", "GOARCH=" + arch, "CGO_ENABLED=0", "GOPROXY=off", "GOWORK=off", "GOFLAGS=-mod=mod",
		"GONOSUMDB=*", "GOSUMDB=off", "LC_ALL=C",
	}
	for _, k := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if v, ok := os.LookupEnv("GOTOOLCHAIN"); ok {
		return append(env, "GOTOOLCHAIN="+v)
	}
	return append(env, "GOTOOLCHAIN=local")
}

func runOne(ctx context.Context, in *Input, it Item, script string, expect int, root, bindir string) Item {
	r, err := in.Sandbox.Run(ctx, sandbox.Spec{
		Image:  in.Image,
		Mounts: []sandbox.Mount{{Host: root, Container: repoMount}, {Host: bindir, Container: binMount}},
		Env:    []string{"PATH=" + binMount + ":/usr/bin:/bin"},
		Script: script,
		Limits: in.Limits,
	})
	code := r.ExitCode
	it.ExitCode, it.Output = &code, clip(r.Output, 2000)
	switch {
	case err != nil:
		it.Status, it.Detail = Error, err.Error()
	case r.TimedOut:
		it.Status, it.Detail = Fail, "timed out"
	case r.ExitCode != expect:
		it.Status, it.Detail = Fail, fmt.Sprintf("exit code %d, want %d", r.ExitCode, expect)
	default:
		it.Status, it.Detail = Pass, "exit code "+strconv.Itoa(expect)
	}
	return it
}
