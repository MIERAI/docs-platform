package prreview

import (
	"context"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

type lines map[string]string

func (l lines) LineText(_ context.Context, p string, _ int) (string, bool) {
	s, ok := l[p]
	return s, ok
}

func TestCompareReportsOnlyNewProblemsJudgedAtHead(t *testing.T) {
	claim := func(id, cat, ref string) ledger.Claim {
		return ledger.Claim{
			ID: id, Repo: "r", Path: "README.md", Line: 3, Text: "retention is 7 days", Category: cat, VerifiedRef: ref,
			Facts:    []ledger.Fact{{Fact: "retention is 7 days", Status: "contradicted", Valid: true}},
			Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "cfg.yaml", Line: 2}}},
		}
	}
	before := &ledger.Ledger{Claims: []ledger.Claim{claim("a", "confirmed", "m"), claim("b", "contradicted", "m"), claim("c", "confirmed", "m")}}
	after := &ledger.Ledger{Claims: []ledger.Claim{
		claim("a", "contradicted", "h"), // new problem: reported
		claim("b", "contradicted", "h"), // backlog: not reported
		claim("c", "contradicted", "m"), // not judged at the head: not reported
		claim("d", "confirmed", "h"),
	}}
	rep := Compare(context.Background(), before, after, "h", lines{"cfg.yaml": "retention: 14d"})
	if rep.Rechecked != 3 || len(rep.Problems) != 1 || rep.Problems[0].ID != "a" || rep.Problems[0].Code[0].Text != "retention: 14d" {
		t.Fatalf("%+v", rep)
	}
	md := rep.Markdown(nil)
	for _, s := range []string{Marker, "让 **1 处文档**和代码不一致", "**文档该改,还是代码改错了?**", "- [ ] 都不是:这里没有不一致(误报)", "retention: 14d"} {
		if !strings.Contains(md, s) {
			t.Errorf("comment lacks %q:\n%s", s, md)
		}
	}
	body := strings.Replace(md, "- [ ] 文档过时了", "- [x] 文档过时了", 1)
	fb := factorypr.Ticked(body)
	if len(fb) != 1 || fb[0].Option != factorypr.OptionDesignStale || !strings.Contains(rep.Markdown(factorypr.TickedKeys(fb)), "- [x] 文档过时了") {
		t.Errorf("feedback %+v", fb)
	}
	if !rep.Unchanged(body) || (&Report{}).Markdown(nil) != "" || !(&Report{}).Unchanged(NoProblemsComment()) {
		t.Error("signature / empty report")
	}
}
