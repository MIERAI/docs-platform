package evalmodel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// oracle answers yes when the evidence holds "9090" (a perfect judge for the fixture set),
// with a quote; "lie" items get a fabricated quote.
type oracle struct {
	mu sync.Mutex
	n  int
}

func (*oracle) Name() string    { return "oracle" }
func (*oracle) Model() string   { return "claude-sonnet-5-5" }
func (*oracle) Cloud() bool     { return true }
func (*oracle) Billing() string { return judgev1.BillingAPI }
func (*oracle) Close() error    { return nil }
func (*oracle) Modes() []string { return []string{judgev1.ModeSnippet} }
func (o *oracle) calls() int    { o.mu.Lock(); defer o.mu.Unlock(); return o.n }
func (o *oracle) Judge(_ context.Context, r *judgev1.Request) (*judgev1.Response, error) {
	o.mu.Lock()
	o.n++
	o.mu.Unlock()
	a := judgev1.Answer{Verdict: judgev1.No}
	switch {
	case r.Task.Type == judgev1.TaskDraftEdit:
		a.Verdict, a.Draft = judgev1.Yes, "The port is 9090 (`const port`), see main.go and `cmd/serve.go`."
	case strings.Contains(r.Evidence[0].Text, "9090") && strings.Contains(r.Claim.Text, "lie"):
		a = judgev1.Answer{Verdict: judgev1.Yes, Quote: "const port = 7777"}
	case strings.Contains(r.Evidence[0].Text, "9090"):
		a = judgev1.Answer{Verdict: judgev1.Yes, Quote: "const port = 9090"}
	}
	return judgev1.NewResponse(r, a, judgev1.Model{Provider: "oracle", ID: "claude-sonnet-5-5"}, judgev1.Usage{InputTokens: 1000, OutputTokens: 100}), nil
}

func item(id, task, expected, claimText, code string) Item {
	tt := judgev1.TaskClaimContradiction
	if task == TaskDraft {
		tt = judgev1.TaskDraftEdit
	}
	return Item{
		ID: id, Source: SourceGTPositive, Label: LabelStrongAI, Repo: "demo", Split: "test", Task: task, TaskType: tt, Expected: expected,
		Claim:        judgev1.Claim{Repo: "demo", Path: "README.md", Line: 3, Text: claimText},
		Evidence:     []judgev1.Evidence{{Repo: "demo", Path: "main.go", From: 1, To: 2, Text: "package main\n" + code}},
		ReferenceFix: "port is 9090",
	}
}

func fixture(t *testing.T) *Set {
	t.Helper()
	items := []Item{
		item("p1", TaskJudge, judgev1.Yes, "The port is 8080", "const port = 9090"),
		item("p2", TaskJudge, judgev1.Yes, "lie: the port is 8080", "const port = 9090"),
		item("n1", TaskJudge, judgev1.No, "The port is 8080", "const port = 8080"),
		item("d1", TaskDraft, "", "The port is 8080", "const port = 9090"),
	}
	p := filepath.Join(t.TempDir(), "set.jsonl")
	if _, err := WriteSet(p, items); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSet(p)
	if err != nil || len(s.Items) != 4 || len(s.SHA256) != 64 {
		t.Fatalf("%v %+v", err, s)
	}
	return s
}

func guard(t *testing.T, p judge.Provider) *judge.Guard {
	t.Helper()
	in, err := instance.Parse([]byte("version: 1\nclassification: {repos: {demo: public}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := judge.NewGuard("judge", p, in)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRunJudgeScores(t *testing.T) {
	set := fixture(t)
	o := &oracle{}
	res := Run(context.Background(), Options{Set: set, Task: TaskJudge, Guard: guard(t, o), Thresholds: instance.Defaults().Eval})
	s := res.Scores
	if s.Items != 3 || s.TruePositives != 1 || s.FalseNegatives != 1 || s.TrueNegatives != 1 || s.FalsePositives != 0 {
		t.Fatalf("scores %+v", s)
	}
	if *s.Precision != 1 || *s.Recall != 0.5 || *s.QuoteRejectionRate != 0.5 || s.CostUSD <= 0 || !strings.Contains(s.Note, "AGREEMENT") {
		t.Fatalf("rates %+v", s)
	}
	if res.Switch == nil || res.Switch.Qualifies || !strings.Contains(strings.Join(res.Switch.Failed, ";"), "quote_rejection_rate") {
		t.Fatalf("switch %+v", res.Switch)
	}
	if res.Set.SHA256 != set.SHA256 || res.PromptTemplateHash != judge.TemplateHash() {
		t.Fatal("result does not identify the frozen set and prompt")
	}
}

func TestRunDraftAndDryRun(t *testing.T) {
	set := fixture(t)
	o := &oracle{}
	res := Run(context.Background(), Options{Set: set, Task: TaskDraft, Guard: guard(t, o)})
	it := res.Items[0]
	if it.Draft == "" || len(it.Unsupported) != 1 || it.Unsupported[0] != "cmd/serve.go" || it.Similarity == nil || *it.Similarity <= 0 {
		t.Fatalf("draft item %+v", it)
	}
	if *res.Scores.SupportedRate != 0 {
		t.Fatalf("draft with an invented path counted as supported: %+v", res.Scores)
	}
	before := o.calls()
	dry := Run(context.Background(), Options{Set: set, Task: TaskJudge, Guard: guard(t, o), DryRun: true})
	if o.calls() != before || !dry.Scores.Estimated || dry.Scores.InputTokens == 0 || dry.Switch != nil {
		t.Fatalf("dry run called the model or scored: %+v", dry.Scores)
	}
}

func TestDecide(t *testing.T) {
	p, r := 0.9, 0.4
	sw := decide(TaskJudge, Scores{Precision: &p, Recall: &r, Errors: 1}, instance.Eval{Judge: instance.JudgeThresholds{MinPrecision: 0.8, MinRecall: 0.5}})
	if sw.Qualifies || len(sw.Failed) != 2 {
		t.Fatalf("%+v", sw)
	}
}

func TestSimilarity(t *testing.T) {
	tests := []struct {
		a, b string
		want float64
	}{{"", "x", 0}, {"port 9090", "port 9090", 1}, {"状态 完成", "状态", 0.5}}
	for _, tt := range tests {
		if got := Similarity(tt.a, tt.b); got != tt.want {
			t.Errorf("Similarity(%q, %q) = %v", tt.a, tt.b, got)
		}
	}
}

func TestParseSetErrors(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"json", "{", "line 1"},
		{"no expected", `{"id":"a","task":"judge","task_type":"claim_contradiction","claim":{"path":"a","text":"b"}}`, "expected yes or no"},
		{"bad task", `{"id":"a","task":"x","task_type":"claim_contradiction","claim":{"path":"a","text":"b"}}`, "want judge or draft"},
		{"dup", `{"id":"a","task":"draft","task_type":"draft_edit","claim":{"path":"a","text":"b"}}` + "\n" + `{"id":"a","task":"draft","task_type":"draft_edit","claim":{"path":"a","text":"b"}}`, "duplicate"},
	}
	for _, tt := range tests {
		if _, err := ParseSet([]byte(tt.in)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if _, err := LoadSet(filepath.Join(os.TempDir(), "does-not-exist.jsonl")); err == nil {
		t.Error("missing set accepted")
	}
}

func FuzzParseSet(f *testing.F) {
	f.Add([]byte(`{"id":"a","task":"draft","task_type":"draft_edit","claim":{"path":"a","text":"b"}}`))
	f.Add([]byte("{}\n\n{"))
	f.Fuzz(func(t *testing.T, b []byte) {
		items, err := ParseSet(b)
		if err != nil {
			return
		}
		for _, it := range items {
			if it.ID == "" || it.Claim.Text == "" {
				t.Fatal("invalid item accepted")
			}
		}
	})
}
