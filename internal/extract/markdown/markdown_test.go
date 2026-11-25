package markdown

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

func TestParseStructure(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		headings []string
		spans    []string
		links    []string
		blocks   int
	}{
		{"atx", "# Title\n## Sub `x` ##\n", []string{"Title", "Sub `x`"}, []string{"x"}, nil, 0},
		{"setext", "Title\n=====\nSub\n---\n", []string{"Title", "Sub"}, nil, nil, 0},
		{"thematic break is not setext", "\n---\n", nil, nil, nil, 0},
		{"double backtick span", "a ``x ` y`` b", nil, []string{"x ` y"}, nil, 0},
		{"unclosed span", "a `x b", nil, nil, nil, 0},
		{"escaped backtick", "a \\`x` b", nil, nil, nil, 0},
		{"links", "[a](b.md) ![i](img.png \"t\") [r]: <ref.md>", nil, nil, []string{"b.md", "img.png"}, 0},
		{"ref definition", "[r]: docs/x.md\n", nil, nil, []string{"docs/x.md"}, 0},
		{"nested parens", "[a](x_(y).md)", nil, nil, []string{"x_(y).md"}, 0},
		{"fence content not inline", "```sh\n`x` [a](b)\n```\n", nil, nil, nil, 1},
		{"tilde fence longer close", "~~~\nx\n~~~~\n# H\n", []string{"H"}, nil, nil, 1},
		{"unclosed fence swallows rest", "```\n# not heading\n", nil, nil, nil, 1},
		{"front matter skipped", "---\ntitle: x\n---\n# H\n", []string{"H"}, nil, nil, 0},
		{"crlf", "# A\r\n`b`\r\n", []string{"A"}, []string{"b"}, nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Parse([]byte(c.src))
			if got := texts(d.Headings, func(h Heading) string { return h.Text }); !reflect.DeepEqual(got, c.headings) {
				t.Errorf("headings = %q, want %q", got, c.headings)
			}
			if got := texts(d.Spans, func(i Inline) string { return i.Text }); !reflect.DeepEqual(got, c.spans) {
				t.Errorf("spans = %q, want %q", got, c.spans)
			}
			if got := texts(d.Links, func(i Inline) string { return i.Text }); !reflect.DeepEqual(got, c.links) {
				t.Errorf("links = %q, want %q", got, c.links)
			}
			if len(d.Blocks) != c.blocks {
				t.Errorf("blocks = %d, want %d", len(d.Blocks), c.blocks)
			}
		})
	}
}

func texts[T any](xs []T, f func(T) string) []string {
	var out []string
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func TestSuppressions(t *testing.T) {
	cases := []struct {
		src  string
		want []Suppression
	}{
		{"<!-- docsp:ignore because -->\n`x`\n", []Suppression{{From: 1, To: 2, Reason: "because"}}},
		{"<!-- docsp:ignore -->\n`x`\n", nil}, // no reason, not honoured
		{"<!-- docsp:ignore-start why -->\na\n<!-- docsp:ignore-end -->\n", []Suppression{{From: 1, To: 3, Reason: "why"}}},
		{"<!-- docsp:ignore-start open -->\na\n", []Suppression{{From: 1, To: 3, Reason: "open"}}},
	}
	for _, c := range cases {
		if got := Parse([]byte(c.src)).Suppressions; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: suppressions = %+v, want %+v", c.src, got, c.want)
		}
	}
}

func TestSlug(t *testing.T) {
	s := NewSlugger()
	cases := []struct{ in, want string }{
		{"Install", "install"},
		{"Hello, World!", "hello-world"},
		{"Use `docsp check` now", "use-docsp-check-now"},
		{"[Link](x.md) text", "link-text"},
		{"Install", "install-1"},
		{"日本語 見出し", "日本語-見出し"},
		{"snake_case **bold**", "snake_case-bold"},
		{"Install", "install-2"},
	}
	for _, c := range cases {
		if got := s.Slug(c.in); got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractReferences(t *testing.T) {
	src := "# T\n\nUse `--flag`, `$HOME_DIR`, `cfg/a.yaml` and [x](sub/b.md#Part).\n\n```sh\n$ tool run --fast\noutput line --not-a-flag\n```\n"
	res, err := Extractor{}.Extract(plugin.File{Path: "docs/r.md", Content: []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.Kind{
		"--flag": model.KindCLIFlag, "HOME_DIR": model.KindEnvVar, "cfg/a.yaml": model.KindFilePath,
		"docs/sub/b.md#part": model.KindLink, "run": model.KindCLICommand, "--fast": model.KindCLIFlag,
	}
	got := map[string]model.Kind{}
	for _, r := range res.References {
		got[r.Text] = r.Kind
	}
	for text, kind := range want {
		if got[text] != kind {
			t.Errorf("reference %q: kind %q, want %q (all: %v)", text, got[text], kind, got)
		}
	}
	if _, ok := got["--not-a-flag"]; ok {
		t.Error("console output line was parsed as a command")
	}
	if len(res.Symbols) != 1 || res.Symbols[0].Name != "docs/r.md#t" {
		t.Errorf("anchors = %+v", res.Symbols)
	}
}

func TestResolveLink(t *testing.T) {
	cases := []struct {
		from, dest, want string
		ok               bool
	}{
		{"README.md", "docs/a.md", "docs/a.md", true},
		{"docs/a.md", "../README.md#Top", "README.md#top", true},
		{"docs/a.md", "#local", "docs/a.md#local", true},
		{"docs/a.md", "/abs/b.md", "abs/b.md", true},
		{"docs/a.md", "my%20file.md", "docs/my file.md", true},
		{"a.md", "https://example.com/x", "", false},
		{"a.md", "mailto:x@y", "", false},
		{"a.md", "{{ .URL }}", "", false},
	}
	for _, c := range cases {
		got, ok := resolveLink(c.from, c.dest)
		if got != c.want || ok != c.ok {
			t.Errorf("resolveLink(%q,%q) = %q,%v want %q,%v", c.from, c.dest, got, ok, c.want, c.ok)
		}
	}
}

// FuzzParse: the parser must never panic, and every reported line must exist.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"# a\n`b`\n", "```\nx\n", "[a](b(c)", "<!-- docsp:ignore-start x -->", "---\n", "a\n===\n", "``` `\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		d := Parse(b)
		lines := strings.Count(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") + 1
		for _, h := range d.Headings {
			if h.Line < 1 || h.Line > lines {
				t.Fatalf("heading line %d out of range 1..%d", h.Line, lines)
			}
		}
		for _, s := range append(append([]Inline{}, d.Spans...), d.Links...) {
			if s.Line < 1 || s.Line > lines {
				t.Fatalf("inline line %d out of range 1..%d", s.Line, lines)
			}
		}
	})
}

// FuzzExtract: the full extractor (parser + classifier) must not panic and must keep valid UTF-8
// input valid in its output.
func FuzzExtract(f *testing.F) {
	f.Add([]byte("Use `--x` and `A_B` in `a/b.go`.\n```sh\nFOO=1 ./s.sh --y | make t\n```\n"))
	f.Add([]byte("[x](../../a.md#b) `${X:-y}` `rate(a_total[5m])`"))
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := Extractor{}.Extract(plugin.File{Path: "d/x.md", Content: b})
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.Valid(b) {
			return
		}
		for _, r := range res.References {
			if !utf8.ValidString(r.Text) {
				t.Fatalf("invalid UTF-8 reference %q from valid input", r.Text)
			}
		}
	})
}
