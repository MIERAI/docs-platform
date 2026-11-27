package draftverify

import (
	"context"
	"strings"
	"testing"
)

// FuzzCheckTOML: the structural TOML check never panics.
func FuzzCheckTOML(f *testing.F) {
	for _, s := range []string{"a = 1\n", "[t]\nb = [\n 1,\n]\n", "c = '''\nx\n'''\n", "d = \"\\\"\"", "a = }"} {
		f.Add(s)
	}
	f.Fuzz(func(_ *testing.T, s string) { _ = checkTOML(s) })
}

// FuzzFormatMarkdown: diffing and format-checking any pair of Markdown texts never panics
// (shell blocks are not lexed here: no subprocess per input).
func FuzzFormatMarkdown(f *testing.F) {
	f.Add("# a\n\n```go\nx\n```\n", "# a\n\n```go\nx := 1\n")
	f.Add("---\na: 1\n---\n", "t `x\n[a](b\n")
	f.Fuzz(func(t *testing.T, a, b string) {
		a, b = strings.ReplaceAll(a, "```sh", "```"), strings.ReplaceAll(b, "```sh", "```")
		cs := diffLines(splitLines(a), splitLines(b))
		for _, c := range cs {
			if c.Old == 0 && c.New == 0 {
				t.Fatalf("empty change %+v", c)
			}
		}
		_ = formatGate(context.Background(), "x.md", a, b, touchedNew(cs))
	})
}
