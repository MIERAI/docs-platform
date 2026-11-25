package check

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/judgev1"
)

// changeJudge says yes, quoting the removed line, and drafts a fix.
type changeJudge struct{ cloud bool }

func (changeJudge) Name() string    { return "fake" }
func (changeJudge) Model() string   { return "m" }
func (c changeJudge) Cloud() bool   { return c.cloud }
func (changeJudge) Billing() string { return judgev1.BillingNone }
func (changeJudge) Close() error    { return nil }
func (changeJudge) Modes() []string { return []string{judgev1.ModeSnippet} }
func (changeJudge) Judge(_ context.Context, r *judgev1.Request) (*judgev1.Response, error) {
	a := judgev1.Answer{Verdict: judgev1.Yes, Quote: `-var port = os.Getenv("APP_PORT")`}
	if r.Task.Type == judgev1.TaskDraftEdit {
		a = judgev1.Answer{Verdict: judgev1.Yes, Draft: "Set `APP_LISTEN`."}
	}
	return judgev1.NewResponse(r, a, judgev1.Model{Provider: "fake", ID: "m"}, judgev1.Usage{}), nil
}

func TestDeriveImpactWithJudgeAndDraft(t *testing.T) {
	dir := t.TempDir()
	for p, c := range map[string]string{
		"README.md":   "# Demo\n\n## Running\n\nSet `APP_PORT` before starting.\n\n## Other\n\nNothing.\n",
		"cmd/tool.go": "package main\n\nimport \"os\"\n\nvar port = os.Getenv(\"APP_LISTEN\")\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	diffs := []repo.FileDiff{{
		Path: "cmd/tool.go", OldPath: "cmd/tool.go",
		Removed: []repo.DiffLine{{N: 5, Text: `var port = os.Getenv("APP_PORT")`}},
		Added:   []repo.DiffLine{{N: 5, Text: `var port = os.Getenv("APP_LISTEN")`}},
	}}
	in, err := instance.Parse([]byte("version: 1\nclassification: {repos: {demo: public}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, verdict, draft string
		cloud                bool
		repoName             string
	}{
		{"local judge", judgev1.Yes, "Set `APP_LISTEN`.", false, "demo"},
		{"cloud judge on private repo is refused", judgev1.Unknown, "", true, "secret"},
	}
	for _, tt := range tests {
		j, _ := judge.NewGuard("judge", changeJudge{cloud: tt.cloud}, in)
		d, _ := judge.NewGuard("draft", changeJudge{cloud: tt.cloud}, in)
		res, err := DeriveImpact(context.Background(), ImpactOptions{Tree: dir, RepoName: tt.repoName, Diffs: diffs, Judge: j, Draft: d})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Sections) != 1 || res.Sections[0].Heading != "Running" || res.Sections[0].Status != impact.SectionReread {
			t.Fatalf("%s: sections %+v", tt.name, res.Sections)
		}
		s := res.Sections[0]
		if s.Judgement == nil || s.Judgement.Verdict != tt.verdict || s.Draft != tt.draft {
			t.Fatalf("%s: judgement %+v draft %q", tt.name, s.Judgement, s.Draft)
		}
		if tt.cloud && !strings.Contains(s.Judgement.Error, "private") {
			t.Errorf("%s: refusal not explained: %+v", tt.name, s.Judgement)
		}
		if res.Total != 3 {
			t.Errorf("%s: total sections %d", tt.name, res.Total)
		}
	}
}
