// Package extracttest has helpers shared by extractor tests.
package extracttest

import (
	"fmt"
	"sort"
	"testing"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

// Symbols runs e on one file and renders symbols as "kind name [program] public" strings.
func Symbols(t *testing.T, e plugin.Extractor, path, src string) []string {
	t.Helper()
	res, err := e.Extract(plugin.File{Path: path, Content: []byte(src)})
	if err != nil {
		t.Fatalf("Extract(%s): %v", path, err)
	}
	out := make([]string, 0, len(res.Symbols))
	for _, s := range res.Symbols {
		out = append(out, Render(s))
	}
	sort.Strings(out)
	return out
}

// Render formats one symbol.
func Render(s model.Symbol) string {
	pub := ""
	if s.Public {
		pub = " public"
	}
	prog := ""
	if s.Program != "" {
		prog = " [" + s.Program + "]"
	}
	return fmt.Sprintf("%s %s%s%s", s.Kind, s.Name, prog, pub)
}
