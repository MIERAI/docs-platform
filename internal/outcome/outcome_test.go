package outcome

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

var now = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)

func claim(cat, at string, mut ...func(*ledger.Claim)) *ledger.Claim {
	c := &ledger.Claim{
		ID: "c1", Repo: "r", Path: "README.md", Line: 3, Text: "Set `X` to 1.", Category: cat, Verdict: cat,
		VerifiedAt: at, VerifiedRef: "ref-" + at, Model: "m",
		Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "main.go", Line: 7, Blob: "b1"}}},
	}
	for _, m := range mut {
		m(c)
	}
	return c
}

func led(cs ...*ledger.Claim) *ledger.Ledger {
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion}
	for _, c := range cs {
		if c != nil {
			l.Claims = append(l.Claims, *c)
		}
	}
	return l
}

func TestDiff(t *testing.T) {
	sameBlobs := map[string]string{"main.go": "b1", "README.md": "d"}
	newBlobs := map[string]string{"main.go": "b2", "README.md": "d"}
	doc := map[string]string{"README.md": "# x\n\nSet `X` to 1.\n"}
	edited := map[string]string{"README.md": "# x\n\nSet `X` to 2.\n"}
	movedLine := func(c *ledger.Claim) { c.Line = 9 }
	absence := func(c *ledger.Claim) {
		c.Evidence.Absence = []ledger.Absence{{Pattern: "X", Scope: "**", FilesHash: "h"}}
	}
	cases := []struct {
		name          string
		before, after *ledger.Claim
		blobs, docs   map[string]string
		kind, how     string
		label, source string
		category      string
	}{
		{"new problem", nil, claim("contradicted", "t2"), sameBlobs, doc, KindReported, HowNew, "", "", "contradicted"},
		{"new non-problem", nil, claim("confirmed", "t2"), sameBlobs, doc, "", "", "", "", ""},
		{"reported flip", claim("confirmed", "t1"), claim("contradicted", "t2"), sameBlobs, doc, KindReported, HowFlip, "", "", "contradicted"},
		{"reported code changed", claim("confirmed", "t1"), claim("contradicted", "t2"), newBlobs, doc, KindReported, HowCodeChanged, "", "", "contradicted"},
		{"resolved flip", claim("incomplete_list", "t1"), claim("confirmed", "t2"), sameBlobs, doc, KindResolved, HowFlip, "", "", "incomplete_list"},
		{"resolved code changed", claim("contradicted", "t1"), claim("confirmed", "t2"), newBlobs, doc, KindResolved, HowCodeChanged, "", "", "contradicted"},
		{"resolved line moved", claim("contradicted", "t1"), claim("confirmed", "t2", movedLine), sameBlobs, doc, KindResolved, HowCodeChanged, "", "", "contradicted"},
		{"absence is never unchanged", claim("contradicted", "t1", absence), claim("confirmed", "t2"), sameBlobs, doc, KindResolved, HowCodeChanged, "", "", "contradicted"},
		{"doc removed", claim("contradicted", "t1"), nil, sameBlobs, map[string]string{}, KindResolved, HowDocRemoved, "", "", "contradicted"},
		{"doc edited", claim("contradicted", "t1"), nil, sameBlobs, edited, KindResolved, HowDocEdited, LabelTP, SourceProxy, "contradicted"},
		{"dropped", claim("contradicted", "t1"), nil, sameBlobs, doc, KindResolved, HowDropped, "", "", "contradicted"},
		{"stable", claim("confirmed", "t1"), claim("confirmed", "t2"), sameBlobs, doc, KindRejudged, HowStable, "", "", "confirmed"},
		{"stable problem", claim("contradicted", "t1"), claim("contradicted", "t2"), sameBlobs, doc, KindRejudged, HowStable, "", "", "contradicted"},
		{"changed and held: nothing", claim("contradicted", "t1"), claim("contradicted", "t2"), newBlobs, doc, "", "", "", "", ""},
		{"not judged again: nothing", claim("contradicted", "t1"), claim("confirmed", "t1"), sameBlobs, doc, "", "", "", "", ""},
		{"non-problem gone: nothing", claim("confirmed", "t1"), nil, sameBlobs, edited, "", "", "", "", ""},
	}
	sch := schema(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blobAt := func(p string) (string, bool) { b, ok := tc.blobs[p]; return b, ok }
			docAt := func(p string) (string, bool) { d, ok := tc.docs[p]; return d, ok }
			ev := Diff(led(tc.before), led(tc.after), "head", now, blobAt, docAt)
			if tc.kind == "" {
				if len(ev) != 0 {
					t.Fatalf("want no event, got %+v", ev)
				}
				return
			}
			if len(ev) != 1 {
				t.Fatalf("want one event, got %+v", ev)
			}
			e := ev[0]
			if e.Kind != tc.kind || e.How != tc.how || e.Label != tc.label || e.Source != tc.source || e.Category != tc.category {
				t.Fatalf("got %s/%s %s/%s %s, want %s/%s %s/%s %s", e.Kind, e.How, e.Label, e.Source, e.Category,
					tc.kind, tc.how, tc.label, tc.source, tc.category)
			}
			if e.ClaimID != "c1" || e.Ref != "head" || e.At != "2026-11-27T09:00:00Z" {
				t.Fatalf("identity: %+v", e)
			}
			validate(t, sch, e)
		})
	}
}

func TestUnchangedEvidence(t *testing.T) {
	blobs := map[string]string{"main.go": "b1", "pin.go": "p1", "d.go": "x"}
	blobAt := func(p string) (string, bool) { b, ok := blobs[p]; return b, ok }
	cases := []struct {
		name string
		mut  func(*ledger.Claim)
		want bool
	}{
		{"quotes", func(*ledger.Claim) {}, true},
		{"pinned changed", func(c *ledger.Claim) { c.Evidence.Pinned = []ledger.File{{Path: "pin.go", Blob: "p0"}} }, false},
		{"drift file", func(c *ledger.Claim) {
			c.Evidence.Drift = []ledger.Drift{{Files: []ledger.File{{Path: "d.go", Blob: "x"}}}}
		}, true},
		{"removed path back", func(c *ledger.Claim) { c.Evidence.Drift = []ledger.Drift{{RemovedPath: "d.go"}} }, false},
		{"missing path still missing", func(c *ledger.Claim) {
			c.Evidence.PathMissing = []ledger.PathMissing{{Path: "gone.go", Candidates: []string{"gone.go"}}}
		}, true},
		{"bare name", func(c *ledger.Claim) { c.Evidence.PathMissing = []ledger.PathMissing{{Path: "x", Name: "x"}} }, false},
		{"other repository", func(c *ledger.Claim) { c.Evidence.Quotes[0].Repo = "other" }, false},
		{"group target", func(c *ledger.Claim) { c.Target = &ledger.Target{Repo: "t", Ref: "prod"} }, false},
	}
	for _, tc := range cases {
		b := claim("confirmed", "t1", tc.mut)
		if got := unchanged(b, claim("confirmed", "t2"), blobAt); got != tc.want {
			t.Errorf("%s: unchanged = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFromFeedbackAndUnseen(t *testing.T) {
	fb := []factorypr.Feedback{
		{Rule: "README.md:3", Option: factorypr.OptionDesignStale, Category: "contradicted", Evidence: "read"},
		{Rule: "docs/a.md:10", Option: factorypr.OptionCodeWrong, Category: "outdated_wording"},
		{Rule: "docs/b.md:4", Option: factorypr.OptionNotAProblem, Category: "incomplete_list"},
		{Rule: "docs/c.md:1", Option: "something_else", Category: "contradicted"},
	}
	ev := FromFeedback("r", 7, "sha", fb, now)
	if len(ev) != 3 {
		t.Fatalf("want 3 events (unknown option ignored), got %+v", ev)
	}
	want := []struct {
		path  string
		line  int
		label string
	}{{"README.md", 3, LabelTP}, {"docs/a.md", 10, LabelTP}, {"docs/b.md", 4, LabelFP}}
	sch := schema(t)
	for i, w := range want {
		e := ev[i]
		if e.Path != w.path || e.Line != w.line || e.Label != w.label || e.Kind != KindHuman || e.Source != SourcePRTick || e.PR != 7 || e.Ref != "sha" {
			t.Errorf("event %d: %+v", i, e)
		}
		validate(t, sch, e)
	}
	Match(ev, led(claim("contradicted", "t1")))
	if ev[0].ClaimID != "c1" || ev[0].Text == "" || ev[1].ClaimID != "" {
		t.Fatalf("match: %+v", ev[:2])
	}
	if got := Unseen(nil, ev); len(got) != 3 {
		t.Fatalf("first read: %d new", len(got))
	}
	if got := Unseen(ev, FromFeedback("r", 7, "sha2", fb, now.Add(time.Hour))); len(got) != 0 {
		t.Fatalf("second read appended %+v", got)
	}
	if got := Unseen(ev, FromFeedback("r", 8, "sha", fb[:1], now)); len(got) != 1 {
		t.Fatalf("another PR's tick is new: %+v", got)
	}
	dup := FromFeedback("r", 9, "sha", []factorypr.Feedback{fb[0], fb[0]}, now)
	if got := Unseen(nil, dup); len(got) != 1 {
		t.Fatalf("a duplicate within one read: %+v", got)
	}
}

func TestStreamRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "outcomes.jsonl")
	if ev, err := Load(p); err != nil || len(ev) != 0 {
		t.Fatalf("missing stream: %v %v", ev, err)
	}
	a := []Event{{At: "t", Repo: "r", Path: "a.md", Line: 1, Kind: KindReported, How: HowNew}}
	b := []Event{{At: "t", Repo: "r", Path: "b.md", Line: 2, Kind: KindAudit, Label: LabelFP, Source: SourceAudit, ClaimID: "x"}}
	if err := Append(p, a); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, b); err != nil {
		t.Fatal(err)
	}
	ev, err := Load(p)
	if err != nil || len(ev) != 2 || ev[0].Path != "a.md" || ev[1].Label != LabelFP || ev[1].SchemaVersion != SchemaVersion {
		t.Fatalf("got %+v %v", ev, err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
	if _, err := Read(strings.NewReader(`{"schema_version":"2.0","kind":"reported"}`)); err == nil {
		t.Fatal("a 2.x stream must be refused")
	}
}

func TestSampleAndCSVRoundTrip(t *testing.T) {
	l := led()
	for i, cat := range []string{"contradicted", "confirmed", "outdated_wording", "dangling_reference", "incomplete_list", "contradicted"} {
		c := claim(cat, "t1")
		c.ID = string(rune('a' + i))
		c.Line = i + 1
		c.Text = "=cmd|' /C calc'!A0" // a formula a spreadsheet must not run
		c.Facts = []ledger.Fact{{Fact: "X is 2 in main.go", Status: "contradicted", Valid: true}, {Fact: "hidden", Status: "confirmed"}}
		l.Claims = append(l.Claims, *c)
	}
	s1 := Sample(l, map[string]bool{"a": true}, 3, 42)
	s2 := Sample(l, map[string]bool{"a": true}, 3, 42)
	if len(s1) != 3 || !sameIDs(s1, s2) {
		t.Fatalf("not reproducible: %v vs %v", ids(s1), ids(s2))
	}
	for _, c := range s1 {
		if c.ID == "a" || c.ID == "b" {
			t.Fatalf("sampled a labelled or non-problem claim: %v", ids(s1))
		}
	}
	if all := Sample(l, nil, 100, 1); len(all) != 5 {
		t.Fatalf("pool = open problems: %v", ids(all))
	}
	var buf bytes.Buffer
	lines := func(ref, _ string, _ int) (string, bool) { return "\tx := 2 // " + ref, true }
	if err := WriteCSV(&buf, s1, "o/r", lines); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"https://github.com/o/r/blob/ref-t1/README.md#L", "https://github.com/o/r/blob/ref-t1/main.go#L7\nx := 2 // ref-t1",
		"contradicted: X is 2 in main.go", "'=cmd", "human_verdict",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("sheet lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hidden") {
		t.Error("an invalid fact reached the sheet")
	}
	// a person fills in the verdicts: tp, fp and one left empty
	filled := strings.Replace(out, "m,,,README.md", "m,TP,,README.md", 1)
	filled = strings.Replace(filled, "m,,,README.md", "m,fp,looks fine,README.md", 1)
	ev, err := LabelsFromCSV(strings.NewReader("\ufeff"+filled), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 2 || ev[0].Label != LabelTP || ev[1].Label != LabelFP || ev[1].Note != "looks fine" {
		t.Fatalf("labels: %+v", ev)
	}
	if ev[0].ClaimID != s1[0].ID || ev[0].Path != "README.md" || ev[0].Line != s1[0].Line || ev[0].Ref != "ref-t1" ||
		ev[0].Text != "=cmd|' /C calc'!A0" || ev[0].Source != SourceAudit || ev[0].Kind != KindAudit {
		t.Fatalf("round trip: %+v", ev[0])
	}
	sch := schema(t)
	for _, e := range ev {
		validate(t, sch, e)
	}
	if got := Labelled(ev); !got[s1[0].ID] || !got[s1[1].ID] || got[s1[2].ID] {
		t.Fatalf("labelled: %v", got)
	}
	if got := Unseen(ev, ev); len(got) != 0 {
		t.Fatalf("importing the same sheet twice: %+v", got)
	}
	if _, err := LabelsFromCSV(strings.NewReader(strings.Replace(out, "m,,,README.md", "m,maybe,,README.md", 1)), now); err == nil {
		t.Fatal("an unknown verdict must be refused")
	}
}

func audit(repo, id, label string) Event {
	return Event{SchemaVersion: SchemaVersion, Repo: repo, ClaimID: id, Kind: KindAudit, Label: label, Source: SourceAudit}
}

func labels(repo string, tp, fp int) []Event {
	var ev []Event
	for i := range tp + fp {
		l := LabelTP
		if i >= tp {
			l = LabelFP
		}
		ev = append(ev, audit(repo, repo+string(rune('A'+i)), l))
	}
	return ev
}

func TestReport(t *testing.T) {
	var ev []Event
	ev = append(ev, labels("a", 18, 2)...) // 90%: budget kept
	ev = append(ev, labels("b", 6, 6)...)  // 50%: burned
	ev = append(ev, labels("c", 2, 3)...)  // too few labels
	// biased sources: many fp ticks and tp proxies must not move the SLI
	for i := range 30 {
		ev = append(ev, Event{Repo: "a", Kind: KindHuman, Label: LabelFP, Source: SourcePRTick, PR: i, Path: "x.md", Line: 1})
		ev = append(ev, Event{Repo: "b", ClaimID: "p", At: string(rune('a' + i)), Kind: KindResolved, How: HowDocEdited, Label: LabelTP, Source: SourceProxy})
	}
	// a claim audited again: the later label replaces the earlier one
	ev = append(ev, audit("a", "aB", LabelFP), audit("a", "aB", LabelTP), audit("a", "aZ", LabelSkip))
	ev = append(ev,
		Event{Repo: "a", ClaimID: "r1", Kind: KindReported, How: HowNew},
		Event{Repo: "a", ClaimID: "r2", Kind: KindReported, How: HowFlip},
		Event{Repo: "a", ClaimID: "r2", Kind: KindResolved, How: HowFlip},
		Event{Repo: "a", ClaimID: "r3", Kind: KindRejudged, How: HowStable},
		Event{Repo: "a", ClaimID: "r4", Kind: KindRejudged, How: HowStable},
	)
	r := Report(ev, 0, 0)
	if r.Target != DefaultTarget || len(r.Repos) != 3 {
		t.Fatalf("report: %+v", r)
	}
	a, b, c := r.Repos[0], r.Repos[1], r.Repos[2]
	if a.Precision.N != 20 || a.Precision.Bad != 2 || !a.Precision.Met || a.Budget == nil || a.Budget.Burned {
		t.Fatalf("a: %+v %+v", a.Precision, a.Budget)
	}
	lo, hi := *a.Precision.Low, *a.Precision.High
	if math.Abs(lo-0.699) > 0.005 || math.Abs(hi-0.972) > 0.005 {
		t.Fatalf("wilson 18/20 = [%.3f, %.3f]", lo, hi)
	}
	if math.Abs(a.Budget.Remaining-0.5) > 1e-9 {
		t.Fatalf("remaining: %v", a.Budget.Remaining)
	}
	if a.Labels[SourceAudit].Skip != 1 || a.Labels[SourcePRTick].FP != 30 {
		t.Fatalf("labels: %+v", a.Labels)
	}
	if b.Precision.Met || b.Budget == nil || !b.Budget.Burned || b.Labels[SourceProxy].TP != 30 {
		t.Fatalf("b: %+v %+v", b.Precision, b.Budget)
	}
	if c.Budget != nil || c.Precision.Value == nil || !strings.Contains(c.Precision.Note, "not enough labels") {
		t.Fatalf("c: %+v", c.Precision)
	}
	if a.Reported != 2 || a.Open != 1 || a.Resolved[HowFlip] != 1 || a.Flips != 2 || a.Rejudged != 4 {
		t.Fatalf("a outcomes: %+v", a)
	}
	if v := *a.FlipRate.Value; v != 0.5 || a.FlipRate.Met || math.Abs(a.FlipRate.Target-DefaultMaxFlipRate) > 1e-9 {
		t.Fatalf("flip rate: %+v", a.FlipRate)
	}
	if r.Total.Precision.N != 37 || r.Total.Precision.Bad != 11 {
		t.Fatalf("total: %+v", r.Total.Precision)
	}
	var txt, md bytes.Buffer
	r.Text(&txt)
	r.Markdown(&md)
	for _, want := range []string{"burned", "not enough labels", "ok (50% left)", "PR ticks 0 tp / 30 fp"} {
		if !strings.Contains(txt.String(), want) || !strings.Contains(md.String(), want) {
			t.Errorf("rendering lacks %q:\n%s\n%s", want, txt.String(), md.String())
		}
	}
	if !strings.Contains(md.String(), "self-selected") {
		t.Error("markdown must say the supporting signals are biased")
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}

func TestReportEmpty(t *testing.T) {
	r := Report(nil, 0.9, 0.05)
	if r.Total.Precision.Value != nil || r.Total.Budget != nil || r.Total.FlipRate.Value != nil || r.Target != 0.9 || r.MaxFlipRate != 0.05 {
		t.Fatalf("empty: %+v", r.Total)
	}
}

func schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.Compile(contracts.Outcome)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, s *jsonschema.Schema, e Event) {
	t.Helper()
	e.SchemaVersion = SchemaVersion
	b, _ := json.Marshal(e)
	if errs := s.ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("outcome/v1: %v\n%s", errs, b)
	}
}

func ids(cs []ledger.Claim) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func sameIDs(a, b []ledger.Claim) bool {
	return strings.Join(ids(a), ",") == strings.Join(ids(b), ",")
}
