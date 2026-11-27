package semantic

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/extract/markdown"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
)

// TestRulesStageDecidesBeforeTheJudge: a line a typed Go fact settles becomes a finding of
// the rules (with a token-replacement draft) and is never a question; other lines still are.
func TestRulesStageDecidesBeforeTheJudge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "cmd/pool/main.go", `package main

import (
	"flag"
	"time"
)

func main() {
	flag.Int("workers", 8, "worker pool size")
	flag.Duration("timeout", 30*time.Second, "request timeout")
	flag.Parse()
}
`)
	write(t, dir, "README.md", "# Pool\n\n"+
		"| `-workers N` | worker pool size (default 4 workers) |\n\n"+
		"| `-timeout D` | request timeout (default 30s) |\n\n"+
		"The `-timeout` keeps slow `pool` requests from piling up for 30 seconds.\n")
	commit(t, dir, "init", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	r, err := repo.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv := scan.Run(r, []plugin.Extractor{markdown.Extractor{}}, scan.Options{Docs: []string{"*.md"}})
	res, err := Run(context.Background(), r, inv, Options{StaleCommits: 2, Repo: "pool"})
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.RulesDecided != 2 || st.RulesConfirmed != 1 || st.RulesContradicted != 1 || st.RulesSaved != 2 {
		t.Errorf("rules stats %+v", st)
	}
	var found *Issue
	for i := range res.Issues {
		if res.Issues[i].Rule == RuleGoFactMismatch {
			found = &res.Issues[i]
		}
	}
	if found == nil || found.Line != 3 || found.Quote != `flag.Int("workers", 8, "worker pool size")` || found.SubLayer() != SubValue ||
		!strings.Contains(found.Suggestion, "(default 8 workers)") || found.Locations[1].Path != "cmd/pool/main.go" {
		t.Fatalf("rules finding %+v (all %+v)", found, res.Issues)
	}
	if st.Candidates == 0 {
		t.Errorf("the undecided line must stay a question: %+v", st)
	}
}
