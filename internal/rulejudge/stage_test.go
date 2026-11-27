package rulejudge_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/rulejudge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// countingJudge answers every agentic request "unverifiable" and counts the requests: the
// lines the rules decide must never reach it.
type countingJudge struct{ n atomic.Int32 }

func (*countingJudge) Name() string    { return "counting" }
func (*countingJudge) Model() string   { return "m" }
func (*countingJudge) Cloud() bool     { return false }
func (*countingJudge) Billing() string { return judgev1.BillingNone }
func (*countingJudge) Close() error    { return nil }
func (*countingJudge) Modes() []string { return []string{judgev1.ModeSnippet, judgev1.ModeAgentic} }
func (j *countingJudge) Judge(_ context.Context, r *judgev1.Request) (*judgev1.Response, error) {
	j.n.Add(1)
	obj, parse := judgev1.ParseObject(nil, `{"facts":[{"fact":"something","status":"unverifiable_in_repo","evidence":[]}],"reason":"model"}`)
	return judgev1.NewStructuredResponse(r, obj, parse, judgev1.Model{Provider: "counting", ID: "m"}, judgev1.Usage{}), nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// fixtureRepo copies testdata/fixture into a fresh git repository.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("testdata", "fixture")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o750); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "fixture")
	return dir
}

// TestStageBeforeTheModel runs the stage as the agentic runner's first step: decided lines
// never reach the model, their evidence is validated like a model's, the ledger records them
// as rules verdicts with blob-addressed quotes, and a change of the quoted code invalidates
// them deterministically.
func TestStageBeforeTheModel(t *testing.T) {
	ctx := context.Background()
	dir := fixtureRepo(t)
	st, err := rulejudge.LoadDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	cj := &countingJudge{}
	g, err := judge.NewGuard("judge", cj, &instance.Instance{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	run := agentic.New(agentic.Options{Guard: g, Repo: "fx", Dir: dir, NoDrift: true, Rules: st.Decider()})
	// 7 port (confirmed), 8 workers (contradicted), 12 says more than the fact, 14 qualified
	// (CI), 30 route method (contradicted), 31 route with prose.
	res := run.Judge(ctx, "README.md", []int{7, 8, 12, 14, 30, 31})
	if got := cj.n.Load(); got != 3 {
		t.Errorf("model requests = %d, want 3 (lines 12, 14, 31)", got)
	}
	want := map[int]string{7: agentic.Confirmed, 8: agentic.Contradicted, 30: agentic.Contradicted}
	b, err := ledger.NewBuilder(ctx, dir, "fx", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	b.Model = "counting" // what the audit sets: must not overwrite a rules verdict's marker
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion}
	for i := range res {
		lr := &res[i]
		cat, ruled := want[lr.Line]
		if ruled != agentic.IsRules(lr.Model) || ruled && (lr.Category != cat || lr.EvidenceOK == 0) {
			t.Errorf("line %d: model %q category %q evidence %d/%d (%s)", lr.Line, lr.Model, lr.Category, lr.EvidenceOK, lr.Evidence, lr.Reason)
		}
		c, ok := b.Claim(ctx, lr)
		if !ok {
			t.Fatalf("line %d: no claim", lr.Line)
		}
		if ruled && (c.Model != rulejudge.Model || c.PromptHash != rulejudge.PromptHash() || len(c.Evidence.Quotes) == 0 || c.Evidence.Quotes[0].Blob == "" || c.Evidence.Quotes[0].Scope == nil) {
			t.Errorf("line %d: ledger claim %+v", lr.Line, c)
		}
		if !ruled && c.Model != "counting" {
			t.Errorf("line %d: a model verdict recorded as %q", lr.Line, c.Model)
		}
		l.Merge(c)
	}

	// The quoted code changes: exactly the claims resting on main.go become stale.
	p := filepath.Join(dir, "cmd", "server", "main.go")
	src, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(src), `"number of worker goroutines")`, `"worker goroutines")`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qam", "reword usage")
	rep, err := ledger.Check(ctx, l, dir, "fx", "HEAD", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Claims {
		if _, ruled := want[s.Line]; ruled && s.Status != ledger.Stale {
			t.Errorf("line %d: %s after its quoted code changed, want stale", s.Line, s.Status)
		}
	}
}

func TestDraftIsATokenReplacement(t *testing.T) {
	dir := fixtureRepo(t)
	st, err := rulejudge.LoadDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]string{
		"| `-workers N` | number of worker goroutines (default 4) |": "| `-workers N` | number of worker goroutines (default 8) |",
		"- `SERVER_REGION` defaults to `us-east-1`.":                 "- `SERVER_REGION` defaults to `eu-west-1`.",
		"- `PUT /v1/orders`":         "- `POST /v1/orders`",
		"`batch_size` (default: 50)": "`batch_size` (default: 100)",
	} {
		d, ok := st.Decide("README.md", line)
		if !ok || d.Draft != want {
			t.Errorf("Decide(%q): ok %v draft %q, want %q", line, ok, d.Draft, want)
		}
	}
}
