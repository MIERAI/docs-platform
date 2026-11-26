package factorypr

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
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
    - 说明: 300.5 秒算违约
      结果: { sla: 1 }
`

// model: the SLA rule is contradicted by the head's threshold; the count rule holds.
type model struct{}

func (model) Name() string    { return "fake" }
func (model) Model() string   { return "m" }
func (model) Cloud() bool     { return true }
func (model) Billing() string { return judgev1.BillingAPI }
func (model) Close() error    { return nil }
func (model) Modes() []string { return []string{judgev1.ModeSnippet, judgev1.ModeAgentic} }
func (model) Judge(_ context.Context, q *judgev1.Request) (*judgev1.Response, error) {
	a := `{"facts":[],"reason":""}`
	switch {
	case q.Task.Type == judgev1.TaskRepoDrift:
		a = `{"drift":[]}`
	case q.Task.Type == judgev1.TaskClaimFacts && q.Claim.Line == 3:
		a = `{"facts":[{"fact":"超过 300 秒算违约","status":"contradicted","evidence":[{"kind":"quote","path":"logic.go","line":3,"quote":"const sla = 600.0"}]}],"reason":"threshold is 600"}`
	case q.Task.Type == judgev1.TaskClaimFacts:
		a = `{"facts":[{"fact":"按 status 数","status":"confirmed","evidence":[{"kind":"quote","path":"logic.go","line":5,"quote":"func count(n int) int { return n }"}]}],"reason":""}`
	case q.Task.Type == judgev1.TaskDraftRule:
		a = `{"edits":[{"old":"    结果: 超过 300 秒算违约","new":"    结果: 超过 600 秒算违约"},{"old":"    - 说明: 300.5 秒算违约\n      结果: { sla: 1 }","new":"    - 说明: 300.5 秒不算违约\n      结果: { sla: 0 }"}],"rationale":"the code uses 600"}`
	}
	return judgev1.NewStructuredResponse(q, json.RawMessage(a), judgev1.ParseSchema, judgev1.Model{Provider: "fake", ID: "m"}, judgev1.Usage{}), nil
}

// factory: the design's example expects the 300-second threshold; the head's code uses 600.
type factory struct{ calls int }

func (f *factory) Try(_ context.Context, files []factorytry.File, _ []string) (*factorytry.Result, error) {
	f.calls++
	d := design
	for _, x := range files {
		d = x.Content
	}
	ok := strings.Contains(d, "sla: 0")
	return &factorytry.Result{
		Schema: factorytry.ResultSchema, Status: factorytry.StatusOK, Stage: "accept",
		APIs: []factorytry.API{{ID: "a.calc", Cases: []factorytry.Case{
			{Name: "300.5 秒", Source: factorytry.SourceExample, Passed: ok},
			{Name: "hand", Source: factorytry.SourceHandTest}, // the code's own test, stale after the change: not the draft's business
		}}},
	}, nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReviewDraftsAVerifiedDesignEdit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, map[string]string{"design.yaml": design, "logic.go": "package a\n\nconst sla = 300.0\n\nfunc count(n int) int { return n }\n", "README.md": "x\n"})
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "base")
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, map[string]string{"logic.go": "package a\n\nconst sla = 600.0\n\nfunc count(n int) int { return n }\n"})
	git(t, dir, "commit", "-qam", "raise the SLA")

	m, err := factorymanifest.Parse([]byte(`{"schema": "factory.api-manifest/v1", "project": ".", "apis": [
	  {"id": "a.calc", "name": "Calc", "domain": "a", "source": {"path": "design.yaml"},
	   "rules": [{"path": "design.yaml", "line": 3, "when": "SLA"}, {"path": "design.yaml", "line": 5, "when": "计数"}],
	   "escape": [{"path": "logic.go", "kind": "logic", "exists": true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := Touched(m, map[string]bool{"README.md": true}); len(got) != 0 {
		t.Errorf("a README change touches no rule: %v", got)
	}
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {af: public}}\n"))
	g, err := judge.NewGuard("judge", model{}, in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ledger.NewBuilder(ctx, dir, "af", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	f := &factory{}
	rep, err := Review(ctx, Options{
		Repo: "af", Dir: dir, Base: base, Manifest: m, Builder: b, Draft: g, Try: f,
		Run: agentic.New(agentic.Options{Guard: g, Repo: "af", Dir: dir, Class: in.Classification, NoDrift: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rules) != 2 || rep.Problems() != 1 {
		t.Fatalf("rules %+v", rep.Rules)
	}
	sla := rep.Rules[0]
	if sla.Category != agentic.Contradicted || len(sla.Quotes) != 1 || sla.Touched[0] != "logic.go" {
		t.Errorf("SLA rule %+v", sla)
	}
	if d := sla.Draft; d == nil || !d.Verified || len(d.Edits) != 2 {
		t.Errorf("draft %+v, want verified by the factory", sla.Draft)
	}
	if rep.Rules[1].Draft != nil {
		t.Error("a rule that holds gets no draft")
	}
	if rep.Acceptance["a.calc"] == "" {
		t.Error("the head as it is fails the design's example: acceptance must say so")
	}
	md := rep.Markdown()
	for _, s := range []string{Marker, "❌ 代码和规则矛盾", "✅ 工厂验证通过", "- " + "    结果: 超过 300 秒算违约", "+ " + "    结果: 超过 600 秒算违约", "依据: `logic.go:3`"} {
		if !strings.Contains(md, s) {
			t.Errorf("comment lacks %q:\n%s", s, md)
		}
	}
	if (&Report{}).Markdown() != "" {
		t.Error("no touched rule: no comment")
	}
}
