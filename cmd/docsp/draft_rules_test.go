package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/rulejudge"
)

func TestRulesDraftNeedsNoModel(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nimport \"flag\"\n\nfunc main() {\n\tflag.Int(\"workers\", 8, \"worker pool size\")\n}\n"
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "pool"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "pool", "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "c"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	head, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	st, err := rulejudge.LoadDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(text, model string) *ledger.Claim {
		return &ledger.Claim{
			ID: text, Repo: "r", Path: "README.md", Line: 3, Text: text, Category: "contradicted", Model: model,
			VerifiedRef: strings.TrimSpace(string(head)), Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "cmd/pool/main.go", Line: 6}}},
		}
	}
	if needsModel([]*ledger.Claim{mk("x", rulejudge.Model)}) || !needsModel([]*ledger.Claim{mk("x", rulejudge.Model), mk("y", "claude")}) {
		t.Error("needsModel")
	}
	now := time.Now()
	e := rulesDraft(context.Background(), st, dir, mk("| `-workers N` | worker pool size (default 4) |", rulejudge.Model), now)
	if e.Status != draft.Proposed || e.Draft != "| `-workers N` | worker pool size (default 8) |" || e.Model != rulejudge.Model || e.DiffTokens != 2 {
		t.Errorf("rules draft %+v", e)
	}
	// The code changed since: the rules confirm the line now, no draft.
	e = rulesDraft(context.Background(), st, dir, mk("| `-workers N` | worker pool size (default 8) |", rulejudge.Model), now)
	if e.Status != draft.RejectedByHarness || e.Draft != "" || len(e.Problems) != 1 {
		t.Errorf("stale rules claim %+v", e)
	}
}
