package evalmodel

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/judgev1"
)

// agent is an in-process agentic provider with canned structured answers.
type agent struct{}

func (agent) Name() string    { return "agent" }
func (agent) Model() string   { return "m" }
func (agent) Cloud() bool     { return true }
func (agent) Billing() string { return judgev1.BillingAPI }
func (agent) Close() error    { return nil }
func (agent) Modes() []string { return []string{judgev1.ModeAgentic} }
func (agent) Judge(_ context.Context, r *judgev1.Request) (*judgev1.Response, error) {
	if r.Task.Type == judgev1.TaskDocFacts { // answer each line as its own claim_facts request would
		var lines []map[string]any
		for _, c := range r.Claims {
			one := *r
			one.Task.Type, one.Claim = judgev1.TaskClaimFacts, c
			resp, _ := agent{}.Judge(context.Background(), &one)
			var m map[string]any
			_ = json.Unmarshal(resp.Answer, &m)
			m["line"] = c.Line
			lines = append(lines, m)
		}
		b, _ := json.Marshal(map[string]any{"lines": lines})
		return judgev1.NewStructuredResponse(r, b, judgev1.ParseSchema, judgev1.Model{Provider: "agent", ID: "m"}, judgev1.Usage{}), nil
	}
	var a any
	quote := func(status, p string, line int, q string) map[string]any {
		return map[string]any{"facts": []any{map[string]any{"fact": "f", "status": status, "evidence": []any{
			map[string]any{"kind": "quote", "path": p, "line": line, "quote": q},
		}}}, "reason": "r"}
	}
	switch {
	case r.Task.Type == judgev1.TaskRepoDrift:
		a = map[string]any{"drift": []any{map[string]any{
			"kind": "changed_value", "old": "port 8080", "new": "port 9090", "summary": "s",
			"evidence": []any{map[string]any{"path": "main.go", "line": 2, "quote": "const port = 9090"}},
		}}}
	case r.Claim.Line == 1:
		a = quote(agentic.Contradicted, "main.go", 2, "const port = 9090")
	case r.Claim.Line == 2:
		a = quote(agentic.Confirmed, "main.go", 1, "package main")
	default:
		a = map[string]any{"facts": []any{map[string]any{"fact": "f", "status": agentic.UnverifiableInRepo, "evidence": []any{}}}, "reason": "r"}
	}
	b, _ := json.Marshal(a)
	return judgev1.NewStructuredResponse(r, b, judgev1.ParseSchema, judgev1.Model{Provider: "agent", ID: "m"}, judgev1.Usage{InputTokens: 1000, OutputTokens: 100, CostUSD: 0.04}), nil
}

func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for p, c := range map[string]string{"README.md": "The port is 8080.\nThe code is in Go.\nIt runs on the lab machine.\n", "main.go": "package main\nconst port = 9090\n"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "c"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	return dir, strings.TrimSpace(string(out))
}

func TestRunAgentic(t *testing.T) {
	dir, head := gitRepo(t)
	mk := func(id, repo, rev, expected string, line int) Item {
		return Item{
			ID: id, Source: SourceGTPositive, Label: LabelStrongAI, Repo: repo, Split: "test", Revision: rev, Task: TaskJudge,
			TaskType: judgev1.TaskClaimFacts, Expected: expected, Claim: judgev1.Claim{Repo: repo, Path: "README.md", Line: line, Text: "x"},
		}
	}
	items := []Item{
		mk("p1", "demo", head, judgev1.Yes, 1), mk("n1", "demo", head[:10], judgev1.No, 2), mk("n2", "demo", "HEAD", judgev1.No, 3),
		mk("x1", "other", "", judgev1.Yes, 1),
	}
	p := filepath.Join(t.TempDir(), "set.jsonl")
	if _, err := WriteSet(p, items); err != nil {
		t.Fatal(err)
	}
	set, err := LoadSet(p)
	if err != nil {
		t.Fatal(err)
	}
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {demo: public}}\n"))
	for _, group := range []string{agentic.GroupLine, agentic.GroupDoc} {
		res := Run(context.Background(), Options{
			Set: set, Task: TaskJudge, Guard: guard(t, agent{}), Concurrency: 2,
			Agentic: &Agentic{Worktrees: map[string]string{"demo": dir}, Class: in.Classification, Settings: agentic.Settings{Group: group}},
		})
		s := res.Scores
		if res.Mode != "agentic" || res.Group != group || res.PromptTemplateHash != agentic.TemplateHash() || len(res.Drift) != 1 || res.Drift[0].Validated != 1 {
			t.Fatalf("%s: result %+v", group, res)
		}
		if s.TruePositives != 1 || s.TrueNegatives != 2 || s.FalseNegatives != 1 || s.Errors != 1 {
			t.Fatalf("%s: scores %+v", group, s)
		}
		if s.Categories[agentic.Contradicted] != 1 || s.Categories[agentic.Confirmed] != 1 || s.Categories[agentic.UnverifiableInRepo] != 1 {
			t.Fatalf("%s: categories %+v", group, s.Categories)
		}
		// unverifiable_in_repo is not unknown; the item without a worktree is
		if *s.UnknownRate != 0.25 || *s.UnverifiableRate != 0.25 || *s.EvidenceRejectionRate != 0 {
			t.Fatalf("%s: rates unknown %v unverifiable %v", group, *s.UnknownRate, *s.UnverifiableRate)
		}
		if !strings.Contains(res.Items[3].Error, "no worktree for repository other") || len(res.Items[0].Facts) != 1 {
			t.Fatalf("%s: items %+v", group, res.Items)
		}
		b, _ := json.Marshal(res)
		if errs := evalSchema(t).ValidateJSON(b); len(errs) > 0 {
			t.Fatalf("%s: result violates eval/v1: %v", group, errs)
		}
	}
	// a checkout at another revision is refused
	items[0].Revision = strings.Repeat("a", 40)
	if _, err := WriteSet(p, items); err != nil {
		t.Fatal(err)
	}
	set, _ = LoadSet(p)
	res := Run(context.Background(), Options{Set: set, Task: TaskJudge, Guard: guard(t, agent{}), Agentic: &Agentic{Worktrees: map[string]string{"demo": dir}}})
	if !strings.Contains(res.Items[0].Error, "wants "+strings.Repeat("a", 40)) {
		t.Fatalf("revision mismatch not reported: %+v", res.Items[0])
	}
	// claim_facts items need the agentic mode
	res = Run(context.Background(), Options{Set: set, Task: TaskJudge, Guard: guard(t, &oracle{})})
	if !strings.Contains(res.Items[0].Error, "agentic mode") {
		t.Fatalf("%+v", res.Items[0])
	}
}

func evalSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.Compile(contracts.EvalResult)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
