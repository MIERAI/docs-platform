package semantic

import (
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

// target is a code location an identifier resolves to.
type target struct {
	Path string
	Line int
	Kind string // "file", "dir", "symbol", "fact"
}

// commonIdents are code-span words too generic to point at one place in the code.
var commonIdents = map[string]bool{
	"true": true, "false": true, "null": true, "none": true, "yes": true, "no": true, "on": true, "off": true,
	"main": true, "test": true, "tests": true, "go": true, "make": true, "id": true, "name": true, "type": true,
	"docs": true, "src": true, "cmd": true, "readme": true, "todo": true, "string": true, "int": true,
}

// resolve maps an identifier written in a document to code locations (never documentation).
func (c *checker) resolve(id string) []target {
	if hit, ok := c.resolved[id]; ok {
		return hit
	}
	out := c.resolveUncached(id)
	sort.Slice(out, func(i, j int) bool { return out[i].Path+out[i].Kind < out[j].Path+out[j].Kind })
	c.resolved[id] = out
	return out
}

func (c *checker) resolveUncached(id string) []target {
	id = strings.TrimSpace(id)
	if i := strings.IndexAny(id, "<{*"); i > 0 && strings.Contains(id[:i], "/") {
		id = strings.TrimSuffix(id[:i], "/") // "specs/<domain>/" → "specs"
	}
	if len(id) < 3 || commonIdents[strings.ToLower(id)] || strings.ContainsAny(id, "<>{}*…") {
		return nil
	}
	if strings.Contains(id, "/") || path.Ext(id) != "" && !strings.Contains(id, " ") {
		if t := c.resolvePath(id); len(t) > 0 {
			return t
		}
	}
	return c.resolveSymbol(symbolName(id))
}

// resolveSymbol maps a name to the code that defines it (layer-③ symbols, then config facts).
func (c *checker) resolveSymbol(name string) []target {
	var out []target
	for _, s := range c.ix.Symbols[name] {
		if s.Kind == model.KindAnchor || s.Kind == model.KindLink || c.ix.DocFiles[s.Path] {
			continue
		}
		out = append(out, target{Path: s.Path, Line: s.Line, Kind: "symbol"})
	}
	if len(out) == 0 && isScreaming(name) {
		for _, f := range c.ix.FactsNamed(name) {
			out = append(out, target{Path: f.Path, Line: f.Line, Kind: "fact"})
		}
	}
	return dedupTargets(out, 8)
}

// symbolName reduces "FOO=bar", "--flag=x", "factory perf" and "$FOO" to the defined name.
func symbolName(id string) string {
	id = strings.TrimPrefix(id, "$")
	if k, _, ok := strings.Cut(id, "="); ok {
		id = k
	}
	if f := strings.Fields(id); len(f) == 2 && !strings.HasPrefix(f[1], "-") {
		return f[1] // "factory perf" → the sub-command perf
	}
	return id
}

func isScreaming(s string) bool {
	return len(s) >= 4 && strings.Contains(s, "_") && strings.ToUpper(s) == s
}

func (c *checker) resolvePath(id string) []target {
	p := strings.TrimPrefix(strings.TrimSuffix(id, "/"), "./")
	r := c.ix.Repo
	switch {
	case r.IsDir(p):
		return []target{{Path: p, Kind: "dir"}}
	case r.IsFile(p) && !c.ix.DocFiles[p]:
		return []target{{Path: p, Kind: "file"}}
	case !strings.Contains(p, "/"):
		var out []target
		for _, f := range r.ByBase(p) {
			if !c.ix.DocFiles[f] {
				out = append(out, target{Path: f, Kind: "file"})
			}
		}
		if len(out) <= 2 {
			return out
		}
	default:
		return c.suffixPath(p)
	}
	return nil
}

// suffixPath resolves "outbox/perf_gate_test.go" written relative to some sub-directory.
func (c *checker) suffixPath(p string) []target {
	var out []target
	for _, f := range c.ix.Repo.AllFiles() {
		if strings.HasSuffix(f, "/"+p) && !c.ix.DocFiles[f] {
			out = append(out, target{Path: f, Kind: "file"})
		}
	}
	if len(out) > 2 {
		return nil
	}
	return out
}

func dedupTargets(ts []target, limit int) []target {
	seen := map[string]bool{}
	var out []target
	for _, t := range ts {
		if seen[t.Path] {
			continue
		}
		seen[t.Path] = true
		out = append(out, t)
	}
	if len(out) > limit {
		return nil // too ambiguous to be evidence
	}
	return out
}

// codePaths returns the distinct code paths an identifier list resolves to.
func (c *checker) codePaths(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		for _, t := range c.resolve(id) {
			if !seen[t.Path] {
				seen[t.Path] = true
				out = append(out, t.Path)
			}
		}
	}
	sort.Strings(out)
	return out
}
