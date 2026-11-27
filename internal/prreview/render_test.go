package prreview

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/prtext"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/prreview -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file:\n%s", name, got)
	}
}

// sample: three document problems and one source conflict, an incomplete run.
func sample(l prtext.Lang) *Report {
	fact := func(s string) []ledger.Fact { return []ledger.Fact{{Fact: s, Status: "contradicted", Valid: true}} }
	return &Report{
		SchemaVersion: SchemaVersion, Repo: "demo", Head: "abc123", GitHub: "o/demo", Rechecked: 6,
		Problems: []Problem{
			{ID: "a", Path: "README.md", Line: 3, Text: "Retention is 7 days.", Category: "contradicted", Facts: fact("retention is 14d in cfg.yaml"), Code: []Line{{Path: "cfg.yaml", Line: 2, Text: "retention: 14d"}}},
			{ID: "b", Path: "README.md", Line: 9, Text: "Jobs: build, test.", Category: "incomplete_list"},
			{ID: "c", Path: "docs/run.md", Line: 4, Text: "See `old.md`.", Category: "dangling_reference"},
			{
				ID: "d", Path: "docs/run.md", Line: 7, Text: "The default timeout is 30s.", Category: SourcesConflict,
				Code: []Line{{Path: "a.go", Line: 5, Text: "timeout = 30 * time.Second"}, {Path: "b.go", Line: 8, Text: "timeout = 60 * time.Second"}},
			},
		},
		Incomplete: []string{prtext.ReasonBudget, prtext.ReasonJudgeFailures},
		Lang:       l, MaxProblems: 3, SummaryURL: "https://github.com/o/demo/actions/runs/1",
	}
}

var reMarker = regexp.MustCompile(`<!-- docsp:(?:fb|pr)[^>]*-->`)

func TestCommentGoldenPerLanguage(t *testing.T) {
	var markers [][]string
	for _, l := range []prtext.Lang{prtext.ZH, prtext.EN, prtext.JA} {
		r := sample(l)
		md := r.Markdown(map[string]bool{"README.md:3 design_stale contradicted": true})
		golden(t, "comment."+string(l)+".md", md)
		golden(t, "summary."+string(l)+".md", r.Summary())
		markers = append(markers, reMarker.FindAllString(md, -1))
		// at most MaxProblems shown, the rest counted with the link
		if strings.Count(md, "\n#### ") != 3 || !strings.Contains(md, "https://github.com/o/demo/actions/runs/1") {
			t.Errorf("%s: cap / link:\n%s", l, md)
		}
		if !strings.Contains(md, prtext.IncompleteHeadline(l)) || !strings.Contains(r.Summary(), prtext.IncompleteHeadline(l)) {
			t.Errorf("%s: an incomplete run must say so", l)
		}
		// ticks are read back the same way in every language
		fb := factorypr.Ticked(md)
		if len(fb) != 1 || fb[0].Rule != "README.md:3" || fb[0].Option != factorypr.OptionDesignStale {
			t.Errorf("%s: read back %+v", l, fb)
		}
	}
	for i := 1; i < len(markers); i++ {
		if !slices.Equal(markers[0], markers[i]) {
			t.Errorf("markers differ between languages:\n%v\n%v", markers[0], markers[i])
		}
	}
	if (&Report{Lang: "en"}).Markdown(nil) != "" {
		t.Error("a complete run without problems has no comment")
	}
}

func TestSourcesConflictComesLastWithItsOwnQuestion(t *testing.T) {
	r := sample(prtext.EN)
	r.MaxProblems, r.Incomplete = 10, nil
	md := r.Markdown(nil)
	if !strings.Contains(md, "**3 documentation lines** no longer match") || !strings.Contains(md, "for **1 documentation lines** the code disagrees with itself") {
		t.Errorf("headline:\n%s", md)
	}
	i, j := strings.Index(md, "docs/run.md:4"), strings.Index(md, "Which place in the code is right?")
	if i < 0 || j < i || strings.Contains(md[j:], "design_stale") || strings.Contains(md, "Incomplete") {
		t.Errorf("conflict section:\n%s", md)
	}
}

func TestIncompleteWithoutProblemsStillComments(t *testing.T) {
	r := &Report{Lang: prtext.EN, Incomplete: []string{prtext.ReasonAborted}, Problems: []Problem{}}
	md := r.Markdown(nil)
	if !strings.Contains(md, Marker) || !strings.Contains(md, "Incomplete: this does not mean everything is fine.") || !strings.Contains(md, "no documentation line this PR makes inconsistent was found so far") {
		t.Errorf("comment:\n%s", md)
	}
	if !r.Unchanged(md) || r.Unchanged(r.NoProblemsComment()) {
		t.Error("an incomplete run is not the no-problems comment")
	}
	complete := &Report{Lang: prtext.EN}
	if !complete.Unchanged(complete.NoProblemsComment()) || complete.Signature() == r.Signature() {
		t.Error("signature")
	}
}

func TestAnnotations(t *testing.T) {
	r := sample(prtext.EN)
	r.Problems[1].Text = "Jobs: a, b\n::error::injected"
	got := strings.Join(r.Annotations(), "\n") + "\n"
	golden(t, "annotations.en.txt", got)
	if strings.Count(got, "\n") != 4 || strings.Contains(got, "\n::error::") {
		t.Errorf("one escaped command per problem:\n%s", got)
	}
}

func TestLedgerlessSaysProblemsMayPredateThePR(t *testing.T) {
	for _, l := range []prtext.Lang{prtext.ZH, prtext.EN, prtext.JA} {
		r := sample(l)
		r.Ledgerless = true
		md := r.Markdown(nil)
		if !strings.Contains(md, l.T("PR 之前就存在", "may predate this PR", "PR 以前からの問題")) || strings.Contains(md, l.T("改了代码,让", "changed the code so that", "コード変更で")) {
			t.Errorf("%s ledger-less comment:\n%s", l, md)
		}
	}
}
