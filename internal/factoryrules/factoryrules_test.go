package factoryrules

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/consistency"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

// recorder answers every claim with no facts and keeps the prompts.
type recorder struct {
	mu    sync.Mutex
	users []string
}

func (*recorder) Name() string    { return "rec" }
func (*recorder) Model() string   { return "m" }
func (*recorder) Cloud() bool     { return true }
func (*recorder) Billing() string { return judgev1.BillingAPI }
func (*recorder) Close() error    { return nil }
func (*recorder) Modes() []string { return []string{judgev1.ModeAgentic} }
func (r *recorder) Judge(_ context.Context, q *judgev1.Request) (*judgev1.Response, error) {
	a := `{"facts":[],"reason":""}`
	if q.Task.Type == judgev1.TaskRepoDrift {
		a = `{"drift":[]}`
	} else {
		r.mu.Lock()
		r.users = append(r.users, q.Prompt.User)
		r.mu.Unlock()
	}
	return judgev1.NewStructuredResponse(q, json.RawMessage(a), judgev1.ParseSchema, judgev1.Model{Provider: "rec", ID: "m"}, judgev1.Usage{}), nil
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for p, s := range map[string]string{
		"design/a/Calc.yaml":               "名前: Calc\n业务规则:\n  - 情况: 计数\n    结果: 按 status 数\n",
		"internal/handler/a/calc_logic.go": "package a\n\n// count by status\nfunc Count() {}\n",
	} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	return dir
}

func manifest(t *testing.T, line int) *factorymanifest.Manifest {
	t.Helper()
	m, err := factorymanifest.Parse([]byte(`{"schema": "factory.api-manifest/v1", "project": ".", "apis": [
	  {"id": "a.calc", "name": "Calc", "domain": "a", "source": {"path": "design/a/Calc.yaml"},
	   "rules": [{"path": "design/a/Calc.yaml", "line": ` + strconv.Itoa(line) + `, "when": "计数", "then": "按 status 数"}],
	   "escape": [{"path": "internal/handler/a/calc_logic.go", "kind": "logic", "exists": true},
	              {"path": "internal/handler/a/calc_logic_test.go", "kind": "test", "exists": true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The rule is judged once with its hand-written file as a hint, pinned to that file, and
// left alone on the next run; a manifest of another revision is refused.
func TestJudgeAndPin(t *testing.T) {
	ctx := context.Background()
	dir := repo(t)
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion}
	b, err := ledger.NewBuilder(ctx, dir, "af", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p, err := MakePlan(ctx, manifest(t, 3), l, "af", b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Judge != 1 || p.Items[0].Reason != ReasonNew || p.Items[0].Text != "  - 情况: 计数" {
		t.Fatalf("plan %+v", p)
	}
	rec := &recorder{}
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {af: public}}\n"))
	g, err := judge.NewGuard("judge", rec, in)
	if err != nil {
		t.Fatal(err)
	}
	run := agentic.New(agentic.Options{Guard: g, Repo: "af", Dir: dir, Class: in.Classification, Hint: p.Hint()})
	res := Judge(ctx, run, b, l, p)
	if res.Judged != 1 || res.Failed != 0 {
		t.Fatalf("result %+v", res)
	}
	if len(rec.users) != 1 || !strings.Contains(rec.users[0], "implemented by hand-written code in: internal/handler/a/calc_logic.go\n") {
		t.Errorf("prompt lacks the hint:\n%v", rec.users)
	}
	c := l.Claims[0]
	if len(c.Evidence.Pinned) != 1 || c.Evidence.Pinned[0].Path != "internal/handler/a/calc_logic.go" {
		t.Errorf("pinned %+v (the test file is not the implementation)", c.Evidence.Pinned)
	}
	// The per-commit recheck (re-opened by a change to the pinned file) keeps the pin.
	run = agentic.New(agentic.Options{Guard: g, Repo: "af", Dir: dir, Class: in.Classification})
	if r := consistency.Recheck(ctx, run, b, l, []consistency.Pick{{ID: c.ID, Path: c.Path, Line: c.Line, Reason: ledger.ReasonPinnedChanged}}); r.Judged != 1 {
		t.Fatalf("recheck %+v", r)
	}
	if len(l.Claims) != 1 || len(l.Claims[0].Evidence.Pinned) != 1 {
		t.Errorf("after recheck: %+v, want the pin kept", l.Claims)
	}
	if p, err = MakePlan(ctx, manifest(t, 3), l, "af", b); err != nil || p.Judge != 0 || p.Items[0].Reason != ReasonCurrent {
		t.Errorf("second run: %+v %v, want nothing to judge", p, err)
	}
	if _, err := MakePlan(ctx, manifest(t, 4), l, "af", b); err == nil || !strings.Contains(err.Error(), "same commit") {
		t.Errorf("rule on the wrong line: %v, want a revision mismatch", err)
	}
}
