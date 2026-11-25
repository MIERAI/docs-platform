package impact

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MIERAI/docs-platform/internal/repo"
)

// TestAgainstGit drives the read-only git queries against a throwaway repository.
func TestAgainstGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	put := func(rel string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	put("README.md")
	run("add", ".")
	run("commit", "-qm", "init")
	run("checkout", "-qb", "feature")
	put("cmd/a.go")
	run("add", ".")
	run("commit", "-qm", "add a\n\nDocs-Not-Needed: covered by generated help")
	ctx := context.Background()
	changed, err := repo.ChangedFiles(ctx, dir, "main", "feature")
	if err != nil || len(changed) != 1 || changed[0] != "cmd/a.go" {
		t.Fatalf("ChangedFiles = %v, %v", changed, err)
	}
	msgs, err := repo.CommitMessages(ctx, dir, "main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	rep := Evaluate(Input{Changed: changed, Texts: []string{msgs}, Cfg: cfg})
	if rep.Verdict != VerdictWaived || rep.WaiverReason != "covered by generated help" {
		t.Errorf("verdict = %s (%q)", rep.Verdict, rep.WaiverReason)
	}
}
