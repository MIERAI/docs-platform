// Package misc holds small pattern-based code-side extractors: Makefile targets, Terraform /
// OpenTofu variables, and environment variables read by JavaScript (Node process.env, k6 __ENV).
// Each is its own plugin with its own name and version.
package misc

import (
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/extract/lineidx"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

// Make extracts Makefile targets.
type Make struct{}

// Terraform extracts variable and output names from *.tf files.
type Terraform struct{}

// JavaScript extracts environment variables read by JS/TS sources.
type JavaScript struct{}

// Info implements plugin.Extractor.
func (Make) Info() plugin.Info {
	return plugin.Info{Name: "make", Version: "0.1.0", Side: plugin.SideCode}
}

// Info implements plugin.Extractor.
func (Terraform) Info() plugin.Info {
	return plugin.Info{Name: "terraform", Version: "0.1.0", Side: plugin.SideCode}
}

// Info implements plugin.Extractor.
func (JavaScript) Info() plugin.Info {
	return plugin.Info{Name: "javascript", Version: "0.1.0", Side: plugin.SideCode}
}

// Accepts implements plugin.Extractor.
func (Make) Accepts(p string) bool {
	b := path.Base(p)
	return b == "Makefile" || b == "GNUmakefile" || b == "makefile" || path.Ext(p) == ".mk"
}

// Accepts implements plugin.Extractor.
func (Terraform) Accepts(p string) bool { return path.Ext(p) == ".tf" }

// Accepts implements plugin.Extractor.
func (JavaScript) Accepts(p string) bool {
	switch path.Ext(p) {
	case ".js", ".mjs", ".cjs", ".ts":
		return !strings.HasSuffix(p, ".min.js") && !strings.Contains(p, ".test.")
	}
	return false
}

var (
	reTarget = regexp.MustCompile(`(?m)^([A-Za-z0-9][\w./-]*(?:[ \t]+[A-Za-z0-9][\w./-]*)*)[ \t]*::?([^=]|$)`)
	reTFVar  = regexp.MustCompile(`(?m)^\s*(variable|output)\s+"([\w-]+)"`)
	reJSEnv  = regexp.MustCompile(`\b(?:process\.env|__ENV)(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\])`)
)

func sym(f plugin.File, idx lineidx.Index, src string, k model.Kind, name string, off int, public bool) model.Symbol {
	prog := ""
	if k == model.KindMakeTarget {
		prog = "make"
	}
	return model.Symbol{
		Kind: k, Name: name, Program: prog, Path: f.Path, Line: idx.Line(off),
		Public: public, Confidence: model.ConfHigh, Source: src,
	}
}

// Extract implements plugin.Extractor.
func (Make) Extract(f plugin.File) (model.Result, error) {
	src, idx := string(f.Content), lineidx.New(f.Content)
	out := []model.Symbol{sym(f, idx, "make", model.KindProgram, "make", 0, false)}
	for _, m := range reTarget.FindAllStringSubmatchIndex(src, -1) {
		for _, t := range strings.Fields(src[m[2]:m[3]]) {
			out = append(out, sym(f, idx, "make", model.KindMakeTarget, t, m[0], false))
		}
	}
	return model.Result{Symbols: out}, nil
}

// Extract implements plugin.Extractor.
func (Terraform) Extract(f plugin.File) (model.Result, error) {
	src, idx := string(f.Content), lineidx.New(f.Content)
	var out []model.Symbol
	for _, m := range reTFVar.FindAllStringSubmatchIndex(src, -1) {
		public := src[m[2]:m[3]] == "variable"
		s := sym(f, idx, "terraform", model.KindConfigKey, src[m[4]:m[5]], m[0], public)
		s.Confidence = model.ConfMedium
		out = append(out, s)
	}
	return model.Result{Symbols: out}, nil
}

// Extract implements plugin.Extractor.
func (JavaScript) Extract(f plugin.File) (model.Result, error) {
	src, idx := string(f.Content), lineidx.New(f.Content)
	var out []model.Symbol
	seen := map[string]bool{}
	for _, m := range reJSEnv.FindAllStringSubmatchIndex(src, -1) {
		name := src[max(m[2], m[4]):max(m[3], m[5])]
		if !seen[name] {
			seen[name] = true
			out = append(out, sym(f, idx, "javascript", model.KindEnvVar, name, m[0], true))
		}
	}
	return model.Result{Symbols: out}, nil
}
