package consistency

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/findings"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

var now = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

func cand(id string, prio int, class string, rank int, age float64, maxAge int) candidate {
	c := candidate{
		Pick: Pick{ID: id, Path: id + ".md", Class: class, Priority: prio, AgeDays: age, MaxAgeDays: maxAge}, classRank: rank,
		pressure: age / float64(maxAge), fresh: prio == PriorityCycle,
	}
	c.Required = prio < PriorityCycle
	return c
}

func TestChooseOrderQuotaBudget(t *testing.T) {
	p := instance.Consistency{MaxAgeDays: 10, RunIntervalDays: 1}.WithDefaults()
	cands := []candidate{
		cand("cycle-young", PriorityCycle, "C", 1, 1, 10),
		cand("due-c", PriorityDue, "C", 1, 12, 10),
		cand("cycle-old", PriorityCycle, "C", 1, 9, 10),
		cand("stale", PriorityStale, "C", 1, 0, 10),
		cand("due-a", PriorityDue, "A", 0, 3, 2),
		cand("recheck", PriorityRecheck, "C", 1, 11, 10),
	}
	// need = 5 x 1/10 + 1/2 = 1 -> quota max(0, 1-4) = 0 required beyond the 4 due
	s := choose("r", "ref", now, p, cands)
	got := ids(s.Picked)
	if got != "stale,due-a,due-c,recheck" || s.Counts.Quota != 0 || *s.Freshness != 2.0/6 {
		t.Fatalf("picked %s quota %d freshness %v", got, s.Counts.Quota, *s.Freshness)
	}
	// no due claims: the cycle quota takes the oldest relative to their max age
	p.RunIntervalDays = 5
	s = choose("r", "ref", now, p, []candidate{cand("a", PriorityCycle, "C", 1, 1, 10), cand("b", PriorityCycle, "C", 1, 8, 10), cand("c", PriorityCycle, "C", 1, 4, 10)})
	if ids(s.Picked) != "b,c" || s.Counts.Quota != 2 || !strings.HasPrefix(s.Picked[0].Reason, "cycle count") {
		t.Fatalf("cycle quota: %s %d", ids(s.Picked), s.Counts.Quota)
	}
	// the budget cuts, and reports what it cut
	p.RunIntervalDays, p.Budget.MaxUSD = 1, 0.1
	s = choose("r", "ref", now, p, cands)
	if s.Counts.Picked != 2 || s.Counts.Shortfall != 2 || ids(s.Shortfall) != "due-c,recheck" || s.EstimatedUSD != 0.08 {
		t.Fatalf("budget: %+v %s", s.Counts, ids(s.Shortfall))
	}
}

func ids(ps []Pick) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return strings.Join(out, ",")
}

func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for p, c := range map[string]string{"README.md": "# R\n\nPort 9090.\nJobs: build.\nUnknowable.\n", "config.yaml": "port: 9090\n", "docs/run.md": "Run it.\n"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "c")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func claim(repoName, p string, //nolint:unparam // keeps the claim's full shape for future cases
	line int, text, cat, at, ref string, quote string,
) ledger.Claim {
	c := ledger.Claim{ID: ledger.ClaimID(repoName, p, text), Repo: repoName, Path: p, Line: line, Text: text, Category: cat, Verdict: "no", VerifiedAt: at, VerifiedRef: ref}
	if quote != "" {
		c.Evidence.Quotes = []ledger.Quote{{Path: quote, Line: 1, Blob: "x"}}
	}
	return c
}

func TestPlanOnRepository(t *testing.T) {
	dir, ref := gitRepo(t)
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion, Claims: []ledger.Claim{
		claim("r", "README.md", 3, "Port 9090.", "confirmed", "2026-12-30T00:00:00Z", ref, "config.yaml"),
		claim("r", "README.md", 4, "Jobs: build.", "confirmed", "2026-12-01T00:00:00Z", ref, "config.yaml"),
		claim("r", "README.md", 5, "Unknowable.", "unverifiable_in_repo", "2026-11-01T00:00:00Z", ref, ""),
		claim("r", "docs/run.md", 1, "Run it.", "confirmed", "2026-12-29T00:00:00Z", ref, "config.yaml"),
	}}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("port: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qam", "port")
	p := instance.Consistency{MaxAgeDays: 20, Classes: []instance.ABCClass{{Name: "A", MaxAgeDays: 5, Docs: []string{"docs/**"}}}}
	s, err := Plan(context.Background(), Input{Ledger: l, Repo: "r", Dir: dir, Ref: "HEAD", Policy: p, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if errs := schema(t, contracts.Schedule).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("schedule/v1: %v", errs)
	}
	// every claim quoting config.yaml is stale now; the old unverifiable one is due for recheck
	if s.Counts.Stale != 3 || s.Counts.Recheck != 1 || s.Picked[0].Class != "A" || !strings.HasPrefix(s.Picked[0].Reason, "stale: evidence_changed") {
		t.Fatalf("%+v %+v", s.Counts, s.Picked)
	}
	st := &State{Docs: map[string]DocState{"docs/run.md": {MaxAgeDays: 1, FullReaudit: true}}}
	s, _ = Plan(context.Background(), Input{Ledger: l, Repo: "r", Dir: dir, Ref: ref, Policy: p, State: st, Now: now})
	for _, pk := range s.Picked {
		if pk.Path == "docs/run.md" && (pk.Reason != "error budget burned: full re-audit" || pk.MaxAgeDays != 1) {
			t.Fatalf("state ignored: %+v", pk)
		}
	}
	// the repository's audit.skip filter keeps claims out of the plan, counted by reason
	skip := func(_, text string) string {
		if text == "Port 9090." {
			return "historical"
		}
		return ""
	}
	s, _ = Plan(context.Background(), Input{Ledger: l, Repo: "r", Dir: dir, Ref: "HEAD", Policy: p, Now: now, Skip: skip})
	b, _ = json.Marshal(s)
	if errs := schema(t, contracts.Schedule).ValidateJSON(b); len(errs) > 0 || s.Skipped["historical"] != 1 || s.Counts.Stale != 2 {
		t.Fatalf("skip: %v %+v %+v", errs, s.Skipped, s.Counts)
	}
}

func schema(t *testing.T, b []byte) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.Compile(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReportBudgetAndActions(t *testing.T) {
	res := &evalmodel.Result{Provider: "claude-code", Scores: evalmodel.Scores{CostUSD: 1.2}}
	add := func(p string, line int, verdict string) {
		res.Items = append(res.Items, evalmodel.ItemResult{ID: SampleID("r", p, line), Source: SourceAccuracySample, Verdict: verdict})
	}
	for i := range 8 {
		add("a.md", i+1, judgev1.No)
	}
	add("b.md", 1, judgev1.Yes)
	add("b.md", 2, judgev1.Yes)
	add("c.md", 1, judgev1.Unknown)
	res.Items = append(res.Items, evalmodel.ItemResult{ID: SampleID("other", "x.md", 1), Source: SourceAccuracySample, Verdict: judgev1.Yes})
	fresh := 0.9
	doc := &findings.Document{
		Metrics: []findings.Metric{
			{Name: "docs_references", Value: 95, Labels: map[string]string{"status": "resolved"}},
			{Name: "docs_references", Value: 5, Labels: map[string]string{"status": "unresolved"}},
		},
		Findings: []findings.Finding{{Check: "docs.reference.unresolved.file_path", Severity: "medium"}, {Check: "docs.reference.unresolved.cli_flag", Severity: "info"}},
	}
	l := &ledger.Ledger{Claims: []ledger.Claim{{Repo: "r", Category: "dangling_reference"}, {Repo: "r", Category: "confirmed"}, {Repo: "other", Category: "dangling_reference"}}}
	r := Report(SLOInput{
		Repo: "r", Policy: instance.Consistency{}, Sample: res, Check: doc, Ledger: l, Now: now,
		Schedule: &Schedule{Freshness: &fresh, Counts: Counts{Claims: 10, Picked: 3}, EstimatedUSD: 0.12},
	})
	b, _ := json.Marshal(r)
	if errs := schema(t, contracts.SLO).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("slo/v1: %v", errs)
	}
	if *r.Accuracy.Value != 0.8 || r.Accuracy.N != 10 || r.Accuracy.Bad != 2 || !r.Accuracy.Met || *r.Accuracy.Low > 0.6 {
		t.Fatalf("accuracy %+v", r.Accuracy)
	}
	if r.Budget == nil || r.Budget.Burned || r.Budget.Remaining > 1e-9 || r.Budget.Remaining < -1e-9 {
		t.Fatalf("budget %+v", r.Budget)
	}
	if len(r.Actions) != 1 || r.Actions[0].Path != "b.md" || r.Actions[0].MaxAgeDays != 15 {
		t.Fatalf("actions %+v", r.Actions)
	}
	if r.Freshness.Met || r.Freshness.Bad != 1 || r.Phantom.N != 102 || r.Phantom.Bad != 2 || r.Phantom.Met || r.Cost.SampleUSD != 1.2 {
		t.Fatalf("freshness %+v phantom %+v cost %+v", r.Freshness, r.Phantom, r.Cost)
	}
	st := &State{Docs: map[string]DocState{}}
	Apply(st, r, now)
	if d := st.Docs["b.md"]; !d.FullReaudit || d.MaxAgeDays != 15 {
		t.Fatalf("state %+v", st)
	}
	Reaudited(st, []string{"b.md"})
	if st.Docs["b.md"].FullReaudit || st.Docs["b.md"].MaxAgeDays != 15 {
		t.Fatalf("reaudited %+v", st.Docs["b.md"])
	}
	p := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(p); err != nil {
		t.Fatal(err)
	}
	if back, err := LoadState(p); err != nil || back.Docs["b.md"].MaxAgeDays != 15 {
		t.Fatalf("%v %+v", err, back)
	}
	if empty := Report(SLOInput{Repo: "r", Now: now}); empty.Accuracy.Value != nil || empty.Budget != nil {
		t.Fatal("accuracy without a sample")
	}
}

func TestSampleIsReproducible(t *testing.T) {
	dir, ref := gitRepo(t)
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion}
	for i, txt := range []string{"Port 9090.", "Jobs: build.", "Unknowable."} {
		l.Claims = append(l.Claims, claim("r", "README.md", i+3, txt, "confirmed", "2026-12-30T00:00:00Z", ref, "config.yaml"))
	}
	a, err := Sample(context.Background(), l, dir, "r", "HEAD", 2, 42, now)
	if err != nil || len(a) != 2 {
		t.Fatalf("%v %d", err, len(a))
	}
	b, _ := Sample(context.Background(), l, dir, "r", "HEAD", 2, 42, now)
	if a[0].ID != b[0].ID || a[1].ID != b[1].ID || a[0].Expected != judgev1.No || a[0].TaskType != judgev1.TaskClaimFacts || docOf(a[0].ID) != "README.md" {
		t.Fatalf("%+v %+v", a, b)
	}
	all, _ := Sample(context.Background(), l, dir, "r", "HEAD", 10, 1, now)
	if len(all) != 3 {
		t.Fatalf("sample larger than the ledger: %d", len(all))
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if _, err := evalmodel.WriteSet(p, all); err != nil {
		t.Fatal(err)
	}
	if _, err := evalmodel.LoadSet(p); err != nil {
		t.Fatalf("sample is not a valid eval set: %v", err)
	}
}

func TestWilson(t *testing.T) {
	lo, hi := wilson(8, 10)
	if lo < 0.44 || lo > 0.5 || hi < 0.94 || hi > 0.97 {
		t.Fatalf("%v %v", lo, hi)
	}
	if lo, hi := wilson(0, 5); lo != 0 || hi > 0.5 {
		t.Fatalf("%v %v", lo, hi)
	}
}

func TestRecheckRetiresEditedLines(t *testing.T) {
	l := &ledger.Ledger{Claims: []ledger.Claim{{ID: "a", Repo: "r", Path: "x.md"}, {ID: "b", Repo: "r", Path: "x.md"}}}
	res := Recheck(context.Background(), nil, nil, l, []Pick{
		{ID: "a", Path: "x.md", Reason: "stale: doc_line_changed"}, {ID: "b", Path: "y.md", Reason: "stale: doc_removed"},
	})
	if res.Retired != 2 || res.Judged != 0 || len(l.Claims) != 0 {
		t.Fatalf("%+v %+v", res, l.Claims)
	}
}

func TestReportSeverityBatchingSuppression(t *testing.T) {
	cl := func(id, path, cat string) ledger.Claim {
		return ledger.Claim{
			ID: id, Repo: "r", Path: path, Line: 1, Text: id, Category: cat,
			Facts: []ledger.Fact{{Fact: id + " fact", Status: cat, Valid: true}},
		}
	}
	l := &ledger.Ledger{Claims: []ledger.Claim{
		cl("c1", "a.md", "contradicted"), cl("d1", "a.md", "dangling_reference"), cl("o1", "b.md", "outdated_wording"),
		cl("i1", "c.md", "incomplete_list"), cl("ok", "a.md", "confirmed"), cl("s1", "legacy/x.md", "contradicted"), cl("s2", "z.md", "contradicted"),
	}}
	p := instance.Consistency{BatchDays: 7, Suppressions: []instance.Suppression{
		{Path: "legacy/**", Reason: "being rewritten", Expires: "2027-01-31"},
		{Claim: "s2", Reason: "known", Expires: "2026-12-01"},
	}}
	st := &ReportState{LastBatch: now.Add(-24 * time.Hour).Format(time.RFC3339)}
	r := BuildReport(l, p, st, now, false)
	if ids(findPicks(r.Immediate)) != "c1,d1,s2" || len(r.Batched) != 0 || r.Held != 2 || r.BatchDue || r.NextBatch == "" {
		t.Fatalf("first: immediate %s batched %d held %d", ids(findPicks(r.Immediate)), len(r.Batched), r.Held)
	}
	b, _ := json.Marshal(r)
	if errs := schema(t, contracts.Report).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("report/v1: %v", errs)
	}
	if len(r.Suppressed) != 1 || r.Suppressed[0].ID != "s1" || len(r.ExpiredSuppressions) != 1 || r.Immediate[0].Facts[0] != "c1 fact" {
		t.Fatalf("suppressions %+v %+v", r.Suppressed, r.ExpiredSuppressions)
	}
	// reported once: nothing new until the batch is due
	r = BuildReport(l, p, st, now.Add(time.Hour), false)
	if len(r.Immediate) != 0 || r.Held != 2 {
		t.Fatalf("repeat: %+v", r)
	}
	r = BuildReport(l, p, st, now.Add(7*24*time.Hour), false)
	if !r.BatchDue || ids(findPicks(r.Batched)) != "o1,i1" {
		t.Fatalf("batch: %+v", r.Batched)
	}
	// a category change is a new finding
	l.Claims[2].Category = "contradicted"
	if r = BuildReport(l, p, st, now.Add(8*24*time.Hour), false); len(r.Immediate) != 1 || r.Immediate[0].ID != "o1" {
		t.Fatalf("category change: %+v", r.Immediate)
	}
}

func findPicks(fs []Finding) []Pick {
	out := make([]Pick, 0, len(fs))
	for _, f := range fs {
		out = append(out, Pick{ID: f.ID})
	}
	return out
}
