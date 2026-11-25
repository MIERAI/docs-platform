package history

import (
	"context"
	"testing"
)

func TestParseCommit(t *testing.T) {
	cases := map[string]Commit{
		"abc123 1700000000":     {ID: "abc123", Time: 1700000000},
		"-x 1":                  {},
		"abc notatime":          {},
		"":                      {},
		"abc 1 trailing fields": {ID: "abc", Time: 1},
	}
	for in, want := range cases {
		if got := parseCommit(in); got != want {
			t.Errorf("parseCommit(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestNoGit(t *testing.T) {
	h := Open(context.Background(), t.TempDir())
	if h.Available() {
		t.Skip("temp dir is inside a git work tree")
	}
	if _, ok := h.LineChanged(context.Background(), "README.md", 1); ok {
		t.Error("no history must mean unknown")
	}
	if h.Tags(context.Background()) != nil || h.CommitsAfter(context.Background(), Commit{ID: "a"}, []string{"x"}) != 0 {
		t.Error("no history must mean no signals")
	}
}
