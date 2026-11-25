package repo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*.md", "docs/a.md", true},
		{"*.md", "a.go", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/x/a.md", false},
		{"docs/**", "docs/x/a.md", true},
		{"**/*.go", "a.go", true},
		{"**/*.go", "x/y/a.go", true},
		{"cmd/**", "cmd", true},
		{"testdata/", "testdata/x/y", true},
		{"testdata/", "a/testdata", false},
		{"/root.md", "root.md", true},
		{"[", "x", false},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := Match(c.pat, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestValidRef(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"main", true},
		{"origin/main", true},
		{"HEAD~1", true},
		{"v1.2.3", true},
		{"0123abcd", true},
		{"HEAD@{1}", true},
		{"--upload-pack=x", false},
		{"-x", false},
		{"a..b", false},
		{"a b", false},
		{"", false},
		{"$(rm)", false},
		{"a;b", false},
	}
	for _, c := range cases {
		if got := ValidRef(c.ref); got != c.want {
			t.Errorf("ValidRef(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

func TestOpenAndRead(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.md", "x")
	write(t, dir, "sub/b.go", "y")
	write(t, dir, "vendor/v.go", "z")
	write(t, dir, "skip/c.md", "w")
	r, err := Open(dir, []string{"skip/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Files(); len(got) != 2 || got[0] != "a.md" || got[1] != "sub/b.go" {
		t.Errorf("Files = %q", got)
	}
	if !r.IsFile("skip/c.md") || !r.IsDir("sub") || r.Exists("vendor/v.go") || !r.HasDirNamed("sub") {
		t.Error("existence index: excluded files exist, vendored trees do not")
	}
	if b, err := r.Read("sub/b.go"); err != nil || string(b) != "y" {
		t.Errorf("Read = %q, %v", b, err)
	}
	for _, bad := range []string{"../x", "/etc/passwd", "sub/../../x"} {
		if _, err := r.Read(bad); err == nil {
			t.Errorf("Read(%q) escaped the root", bad)
		}
	}
}

func TestGitAllowlist(t *testing.T) {
	for _, args := range [][]string{{"push"}, {"config", "x"}, {}, {"checkout", "main"}} {
		if _, err := Git(context.Background(), t.TempDir(), args...); err == nil {
			t.Errorf("Git(%v) ran a non-read-only sub-command", args)
		}
	}
	if _, err := ChangedFiles(context.Background(), ".", "--output=/tmp/x", "HEAD"); err == nil {
		t.Error("an option-looking ref was accepted")
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
