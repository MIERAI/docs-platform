package rulecover

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

const design = `API: Calc
业务规则:
  - 情况: SLA
    结果: 超过 300 秒算违约
  - 情况: 计数
    结果: 按 status 数
例子:
  用例:
    - 说明: 一件
      结果: { total: 1 }
`

const logic = `package a

const sla = 300.0

func count(n int) int { return n }
`

// model proposes a mutant per rule and, when asked, an example that pins the SLA down.
type model struct {
	mu    sync.Mutex
	tasks []string
}

func (*model) Name() string    { return "fake" }
func (*model) Model() string   { return "m" }
func (*model) Cloud() bool     { return true }
func (*model) Billing() string { return judgev1.BillingAPI }
func (*model) Close() error    { return nil }
func (*model) Modes() []string { return []string{judgev1.ModeSnippet} }
func (m *model) Judge(_ context.Context, q *judgev1.Request) (*judgev1.Response, error) {
	m.mu.Lock()
	m.tasks = append(m.tasks, q.Task.Type)
	m.mu.Unlock()
	var a any
	switch {
	case q.Task.Type == judgev1.TaskRuleMutant && strings.Contains(q.Claim.Text, "SLA"):
		a = map[string]string{"path": "logic.go", "old": "const sla = 300.0", "new": "const sla = 301.0", "rationale": "threshold"}
	case q.Task.Type == judgev1.TaskRuleMutant:
		a = map[string]string{"path": "logic.go", "old": "return n }", "new": "return n + 1 }", "rationale": "off by one"}
	case q.Task.Type == judgev1.TaskDraftExample:
		a = map[string]any{"inserts": []map[string]string{{"anchor": "      结果: { total: 1 }", "text": "    - 说明: 300.5 秒算违约\n      结果: { sla: 1 }"}}, "rationale": "boundary", "equivalent": false}
	}
	b, _ := json.Marshal(a)
	return judgev1.NewStructuredResponse(q, b, judgev1.ParseSchema, judgev1.Model{Provider: "fake", ID: "m"}, judgev1.Usage{}), nil
}

// factory: the count example catches an off-by-one; only the drafted boundary example
// catches the SLA threshold.
type factory struct {
	mu    sync.Mutex
	tries int
}

func (f *factory) Try(_ context.Context, files []factorytry.File, _ []string) (*factorytry.Result, error) {
	f.mu.Lock()
	f.tries++
	f.mu.Unlock()
	d, l := design, logic
	for _, x := range files {
		if x.Path == "logic.go" {
			l = x.Content
		} else {
			d = x.Content
		}
	}
	r := &factorytry.Result{Schema: factorytry.ResultSchema, Status: factorytry.StatusOK, Stage: "accept"}
	cases := []factorytry.Case{{Name: "一件", Source: factorytry.SourceExample, Passed: !strings.Contains(l, "n + 1")}}
	if strings.Contains(d, "300.5 秒算违约") {
		cases = append(cases, factorytry.Case{Name: "300.5 秒算违约", Source: factorytry.SourceExample, Passed: !strings.Contains(l, "301.0")})
	}
	cases = append(cases, factorytry.Case{Name: "hand", Source: factorytry.SourceHandTest, Passed: !strings.Contains(l, "301.0")})
	r.APIs = []factorytry.API{{ID: "a.calc", Cases: cases}}
	return r, nil
}

func setup(t *testing.T) (string, *factorymanifest.Manifest, *judge.Guard, *model) {
	t.Helper()
	dir := t.TempDir()
	for p, s := range map[string]string{"design.yaml": design, "logic.go": logic} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := factorymanifest.Parse([]byte(`{"schema": "factory.api-manifest/v1", "project": ".", "apis": [
	  {"id": "a.calc", "name": "Calc", "domain": "a", "source": {"path": "design.yaml"},
	   "rules": [{"path": "design.yaml", "line": 3, "when": "SLA"}, {"path": "design.yaml", "line": 5, "when": "计数"}],
	   "escape": [{"path": "logic.go", "kind": "logic", "exists": true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {af: public}}\n"))
	md := &model{}
	g, err := judge.NewGuard("judge", md, in)
	if err != nil {
		t.Fatal(err)
	}
	return dir, m, g, md
}

func TestMeasureAndPropose(t *testing.T) {
	dir, m, g, md := setup(t)
	f := &factory{}
	rep, err := Measure(context.Background(), Options{Repo: "af", Dir: dir, Ref: "x", Manifest: m, Guard: g, Try: f, Propose: true, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rules) != 2 {
		t.Fatalf("rules %+v", rep.Rules)
	}
	sla, cnt := rep.Rules[0], rep.Rules[1]
	if sla.Text != "  - 情况: SLA\n    结果: 超过 300 秒算违约" {
		t.Errorf("rule block %q", sla.Text)
	}
	if cnt.Status != Covered || len(cnt.CaughtBy) != 1 {
		t.Errorf("count rule: %+v, want covered by the example", cnt)
	}
	if sla.Status != Uncovered || len(sla.CaughtElsewhere) != 1 || sla.CaughtElsewhere[0] != "hand" {
		t.Errorf("SLA rule: %+v, want uncovered (a hand test is not the design)", sla)
	}
	if p := sla.Proposal; p == nil || !p.Verified || len(p.Catches) != 1 {
		t.Errorf("proposal %+v, want verified: passes on the code, fails on the mutant", sla.Proposal)
	}
	s := rep.Summary
	if s.Measured != 2 || s.Covered != 1 || s.Proposals != 1 || s.Coverage == nil || *s.Coverage != 0.5 {
		t.Errorf("summary %+v", s)
	}
	// baseline + 2 mutants + (example alone, example + mutant)
	if f.tries != 5 {
		t.Errorf("%d tries, want 5", f.tries)
	}
	// Unchanged rule, design and code: nothing is asked or run again.
	n, tries := len(md.tasks), f.tries
	rep2, err := Measure(context.Background(), Options{Repo: "af", Dir: dir, Ref: "y", Manifest: m, Guard: g, Try: f, Propose: true, Previous: rep})
	if err != nil {
		t.Fatal(err)
	}
	if len(md.tasks) != n || f.tries != tries || rep2.Summary.Reused != 2 || *rep2.Summary.Coverage != 0.5 {
		t.Errorf("second run asked %d, tried %d, reused %d", len(md.tasks)-n, f.tries-tries, rep2.Summary.Reused)
	}
}

// A failing baseline makes the API unmeasurable instead of counting every mutant as caught.
func TestBaselineFailing(t *testing.T) {
	dir, m, g, _ := setup(t)
	rep, err := Measure(context.Background(), Options{Repo: "af", Dir: dir, Manifest: m, Guard: g, Try: broken{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Rules {
		if r.Status != BaselineFailing {
			t.Errorf("%+v, want baseline_failing", r)
		}
	}
	if rep.Summary.Coverage != nil {
		t.Error("nothing measured: no coverage")
	}
}

type broken struct{}

func (broken) Try(context.Context, []factorytry.File, []string) (*factorytry.Result, error) {
	return &factorytry.Result{Schema: factorytry.ResultSchema, Status: factorytry.StatusOK, APIs: []factorytry.API{{ID: "a.calc", Cases: []factorytry.Case{{Name: "x", Source: factorytry.SourceExample}}}}}, nil
}

// scripted answers the SLA rule's mutants and examples from queues.
type scripted struct {
	model
	mutants, examples []string
}

func (m *scripted) Judge(_ context.Context, q *judgev1.Request) (*judgev1.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = append(m.tasks, q.Task.Type)
	var a string
	switch {
	case q.Task.Type == judgev1.TaskRuleMutant && !strings.Contains(q.Claim.Text, "SLA"):
		a = `{"path": "", "old": "", "new": "", "rationale": "only the SLA rule here"}`
	case q.Task.Type == judgev1.TaskRuleMutant:
		a, m.mutants = m.mutants[0], m.mutants[1:]
	default:
		a, m.examples = m.examples[0], m.examples[1:]
	}
	return judgev1.NewStructuredResponse(q, json.RawMessage(a), judgev1.ParseSchema, judgev1.Model{Provider: "fake", ID: "m"}, judgev1.Usage{}), nil
}

// An equivalent mutant is replaced; a survivor nobody proves observable is undetermined and
// stays out of the coverage.
func TestEquivalentAndUndetermined(t *testing.T) {
	dir, m, _, _ := setup(t)
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {af: public}}\n"))
	run := func(sm *scripted) Rule {
		t.Helper()
		g, err := judge.NewGuard("judge", sm, in)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := Measure(context.Background(), Options{Repo: "af", Dir: dir, Manifest: m, Guard: g, Try: &factory{}, Propose: true})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Summary.Measured+rep.Summary.Undetermined+1 != rep.Summary.Rules {
			t.Errorf("summary %+v", rep.Summary)
		}
		return rep.Rules[0]
	}
	cosmetic := `{"path": "logic.go", "old": "package a", "new": "package a // x", "rationale": "comment"}`
	threshold := `{"path": "logic.go", "old": "const sla = 300.0", "new": "const sla = 301.0", "rationale": "threshold"}`
	example := `{"inserts": [{"anchor": "      结果: { total: 1 }", "text": "    - 说明: 300.5 秒算违约\n      结果: { sla: 1 }"}], "rationale": "boundary", "equivalent": false}`
	r := run(&scripted{
		mutants:  []string{cosmetic, threshold},
		examples: []string{`{"inserts": [], "rationale": "a comment changes nothing", "equivalent": true}`, example},
	})
	if r.Status != Uncovered || r.Mutant == nil || r.Mutant.New != "const sla = 301.0" || !r.Proposal.Verified {
		t.Errorf("after an equivalent mutant: %+v, want the second mutant, proven by a verified example", r)
	}
	r = run(&scripted{mutants: []string{threshold}, examples: []string{`{"inserts": [], "rationale": "no room", "equivalent": false}`}})
	if r.Status != Undetermined {
		t.Errorf("unproven survivor: %s, want undetermined", r.Status)
	}
}
