package yamlkeys

import (
	"reflect"
	"strings"
	"testing"
)

func paths(docs []Doc) []string {
	var out []string
	for _, d := range docs {
		for _, k := range d.Keys {
			out = append(out, k.Path)
		}
	}
	return out
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		want    []string
		wantErr bool
	}{
		{"nested", "a:\n  b: 1\n  c:\n    d: x\n", []string{"a", "a.b", "a.c", "a.c.d"}, false},
		{"sequence items add no segment", "s:\n  - n: 1\n  - m: 2\n", []string{"s", "s.n", "s.m"}, false},
		{"multi document", "a: 1\n---\nb: 2\n", []string{"a", "b"}, false},
		{"merge key skipped", "base: &b\n  x: 1\nderived:\n  <<: *b\n  y: 2\n", []string{"base", "base.x", "derived", "derived.y"}, false},
		{"empty", "", nil, false},
		{"syntax error", "a: [1,\n", nil, true},
		{"scalar document", "just a string\n", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			docs, err := Parse([]byte(c.src))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got := paths(docs); !reflect.DeepEqual(got, c.want) {
				t.Errorf("paths = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDepthLimit(t *testing.T) {
	var b strings.Builder
	for i := range MaxDepth + 5 {
		b.WriteString(strings.Repeat("  ", i) + "k:\n")
	}
	if _, err := Parse([]byte(b.String())); err == nil {
		t.Error("expected an error for a document deeper than MaxDepth")
	}
}

func TestHelpers(t *testing.T) {
	docs, err := Parse([]byte("on:\n  workflow_dispatch:\n    inputs:\n      a: {}\n      b: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	keys := MapKeys(Path(docs[0].Root, "on", "workflow_dispatch", "inputs"))
	if len(keys) != 2 || keys[0].Path != "a" || keys[1].Line != 5 {
		t.Errorf("MapKeys = %+v", keys)
	}
	if Path(docs[0].Root, "missing", "x") != nil || MapKeys(nil) != nil {
		t.Error("missing paths must yield nil")
	}
}

// FuzzParse: hostile YAML (aliases, deep nesting, garbage) must never panic or hang.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"a: &x [*x]\n", "a:\n  b: c\n", "- [a, {b: c}]\n", "a: !!binary aGk=\n", "{{{{", "? a\n: b\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		docs, _ := Parse(b)
		for _, d := range docs {
			for _, k := range d.Keys {
				if k.Depth > MaxDepth {
					t.Fatalf("key deeper than MaxDepth: %d", k.Depth)
				}
			}
		}
	})
}
