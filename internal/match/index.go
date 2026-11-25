// Package match is layer ③ of docs-platform: it resolves every documentation reference against
// what the code defines (doc → code: "wrong or obsolete"), and every public code symbol against
// what the documentation mentions (code → doc: "undocumented").
package match

import (
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
)

// index is the symbol table built from an inventory.
type index struct {
	byKind      map[model.Kind]map[string][]model.Symbol
	programs    map[string]bool
	flagsByProg map[string]map[string]bool
	cmdsByProg  map[string]map[string]bool
	metricNS    map[string]bool
	configPaths []string
	codeTokens  map[string]bool
	repo        *repo.Repo
	gitignore   []string
	related     []string
	hasMake     bool
	hasFlags    bool
}

func newIndex(inv *scan.Inventory, r *repo.Repo, gitignore []string) *index {
	ix := &index{
		byKind:      map[model.Kind]map[string][]model.Symbol{},
		programs:    map[string]bool{},
		flagsByProg: map[string]map[string]bool{},
		cmdsByProg:  map[string]map[string]bool{},
		metricNS:    map[string]bool{},
		codeTokens:  inv.CodeTokens,
		repo:        r,
		gitignore:   gitignore,
	}
	for _, s := range inv.Symbols {
		ix.add(s)
	}
	return ix
}

func (ix *index) add(s model.Symbol) {
	key := norm(s.Kind, s.Name)
	m := ix.byKind[s.Kind]
	if m == nil {
		m = map[string][]model.Symbol{}
		ix.byKind[s.Kind] = m
	}
	m[key] = append(m[key], s)
	switch s.Kind {
	case model.KindProgram:
		ix.programs[s.Name] = true
		ix.hasMake = ix.hasMake || s.Name == "make"
	case model.KindCLIFlag:
		addTo(ix.flagsByProg, s.Program, key)
		ix.hasFlags = true
	case model.KindCLICommand:
		addTo(ix.cmdsByProg, s.Program, key)
	case model.KindMetric:
		if s.Public {
			ix.metricNS[metricNamespace(key)] = true
		}
	case model.KindConfigKey:
		ix.configPaths = append(ix.configPaths, s.Name)
	default:
	}
}

func addTo(m map[string]map[string]bool, prog, key string) {
	if m[prog] == nil {
		m[prog] = map[string]bool{}
	}
	m[prog][key] = true
}

// norm gives the comparison key of an identifier.
func norm(k model.Kind, s string) string {
	switch k {
	case model.KindCLIFlag:
		if strings.HasPrefix(s, "--") || len(s) > 2 {
			return strings.TrimLeft(s, "-")
		}
		return s
	case model.KindMetric:
		return metricBase(s)
	case model.KindAnchor:
		return strings.ToLower(s)
	default:
		return s
	}
}

var metricSuffixes = []string{"_total", "_bucket", "_sum", "_count", "_created"}

// metricBase strips the suffixes Prometheus clients and exporters append to a family name.
func metricBase(s string) string {
	for _, suf := range metricSuffixes {
		if strings.HasSuffix(s, suf) && len(s) > len(suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

func metricNamespace(s string) string {
	ns, _, _ := strings.Cut(s, "_")
	return ns
}

func (ix *index) has(k model.Kind, name string) bool {
	return len(ix.byKind[k][norm(k, name)]) > 0
}

// inCode reports whether a token appears anywhere in code (the resolution fallback).
func (ix *index) inCode(tokens ...string) bool {
	for _, t := range tokens {
		if ix.codeTokens[t] {
			return true
		}
	}
	return false
}

// nearest suggests the closest defined name of the same kind ("did you mean").
func (ix *index) nearest(k model.Kind, name string) string {
	if k == model.KindConfigKey || k == model.KindFilePath {
		return ""
	}
	key := norm(k, name)
	best, bestD := "", 4
	for cand, syms := range ix.byKind[k] {
		if d := levenshtein(key, cand, bestD); d < bestD && d*2 < len(key) {
			best, bestD = syms[0].Name, d
		} else if d == bestD && best != "" && syms[0].Name < best {
			best = syms[0].Name
		}
	}
	return best
}

// levenshtein computes edit distance, giving up (returning limit) once it exceeds limit.
func levenshtein(a, b string, limit int) int {
	if d := len(a) - len(b); d > limit || -d > limit {
		return limit
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		rowMin := i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if rowMin >= limit {
			return limit
		}
		prev = cur
	}
	return prev[len(b)]
}
