package rulejudge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
)

// tally counts, over a worktree's Markdown documents, the prose lines and claims and how many
// of them the rules decide.
type tally struct {
	lines, claims, decided, confirmed, contradicted, claimsDecided int
	decisions                                                      []string
}

func measure(t *testing.T, dir string) tally {
	t.Helper()
	r, err := repo.Open(dir, []string{"testdata/**", "**/testdata/**"})
	if err != nil {
		t.Fatal(err)
	}
	st := Load(r.Read, r.Files())
	var n tally
	for _, p := range r.Files() {
		if !strings.HasSuffix(p, ".md") {
			continue
		}
		b, err := r.Read(p)
		if err != nil {
			continue
		}
		claimLines := map[int]int{}
		for _, c := range claim.Markdown(p, b) {
			claimLines[c.Line]++
			n.claims++
		}
		for _, l := range claim.ProseLines(b) {
			n.lines++
			d, ok := st.Decide(p, l.Text)
			if !ok {
				continue
			}
			n.decided++
			n.claimsDecided += claimLines[l.N]
			if d.Category == agentic.Confirmed {
				n.confirmed++
			} else {
				n.contradicted++
			}
			n.decisions = append(n.decisions, d.Category+" "+p+": "+d.Reason)
		}
	}
	return n
}

// TestMeasureRulesShare measures the rules stage offline (no model, no network) on this
// repository and on the rules fixture; `go test -v -run TestMeasureRulesShare` prints the
// numbers. This repository's own documentation must not be contradicted by the rules (its
// docs are kept correct: a contradiction is a regression of the docs or a false positive).
func TestMeasureRulesShare(t *testing.T) {
	self := measure(t, filepath.Join("..", ".."))
	t.Logf("docs-platform: %d prose lines, %d claims; rules decided %d lines (%d confirmed, %d contradicted) carrying %d claims",
		self.lines, self.claims, self.decided, self.confirmed, self.contradicted, self.claimsDecided)
	for _, d := range self.decisions {
		t.Log("  " + d)
	}
	if self.contradicted > 0 {
		t.Errorf("the rules contradict %d lines of this repository's own docs", self.contradicted)
	}
	fx := measure(t, filepath.Join("testdata", "fixture"))
	t.Logf("testdata/fixture: %d prose lines, %d claims; rules decided %d lines (%d confirmed, %d contradicted) carrying %d claims",
		fx.lines, fx.claims, fx.decided, fx.confirmed, fx.contradicted, fx.claimsDecided)
	for _, d := range fx.decisions {
		t.Log("  " + d)
	}
	if fx.confirmed != 6 || fx.contradicted != 6 {
		t.Errorf("fixture: %d confirmed, %d contradicted; want 6 and 6", fx.confirmed, fx.contradicted)
	}
	if _, err := os.Stat(filepath.Join("testdata", "fixture", "README.md")); err != nil {
		t.Fatal(err)
	}
}
