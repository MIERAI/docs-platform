package factorypr

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factoryai"
	"github.com/MIERAI/docs-platform/internal/factorytry"
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
		t.Fatalf("%v (run go test ./internal/factorypr -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden file:\n%s", name, got)
	}
}

func sampleReport(l prtext.Lang) *Report {
	d := &Draft{Verified: true, Summary: "the design says 300 s, the code 600 s", Question: "", Edits: []factoryai.Edit{{Path: "design.yaml", Old: "300", New: "600"}}, Patch: "--- a/design.yaml\n+++ b/design.yaml\n@@ -1 +1 @@\n-300\n+600\n"}
	return &Report{
		Head: "h", GitHub: "o/af", Lang: l, MaxProblems: 2, SummaryURL: "https://github.com/o/af/actions/runs/7",
		Failing: map[string][]factorytry.Case{"a": {{Name: "ex1", Detail: "response.sla: want 1, got <nil>"}}},
		Rules: []Rule{
			{API: "a", Path: "design.yaml", Line: 3, Text: "  - SLA 300 s", Category: agentic.Contradicted, Evidence: EvidenceExecuted, Draft: d},
			{API: "a", Path: "design.yaml", Line: 5, Text: "  - health", Category: agentic.OutdatedWording, Evidence: EvidenceRead},
			{API: "b", Path: "design.yaml", Line: 9, Text: "  - quota", Category: agentic.IncompleteList, Evidence: EvidenceRead},
			{API: "b", Path: "design.yaml", Line: 12, Text: "  - name", Category: agentic.Confirmed},
			{API: "b", Path: "design.yaml", Line: 14, Text: "  - limit", Category: agentic.Unknown, Error: "budget exhausted"},
		},
	}
}

var reMarker = regexp.MustCompile(`<!-- docsp:(?:fb|factory-pr)[^>]*-->`)

func TestFactoryCommentPerLanguage(t *testing.T) {
	var markers [][]string
	for _, l := range []prtext.Lang{prtext.ZH, prtext.EN, prtext.JA} {
		r := sampleReport(l)
		md := r.MarkdownWith(map[string]bool{"design.yaml:5 code_wrong outdated_wording": true})
		golden(t, "comment."+string(l)+".md", md)
		markers = append(markers, reMarker.FindAllString(md, -1))
		if strings.Count(md, "\n#### ") != 2 || !strings.Contains(md, "https://github.com/o/af/actions/runs/7") {
			t.Errorf("%s: at most 2 problems shown, the rest linked:\n%s", l, md)
		}
		if !strings.Contains(md, prtext.IncompleteHeadline(l)) || !strings.Contains(r.Summary(), prtext.IncompleteHeadline(l)) {
			t.Errorf("%s: a judge failure makes the run incomplete:\n%s", l, md)
		}
		if fb := Ticked(md); len(fb) != 1 || fb[0].Option != OptionCodeWrong || fb[0].Rule != "design.yaml:5" {
			t.Errorf("%s: read back %+v", l, fb)
		}
		if a := r.Annotations(); len(a) != 3 || !strings.HasPrefix(a[0], "::warning file=design.yaml,line=3,") {
			t.Errorf("%s: annotations %v", l, a)
		}
	}
	for i := 1; i < len(markers); i++ {
		if !slices.Equal(markers[0], markers[i]) {
			t.Errorf("markers differ between languages:\n%v\n%v", markers[0], markers[i])
		}
	}
	en := &Report{Lang: prtext.EN}
	if !en.Unchanged(en.NoRulesComment()) || !strings.Contains(en.NoRulesComment(), Marker) {
		t.Error("no-rules comment in English")
	}
}
