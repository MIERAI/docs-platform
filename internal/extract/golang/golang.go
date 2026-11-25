// Package golang is the code-side extractor for Go sources. It uses go/ast only (no type
// checking, no build), so it works on any checkout without dependencies being downloaded.
// It finds: programs (package main under cmd/<name>), cobra commands, flag/pflag/cobra flags,
// environment variables read via os.Getenv & friends, and Prometheus/OpenTelemetry metric names.
package golang

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

// Name and Version of this extractor.
const (
	Name    = "go"
	Version = "0.1.0"
)

// Extractor is the Go extractor.
type Extractor struct{}

// Info implements plugin.Extractor.
func (Extractor) Info() plugin.Info {
	return plugin.Info{Name: Name, Version: Version, Side: plugin.SideCode}
}

// Accepts implements plugin.Extractor: non-test Go files outside testdata.
func (Extractor) Accepts(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.Contains("/"+p, "/testdata/")
}

// Extract implements plugin.Extractor.
func (Extractor) Extract(f plugin.File) (model.Result, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.Path, f.Content, parser.SkipObjectResolution)
	if err != nil {
		return model.Result{}, fmt.Errorf("parse %s: %w", f.Path, err)
	}
	v := &visitor{fset: fset, path: f.Path, consts: stringConsts(file), test: strings.HasSuffix(f.Path, "_test.go")}
	if file.Name.Name == "main" && !v.test {
		v.program = programName(f.Path)
		if v.program != "" {
			v.add(model.KindProgram, v.program, file.Package, true, model.ConfHigh)
		}
	}
	ast.Inspect(file, v.visit)
	return model.Result{Symbols: v.syms}, nil
}

// programName derives a CLI name from a main package directory (cmd/<name>/... or <name>/main.go).
func programName(p string) string {
	dir := path.Dir(p)
	if dir == "." {
		return ""
	}
	return path.Base(dir)
}

type visitor struct {
	fset    *token.FileSet
	path    string
	program string
	consts  map[string]string
	syms    []model.Symbol
	// test files contribute only the environment variables they read (test and gate knobs).
	test bool
}

func (v *visitor) add(k model.Kind, name string, pos token.Pos, public bool, c model.Confidence) {
	if v.test && k != model.KindEnvVar {
		return
	}
	if v.test {
		c = min(c, model.ConfMedium)
	}
	prog := ""
	if k == model.KindCLIFlag || k == model.KindCLICommand || k == model.KindEnvVar {
		prog = v.program
	}
	v.syms = append(v.syms, model.Symbol{
		Kind: k, Name: name, Program: prog, Path: v.path, Line: v.fset.Position(pos).Line,
		Public: public, Confidence: c, Source: Name,
	})
}

func (v *visitor) visit(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.CallExpr:
		v.call(x)
	case *ast.CompositeLit:
		v.composite(x)
	case *ast.SwitchStmt:
		v.argSwitch(x)
	case *ast.BasicLit:
		if x.Kind == token.STRING && strings.HasPrefix(x.Value, `"# `) {
			v.exposition(x)
		}
	}
	return true
}

// str resolves a string literal or a same-file string constant.
func (v *visitor) str(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := v.consts[x.Name]
		return s, ok
	}
	return "", false
}

var reExposition = regexp.MustCompile(`^# (?:HELP|TYPE) ([a-zA-Z_:][a-zA-Z0-9_:]*)\b`)

// exposition recognises hand-written Prometheus text exposition ("# TYPE name gauge").
func (v *visitor) exposition(l *ast.BasicLit) {
	if s, ok := v.str(l); ok {
		if m := reExposition.FindStringSubmatch(s); m != nil {
			v.add(model.KindMetric, m[1], l.Pos(), true, model.ConfHigh)
		}
	}
}

func stringConsts(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			for _, s := range gd.Specs {
				addConsts(out, s)
			}
		}
	}
	return out
}

func addConsts(out map[string]string, s ast.Spec) {
	vs, ok := s.(*ast.ValueSpec)
	if !ok || len(vs.Names) != len(vs.Values) {
		return
	}
	for i, n := range vs.Names {
		lit, ok := vs.Values[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if v, err := strconv.Unquote(lit.Value); err == nil {
			out[n.Name] = v
		}
	}
}
