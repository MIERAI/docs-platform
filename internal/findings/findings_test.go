package findings

import (
	"testing"

	"github.com/MIERAI/docs-platform/internal/match"
	"github.com/MIERAI/docs-platform/internal/model"
)

func TestID(t *testing.T) {
	// Cross-checked with the reference implementation: sha1("docs|<asset>|<check>")[:12].
	got := ID("docs", "doc:r/README.md#cli_flag:--x", "docs.reference.unresolved.cli_flag")
	if got != "fcc74f71c13a" {
		t.Errorf("ID = %s", got)
	}
}

func TestAssetIgnoresLines(t *testing.T) {
	a := match.Issue{
		Type: match.TypeWrong, Kind: model.KindEnvVar, Subject: "X_Y", Anchor: "README.md",
		Locations: []match.Location{{Path: "README.md", Line: 3}},
	}
	b := a
	b.Locations = []match.Location{{Path: "README.md", Line: 30}}
	if Asset("r", a) != Asset("r", b) {
		t.Error("asset must not depend on line numbers")
	}
	u := match.Issue{Type: match.TypeUndocumented, Kind: model.KindCLIFlag, Subject: "--x", Program: "tool", Anchor: "cmd/tool/main.go"}
	if got := Asset("r", u); got != "code:r/cli_flag:tool --x" {
		t.Errorf("undocumented asset = %q", got)
	}
}

func TestSeverity(t *testing.T) {
	cases := []struct {
		typ  string
		conf model.Confidence
		want string
	}{
		{match.TypeWrong, model.ConfHigh, "medium"},
		{match.TypeWrong, model.ConfMedium, "low"},
		{match.TypeWrong, model.ConfLow, "info"},
		{match.TypeUndocumented, model.ConfHigh, "low"},
		{match.TypeUndocumented, model.ConfMedium, "info"},
		{match.TypeResidue, model.ConfHigh, "medium"},
	}
	for _, c := range cases {
		if got := severity(match.Issue{Type: c.typ, Confidence: c.conf}); got != c.want {
			t.Errorf("severity(%s,%s) = %s, want %s", c.typ, c.conf, got, c.want)
		}
	}
}

func TestBuildMetrics(t *testing.T) {
	rep := &match.Report{
		RefStats: []match.RefStat{{Kind: model.KindEnvVar, Status: match.Resolved, Count: 3}, {Kind: model.KindEnvVar, Status: match.Unresolved, Count: 1}},
		SymStats: []match.SymStat{{Kind: model.KindEnvVar, Documented: true, Count: 1}, {Kind: model.KindEnvVar, Count: 1}},
	}
	d := Build(Params{Repo: "r", Version: "1.0.0", Started: "t", Finished: "t", CodeAsset: true}, rep)
	vals := map[string]float64{}
	for _, m := range d.Metrics {
		vals[m.Name] = m.Value
	}
	if vals["docs_reference_resolution_ratio"] != 0.75 || vals["docs_public_symbol_coverage_ratio"] != 0.5 {
		t.Errorf("ratios = %v", vals)
	}
	if len(d.Coverage) != 2 || d.Findings == nil {
		t.Errorf("coverage = %+v, findings nil = %v", d.Coverage, d.Findings == nil)
	}
}
