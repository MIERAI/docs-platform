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
	"github.com/MIERAI/docs-platform/internal/factoryai"
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
		a = `{"edits":[{"old":"    结果: 超过 300 秒算违约","new":"    结果: 超过 600 秒算违约"},{"old":"    - 说明: 300.5 秒算违约\n      结果: { sla: 1 }","new":"    - 说明: 300.5 秒不算违约\n      结果: { sla: 0 }"}],"rationale":"the code uses 600","summary":"设计书写 300 秒,代码是 600 秒","question":"改成 600 秒是有意的吗?","counterexample":"400 秒: 设计书说违约,代码说不违约"}`
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
			{Name: "300.5 秒", Source: factorytry.SourceExample, Passed: ok, Detail: "response.sla: want 1, got <nil> (body {})"},
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
	if sla.Evidence != EvidenceExecuted || len(rep.Failing["a.calc"]) != 1 || len(sla.Code) != 1 || sla.Code[0].Text != "const sla = 600.0" {
		t.Errorf("evidence %q, failing %v, code %v: want the executed example and the cited code line", sla.Evidence, rep.Failing, sla.Code)
	}
	if !strings.Contains(rep.Diffs["logic.go"], "+const sla = 600.0") {
		t.Errorf("diff %q", rep.Diffs["logic.go"])
	}
	if p := sla.Draft.Patch; !strings.HasPrefix(p, "--- a/design.yaml\n+++ b/design.yaml\n@@") || !strings.Contains(p, "+    结果: 超过 600 秒算违约") {
		t.Errorf("patch %q", p)
	}
	rep.GitHub = "o/af"
	md := rep.Markdown()
	for _, s := range []string{
		Marker, "❌ 代码和规则矛盾", "✅ 工厂验证通过", "**一句话**:设计书写 300 秒,代码是 600 秒", "**只需要回答**:改成 600 秒是有意的吗?",
		"`response.sla` 设计书预期 **1**,代码实际是 **没有这个值(0 / 空)**", "https://github.com/o/af/blob/" + rep.Head + "/logic.go#L3", "+    结果: 超过 600 秒算违约",
		"- [ ] 代码改错了",
	} {
		if !strings.Contains(md, s) {
			t.Errorf("comment lacks %q:\n%s", s, md)
		}
	}
	if strings.Contains(md, OptionNotAProblem) {
		t.Error("an executed counterexample offers no 'not a problem'")
	}
	// A ticked box is read back and kept; a tick on another verdict is not.
	body := strings.Replace(md, "- [ ] 代码改错了", "- [x] 代码改错了", 1)
	fb := Ticked(body)
	if len(fb) != 1 || fb[0] != (Feedback{Rule: "design.yaml:3", Option: OptionCodeWrong, Category: agentic.Contradicted, Evidence: EvidenceExecuted}) {
		t.Fatalf("ticked %+v", fb)
	}
	if !strings.Contains(rep.MarkdownWith(TickedKeys(fb)), "- [x] 代码改错了") {
		t.Error("the tick is lost on update")
	}
	rep.Rules[0].Category = agentic.OutdatedWording
	if strings.Contains(rep.MarkdownWith(TickedKeys(fb)), "- [x]") {
		t.Error("a tick on a verdict that changed is carried over")
	}
	if (&Report{}).Markdown() != "" {
		t.Error("no touched rule: no comment")
	}
}

// Failing examples prove the API's design and code disagree, not which rule is stale: a rule
// whose verified draft rewrites another rule is only read, and can be ticked away.
func TestExecutedOnlyForTheRuleTheDraftRewrites(t *testing.T) {
	sla := "  - 情况: SLA\n    结果: 超过 300 秒算违约"
	health := "  - 情况: 健康度\n    结果: 有违约 → DEGRADED"
	d := &Draft{Verified: true, Edits: []factoryai.Edit{{Path: "design.yaml", Old: "    结果: 超过 300 秒算违约", New: "    结果: 超过 600 秒算违约"}}, Patch: "--- a/design.yaml\n+++ b/design.yaml\n@@ -1 +1 @@\n-x\n+y\n"}
	if !draftRewrites(d, sla) || draftRewrites(d, health) {
		t.Fatal("the draft rewrites the SLA rule only")
	}
	if draftRewrites(&Draft{Edits: d.Edits}, sla) {
		t.Error("an unverified draft proves nothing")
	}
	rep := &Report{Head: "h", Failing: map[string][]factorytry.Case{"a": {{Name: "mu", Detail: "response.health: want DEGRADED, got OK"}}}, Rules: []Rule{
		{API: "a", Path: "design.yaml", Line: 3, Text: "  - 情况: SLA", Block: sla, Category: agentic.Contradicted, Evidence: EvidenceExecuted, Draft: d},
		{API: "a", Path: "design.yaml", Line: 5, Text: "  - 情况: 健康度", Block: health, Category: agentic.Contradicted, Evidence: EvidenceRead, Draft: d},
	}}
	md := rep.Markdown()
	if strings.Count(md, "--- a/design.yaml") != 1 || !strings.Contains(md, "和上面 `design.yaml:3` 的改稿相同") {
		t.Errorf("the same draft is shown twice:\n%s", md)
	}
	if !strings.Contains(md, "design.yaml:5 not_a_problem") || strings.Contains(md, "design.yaml:3 not_a_problem") {
		t.Errorf("only the read rule can be ticked away:\n%s", md)
	}
	if !strings.Contains(md, "结果: 有违约 → DEGRADED") {
		t.Error("the design side shows the whole rule")
	}
}
