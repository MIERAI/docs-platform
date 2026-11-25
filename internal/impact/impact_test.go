package impact

import (
	"testing"

	"github.com/MIERAI/docs-platform/internal/config"
)

var cfg = config.Impact{Rules: []config.ImpactRule{
	{Name: "cli", Code: []string{"cmd/**"}, Docs: []string{"docs/cli.md", "README.md"}},
	{Name: "config", Code: []string{"internal/config/**"}, Docs: []string{"docs/config.md"}},
}}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		texts   []string
		verdict string
		status  map[string]string
	}{
		{"untouched", []string{"internal/x.go"}, nil, VerdictNotApplicable, map[string]string{"cli": NotTriggered, "config": NotTriggered}},
		{"code with docs", []string{"cmd/a.go", "README.md"}, nil, VerdictPass, map[string]string{"cli": DocsUpdated}},
		{"code without docs", []string{"cmd/a.go", "docs/config.md"}, nil, VerdictFail, map[string]string{"cli": MissingDocs, "config": NotTriggered}},
		{"waived", []string{"internal/config/c.go"}, []string{"fix typo\n\nDocs-Not-Needed: internal refactor, no behaviour change\n"}, VerdictWaived, map[string]string{"config": Waived}},
		{"empty waiver does not count", []string{"cmd/a.go"}, []string{"Docs-Not-Needed:   \n"}, VerdictFail, map[string]string{"cli": MissingDocs}},
		{"docs-only change", []string{"docs/cli.md"}, nil, VerdictNotApplicable, map[string]string{"cli": NotTriggered}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := Evaluate(Input{Base: "b", Head: "h", Changed: c.changed, Texts: c.texts, Cfg: cfg, Version: "t"})
			if rep.Verdict != c.verdict {
				t.Errorf("verdict = %s, want %s", rep.Verdict, c.verdict)
			}
			for _, r := range rep.Rules {
				if want, ok := c.status[r.Name]; ok && r.Status != want {
					t.Errorf("rule %s = %s, want %s", r.Name, r.Status, want)
				}
			}
			if len(rep.Metrics) != 1 || rep.Metrics[0].Name != "docs_impact_changes" {
				t.Errorf("metrics = %+v", rep.Metrics)
			}
		})
	}
}

func TestWaiverCustomTrailer(t *testing.T) {
	if got := Waiver("No-Docs", []string{"body\nno-docs: because\n"}); got != "because" {
		t.Errorf("Waiver = %q (trailer keys are case-insensitive)", got)
	}
	if got := Waiver("No-Docs", []string{"text No-Docs: inline is not a trailer"}); got != "" {
		t.Errorf("Waiver = %q, want none", got)
	}
}
