// Package shell is the code-side extractor for shell scripts: the script as a program, its
// flags (getopts specs and case-arm patterns) and the environment variables it takes as input
// (${VAR:-default} forms, and upper-case variables it reads but never assigns).
package shell

import (
	"bytes"
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/extract/lineidx"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

// Name and Version of this extractor.
const (
	Name    = "shell"
	Version = "0.1.0"
)

// Extractor is the shell-script extractor.
type Extractor struct{}

// Info implements plugin.Extractor.
func (Extractor) Info() plugin.Info {
	return plugin.Info{Name: Name, Version: Version, Side: plugin.SideCode}
}

// Accepts implements plugin.Extractor: *.sh/*.bash/*.zsh, and extension-less files (checked
// for a shell shebang in Extract).
func (Extractor) Accepts(p string) bool {
	switch path.Ext(p) {
	case ".sh", ".bash", ".zsh":
		return true
	case "":
		return !strings.HasPrefix(path.Base(p), ".") && !strings.Contains(p, ".git/")
	}
	return false
}

var (
	reShebang  = regexp.MustCompile(`^#!\s*\S*(?:/|env\s+)(?:ba|z|da)?sh\b`)
	reGetopts  = regexp.MustCompile(`\bgetopts\s+["']?:?([A-Za-z:]+)["']?`)
	reCaseArm  = regexp.MustCompile(`(?m)^\s*\(?((?:-{1,2}[A-Za-z0-9][\w-]*=?\*?\|?)+)\)`)
	reDefault  = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*):?[-?=]`)
	reVarRef   = regexp.MustCompile(`\$\{?([A-Z_][A-Z0-9_]*)\}?`)
	reAssign   = regexp.MustCompile(`(?m)(?:^|[;&|()\s]\s*|\b(?:export|local|readonly|declare(?:\s+-\w+)*)\s+)([A-Z_][A-Z0-9_]*)\+?=`)
	reRead     = regexp.MustCompile(`\b(?:read(?:\s+-\w+(?:\s+\S+)?)*|for)\s+([A-Z_][A-Z0-9_ ]*)`)
	reComment  = regexp.MustCompile(`(?m)(^|\s)#[^\n]*`)
	reFlagWord = regexp.MustCompile(`^-{1,2}[A-Za-z0-9][\w-]*$`)
)

// ambient are variables every shell or CI runner provides; they are not script inputs.
var ambient = map[string]bool{
	"HOME": true, "PATH": true, "PWD": true, "OLDPWD": true, "USER": true, "SHELL": true, "TMPDIR": true,
	"IFS": true, "RANDOM": true, "LINENO": true, "BASH_SOURCE": true, "BASH_REMATCH": true, "FUNCNAME": true,
	"HOSTNAME": true, "UID": true, "EUID": true, "PPID": true, "SECONDS": true, "OSTYPE": true, "TERM": true,
	"LANG": true, "LC_ALL": true, "CI": true, "OPTARG": true, "OPTIND": true, "PIPESTATUS": true, "REPLY": true,
	"BASH_VERSION": true, "EPOCHSECONDS": true, "DEBIAN_FRONTEND": true, "XDG_CONFIG_HOME": true,
}

// Extract implements plugin.Extractor.
func (Extractor) Extract(f plugin.File) (model.Result, error) {
	if path.Ext(f.Path) == "" && !reShebang.Match(firstLine(f.Content)) {
		return model.Result{}, nil
	}
	src := reComment.ReplaceAllStringFunc(string(f.Content), blank)
	e := &ext{f: f, idx: lineidx.New(f.Content), prog: strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))}
	e.add(model.KindProgram, e.prog, 0, false, model.ConfHigh)
	e.flags(src)
	e.envs(src)
	return model.Result{Symbols: e.syms}, nil
}

// blank keeps byte offsets (and therefore line numbers) stable while removing comments.
func blank(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		return ' '
	}, s)
}

func firstLine(b []byte) []byte {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i]
	}
	return b
}

type ext struct {
	f    plugin.File
	idx  lineidx.Index
	prog string
	syms []model.Symbol
}

func (e *ext) add(k model.Kind, name string, off int, public bool, c model.Confidence) {
	prog := ""
	if k != model.KindProgram {
		prog = e.prog
	}
	e.syms = append(e.syms, model.Symbol{
		Kind: k, Name: name, Program: prog, Path: e.f.Path, Line: e.idx.Line(off),
		Public: public, Confidence: c, Source: Name,
	})
}

func (e *ext) flags(src string) {
	for _, m := range reGetopts.FindAllStringSubmatchIndex(src, -1) {
		for _, r := range strings.ReplaceAll(src[m[2]:m[3]], ":", "") {
			e.add(model.KindCLIFlag, "-"+string(r), m[0], true, model.ConfMedium)
		}
	}
	for _, m := range reCaseArm.FindAllStringSubmatchIndex(src, -1) {
		for _, w := range strings.Split(src[m[2]:m[3]], "|") {
			w = strings.TrimSuffix(strings.TrimSuffix(w, "*"), "=")
			if reFlagWord.MatchString(w) {
				e.add(model.KindCLIFlag, w, m[0], true, model.ConfMedium)
			}
		}
	}
}

func (e *ext) envs(src string) {
	assigned := map[string]bool{}
	for _, m := range reAssign.FindAllStringSubmatch(src, -1) {
		assigned[m[1]] = true
	}
	for _, m := range reRead.FindAllStringSubmatch(src, -1) {
		for _, v := range strings.Fields(m[1]) {
			assigned[v] = true
		}
	}
	seen := map[string]bool{}
	for _, m := range reDefault.FindAllStringSubmatchIndex(src, -1) {
		e.env(src[m[2]:m[3]], m[0], model.ConfHigh, seen)
	}
	for _, m := range reVarRef.FindAllStringSubmatchIndex(src, -1) {
		if v := src[m[2]:m[3]]; !assigned[v] {
			e.env(v, m[0], model.ConfMedium, seen)
		}
	}
}

func (e *ext) env(v string, off int, c model.Confidence, seen map[string]bool) {
	if seen[v] || ambient[v] || strings.HasPrefix(v, "GITHUB_") || strings.HasPrefix(v, "RUNNER_") {
		return
	}
	seen[v] = true
	e.add(model.KindEnvVar, v, off, true, c)
}
