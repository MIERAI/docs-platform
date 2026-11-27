package semantic

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/extract/markdown"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
)

// TestFilterKeepsClaimsFromJudge: a changelog is not read for claims; a historical sentence
// is not put to the judge; both are counted by reason. Without the filter they are judged.
func TestFilterKeepsClaimsFromJudge(t *testing.T) {
	dir := fixtureRepo(t)
	write(t, dir, "CHANGELOG.md", "# Changelog\n\n## 0.2.0\n\n- The `Window` constant in `window.go` holds 100 samples.\n")
	write(t, dir, "NOTES.md", "# Notes\n\nThe `Window` constant in `window.go` previously held 100 samples.\n")
	commit(t, dir, "notes", time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC))
	run := func(f claim.Filter) (*Result, []string) {
		var asked []string
		var mu sync.Mutex
		j := &fakeJudge{reply: func(p string) string {
			mu.Lock()
			defer mu.Unlock()
			for _, d := range []string{"CHANGELOG.md", "NOTES.md"} {
				if strings.Contains(p, d) {
					asked = append(asked, d)
				}
			}
			return `{"verdict":"no","quote":""}`
		}}
		r, err := repo.Open(dir, nil)
		if err != nil {
			t.Fatal(err)
		}
		inv := scan.Run(r, []plugin.Extractor{markdown.Extractor{}}, scan.Options{Docs: []string{"*.md"}})
		res, err := Run(context.Background(), r, inv, Options{StaleCommits: 2, Judge: guard(t, j, ""), Repo: "demo", Filter: f})
		if err != nil {
			t.Fatal(err)
		}
		return res, asked
	}
	res, asked := run(claim.DefaultFilter())
	if res.Stats.Skipped[claim.SkipChangelog] == 0 || res.Stats.Skipped[claim.SkipHistorical] == 0 || len(asked) != 0 {
		t.Errorf("skipped %v, asked about %v", res.Stats.Skipped, asked)
	}
	res, asked = run(claim.Filter{})
	if len(res.Stats.Skipped) != 0 || len(asked) == 0 {
		t.Errorf("zero filter: skipped %v, asked about %v", res.Stats.Skipped, asked)
	}
}
