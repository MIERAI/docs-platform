// Package python is the code-side extractor for Python sources. It is pattern based (no Python
// interpreter is needed): argparse and click flags and sub-commands, environment variables read
// through os.environ / os.getenv, and prometheus_client metric names.
package python

import (
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/extract/lineidx"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

// Name and Version of this extractor.
const (
	Name    = "python"
	Version = "0.1.0"
)

// Extractor is the Python extractor.
type Extractor struct{}

// Info implements plugin.Extractor.
func (Extractor) Info() plugin.Info {
	return plugin.Info{Name: Name, Version: Version, Side: plugin.SideCode}
}

// Accepts implements plugin.Extractor: non-test Python files.
func (Extractor) Accepts(p string) bool {
	b := path.Base(p)
	return strings.HasSuffix(p, ".py") && !strings.HasPrefix(b, "test_") && !strings.HasSuffix(b, "_test.py") &&
		!strings.Contains("/"+p, "/tests/")
}

var (
	strLit    = `\s*[rbuf]?(?:"([^"\n]+)"|'([^'\n]+)')\s*`
	reArg     = regexp.MustCompile(`\b(?:add_argument|option)\(((?:` + strLit + `,?)+)`)
	reStr     = regexp.MustCompile(strLit)
	reParser  = regexp.MustCompile(`\badd_parser\(` + strLit)
	reClick   = regexp.MustCompile(`@[\w.]*\.command\(` + strLit)
	reEnv     = regexp.MustCompile(`\b(?:os\.environ(?:\.get|\.setdefault|\.pop)?\s*[\[(]|os\.getenv\(|environ\.get\()` + strLit)
	reMetric  = regexp.MustCompile(`\b(?:Counter|Gauge|Histogram|Summary|Info|Enum)\(` + strLit + `,`)
	reProg    = regexp.MustCompile(`ArgumentParser\([^)]*\bprog\s*=` + strLit)
	reMain    = regexp.MustCompile(`(?m)^if __name__ == ['"]__main__['"]`)
	reEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// Extract implements plugin.Extractor.
func (Extractor) Extract(f plugin.File) (model.Result, error) {
	e := &ext{f: f, idx: lineidx.New(f.Content), src: string(f.Content)}
	e.program()
	e.flags()
	e.each(reParser, model.KindCLICommand, true, model.ConfHigh)
	e.each(reClick, model.KindCLICommand, true, model.ConfHigh)
	e.envs()
	e.each(reMetric, model.KindMetric, true, model.ConfHigh)
	return model.Result{Symbols: e.syms}, nil
}

type ext struct {
	f    plugin.File
	idx  lineidx.Index
	src  string
	prog string
	syms []model.Symbol
}

func (e *ext) add(k model.Kind, name string, off int, public bool, c model.Confidence) {
	prog := ""
	if k == model.KindCLIFlag || k == model.KindCLICommand || k == model.KindEnvVar {
		prog = e.prog
	}
	e.syms = append(e.syms, model.Symbol{
		Kind: k, Name: name, Program: prog, Path: e.f.Path, Line: e.idx.Line(off),
		Public: public, Confidence: c, Source: Name,
	})
}

// lit returns whichever of the two alternative quote groups matched.
func lit(m []string, i int) string { return m[i] + m[i+1] }

func (e *ext) program() {
	if m := reProg.FindStringSubmatchIndex(e.src); m != nil {
		e.prog = e.src[max(m[2], m[4]):max(m[3], m[5])]
	} else if reMain.MatchString(e.src) {
		b := path.Base(e.f.Path)
		e.prog = strings.TrimSuffix(b, ".py")
	}
	if e.prog != "" {
		e.add(model.KindProgram, e.prog, 0, true, model.ConfMedium)
	}
}

func (e *ext) flags() {
	for _, m := range reArg.FindAllStringSubmatchIndex(e.src, -1) {
		args := e.src[m[2]:m[3]]
		for _, s := range reStr.FindAllStringSubmatch(args, -1) {
			v := lit(s, 1)
			switch {
			case strings.HasPrefix(v, "--"):
				e.add(model.KindCLIFlag, v, m[0], true, model.ConfHigh)
			case len(v) == 2 && v[0] == '-':
				e.add(model.KindCLIFlag, v, m[0], false, model.ConfHigh)
			}
		}
	}
}

func (e *ext) each(re *regexp.Regexp, k model.Kind, public bool, c model.Confidence) {
	for _, m := range re.FindAllStringSubmatchIndex(e.src, -1) {
		v := e.src[max(m[2], m[4]):max(m[3], m[5])]
		e.add(k, v, m[0], public, c)
	}
}

func (e *ext) envs() {
	for _, m := range reEnv.FindAllStringSubmatchIndex(e.src, -1) {
		v := e.src[max(m[2], m[4]):max(m[3], m[5])]
		if reEnvName.MatchString(v) {
			e.add(model.KindEnvVar, v, m[0], true, model.ConfHigh)
		}
	}
}
