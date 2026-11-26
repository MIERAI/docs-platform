package judge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MIERAI/docs-platform/judgev1"
)

// A checkout given as a relative path must reach the out-of-process tool server as an
// absolute root: the server runs in the CLI's scratch directory, where a relative root
// resolves to nothing and every tool call fails.
func TestWriteMCPSpecAbsoluteRoot(t *testing.T) {
	wt := worktree(t)
	t.Chdir(filepath.Dir(wt))
	in := testInstance(t, "")
	tl, err := NewPolicyTools(filepath.Base(wt), Policy{Repo: "pub", Class: in.Classification, Cloud: true}, judgev1.ToolLimits{MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tl.Close() }()
	spec := filepath.Join(t.TempDir(), "tools.json")
	if err := WriteMCPSpec(tl, spec, filepath.Join(t.TempDir(), "t.jsonl")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(spec)
	if err != nil {
		t.Fatal(err)
	}
	var s MCPSpec
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(wt)
	got, _ := filepath.EvalSymlinks(s.Root)
	if !filepath.IsAbs(s.Root) || got != want {
		t.Fatalf("spec root = %q, want absolute %q", s.Root, wt)
	}
}
