package ledger

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
	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
)

type gitRepo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *gitRepo {
	t.Helper()
	g := &gitRepo{t: t, dir: t.TempDir()}
	g.git("init", "-q")
	g.write(map[string]string{
		"README.md":                "# Demo\n\nThe service listens on port 9090.\nThe jobs are build and test.\nSee ENVIRONMENTS.md for hosts.\nThe old script is gone.\nNothing to check here.\n",
		"config.yaml":              "port: 9090\n",
		".github/workflows/ci.yml": "jobs:\n  build: {}\n  test: {}\n",
		"scripts/run.sh":           "exec ./service\n",
		"private/notes.md":         "secret\n",
	})
	g.commit("initial")
	return g
}

func (g *gitRepo) git(args ...string) string {
	g.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", g.dir, "-c", "user.name=t", "-c", "user.email=t@e", "-c", "init.defaultBranch=main"}, args...)...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitRepo) write(files map[string]string) {
	g.t.Helper()
	for p, c := range files {
		full := filepath.Join(g.dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			g.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			g.t.Fatal(err)
		}
	}
}

func (g *gitRepo) commit(msg string) string {
	g.git("add", "-A")
	g.git("commit", "-qm", msg)
	return g.git("rev-parse", "HEAD")
}

func ok(e agentic.Evidence) agentic.EvidenceCheck {
	return agentic.EvidenceCheck{Evidence: e, OK: true}
}

func line(n int, cat string, ev ...agentic.EvidenceCheck) agentic.LineResult {
	lr := agentic.LineResult{Path: "README.md", Line: n, Category: cat, Verdict: agentic.Verdict(cat), Parse: "schema", Model: "m"}
	lr.Facts = []agentic.FactCheck{{Fact: "f", Status: cat, Valid: len(ev) > 0, Evidence: ev}}
	return lr
}

// claims builds one claim per invalidation rule at the repository's HEAD.
func claims(t *testing.T, g *gitRepo) *Ledger {
	t.Helper()
	ctx := context.Background()
	b, err := NewBuilder(ctx, g.dir, "demo", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	b.Now = func() time.Time { return time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC) }
	b.Class = instance.Classification{Default: instance.Public, Paths: []instance.PathClass{{Repo: "demo", Glob: "private/**", Class: instance.Private}}}
	b.Drift = &agentic.DriftList{Entries: []agentic.DriftEntry{{ID: "D1", Kind: "moved", Old: "old/run.sh", AbsentOldPath: true,
		Evidence: []agentic.QuoteRef{{Path: "scripts/run.sh", Line: 1, Quote: "exec ./service"}}}}}
	lrs := []agentic.LineResult{
		line(3, agentic.Confirmed, ok(agentic.Evidence{Kind: agentic.KindQuote, Path: "config.yaml", Line: 1, Quote: "port: 9090"})),
		line(4, agentic.Confirmed, ok(agentic.Evidence{Kind: agentic.KindAbsence, SearchPattern: "deploy", SearchScope: ".github/workflows/*"})),
		line(5, agentic.DanglingReference, ok(agentic.Evidence{Kind: agentic.KindPathMissing, Path: "ENVIRONMENTS.md"})),
		line(6, agentic.Confirmed, ok(agentic.Evidence{Kind: agentic.KindDrift, DriftID: "D1"})),
		line(7, agentic.UnverifiableInRepo),
	}
	l := &Ledger{SchemaVersion: SchemaVersion}
	for i := range lrs {
		c, ok := b.Claim(ctx, &lrs[i])
		if !ok {
			t.Fatalf("no claim for line %d", lrs[i].Line)
		}
		l.Merge(c)
	}
	bad := agentic.LineResult{Path: "README.md", Line: 3, Error: "timeout"}
	if _, ok := b.Claim(ctx, &bad); ok {
		t.Fatal("a failed judgment became a claim")
	}
	return l
}

func status(t *testing.T, g *gitRepo, l *Ledger) map[int]ClaimStatus {
	t.Helper()
	r, err := Check(context.Background(), l, g.dir, "demo", "HEAD", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if errs := schema(t, contracts.Stale).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("stale/v1: %v", errs)
	}
	out := map[int]ClaimStatus{}
	for _, c := range r.Claims {
		out[c.Line] = c
	}
	return out
}

func schema(t *testing.T, b []byte) *jsonschema.Schema {
	t.Helper()
	s, err := jsonschema.Compile(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInvalidationRules(t *testing.T) {
	g := newRepo(t)
	l := claims(t, g)
	b, _ := json.Marshal(l)
	if errs := schema(t, contracts.Ledger).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("ledger/v1: %v", errs)
	}
	// nothing changed: evidence-backed claims are fresh, the one without evidence is rechecked
	st := status(t, g, l)
	for n, want := range map[int]string{3: Fresh, 4: Fresh, 5: Fresh, 6: Fresh, 7: Recheck} {
		if st[n].Status != want {
			t.Errorf("unchanged line %d: %s, want %s", n, st[n].Status, want)
		}
	}
	tests := []struct {
		name   string
		change func()
		line   int
		reason string
	}{
		{"unrelated change", func() { g.write(map[string]string{"other.txt": "x\n"}) }, 0, ""},
		{"quoted file changed", func() { g.write(map[string]string{"config.yaml": "port: 8080\n"}) }, 3, ReasonEvidenceChanged},
		{"file added to an absence scope", func() { g.write(map[string]string{".github/workflows/deploy.yml": "on: push\n"}) }, 4, ReasonAbsenceScope},
		{"missing path appeared", func() { g.write(map[string]string{"docs/ENVIRONMENTS.md": "hosts\n"}) }, 5, ReasonPathAppeared},
		{"drift evidence changed", func() { g.write(map[string]string{"scripts/run.sh": "exec ./service --fast\n"}) }, 6, ReasonDriftChanged},
		{"removed path reappeared", func() { g.write(map[string]string{"old/run.sh": "x\n"}) }, 6, ReasonRemovedReappeared},
		{"documented line edited", func() {
			g.write(map[string]string{"README.md": strings.Replace(readFile(t, g, "README.md"), "build and test", "build, test and lint", 1)})
		}, 4, ReasonDocLineChanged},
	}
	for _, tt := range tests {
		tt.change()
		g.commit(tt.name)
		st := status(t, g, l)
		for n, s := range st {
			want := Fresh
			if n == 7 {
				want = Recheck
			}
			if n == tt.line {
				want = Stale
				if !hasReason(s, tt.reason) {
					t.Errorf("%s: line %d reasons %+v, want %s", tt.name, n, s.Reasons, tt.reason)
				}
			}
			if s.Status != want {
				t.Errorf("%s: line %d %s, want %s", tt.name, n, s.Status, want)
			}
		}
		l = claims(t, g) // re-verify everything at the new HEAD before the next change
	}
}

func readFile(t *testing.T, g *gitRepo, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g.dir, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hasReason(s ClaimStatus, kind string) bool {
	for _, r := range s.Reasons {
		if r.Kind == kind {
			return true
		}
	}
	return false
}

func TestLineMoveKeepsClaim(t *testing.T) {
	g := newRepo(t)
	l := claims(t, g)
	g.write(map[string]string{"README.md": "Intro line.\n" + readFile(t, g, "README.md")})
	g.commit("prepend a line")
	st := status(t, g, l)
	if st[3].Status != Fresh || st[3].NewLine != 4 {
		t.Fatalf("moved line: %+v", st[3])
	}
	g.git("rm", "-q", "README.md")
	g.commit("drop the readme")
	if st := status(t, g, l); st[3].Status != Stale || !hasReason(st[3], ReasonDocRemoved) {
		t.Fatalf("removed doc: %+v", st[3])
	}
}

func TestClaimFieldsAndStore(t *testing.T) {
	g := newRepo(t)
	l := claims(t, g)
	var c3 Claim
	for _, c := range l.Claims {
		if c.Line == 3 {
			c3 = c
		}
	}
	head := g.git("rev-parse", "HEAD")
	if c3.ID != ClaimID("demo", "README.md", "The service listens   on port 9090.") || c3.VerifiedRef != head || c3.DocBlob == "" ||
		len(c3.Evidence.Quotes) != 1 || c3.Evidence.Quotes[0].Blob == "" || c3.Private || c3.Verdict != "no" {
		t.Fatalf("claim %+v", c3)
	}
	p := filepath.Join(t.TempDir(), "sub", "ledger.json")
	if err := l.Save(p); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	back, err := Load(p)
	if err != nil || len(back.Claims) != 5 {
		t.Fatalf("%v %d", err, len(back.Claims))
	}
	older := c3
	older.VerifiedAt, older.Category = "2000-01-01T00:00:00Z", "contradicted"
	back.Merge(older)
	newer := c3
	newer.VerifiedAt, newer.Category = "2030-01-01T00:00:00Z", "outdated_wording"
	back.Merge(newer)
	for _, c := range back.Claims {
		if c.ID == c3.ID && c.Category != "outdated_wording" {
			t.Fatalf("merge kept %s", c.Category)
		}
	}
	if empty, err := Load(filepath.Join(t.TempDir(), "none.json")); err != nil || len(empty.Claims) != 0 {
		t.Fatal("missing ledger is not empty")
	}
	for _, bad := range []string{`{"schema_version":"2.0","claims":[]}`, `{"schema_version":"1.0","claims":[{"id":"x"}]}`, `{`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(`{"schema_version":"1.0","claims":[{"id":"a","repo":"r","path":"p","verified_ref":"x","evidence":{"absence":[{"pattern":"p","scope":"[","files_hash":"","files":0}]}}]}`)
	f.Add(`{"schema_version":"1.9","claims":null}`)
	f.Fuzz(func(t *testing.T, s string) {
		l, err := Parse([]byte(s))
		if err != nil {
			return
		}
		ch := &Changes{Paths: map[string]byte{"a": 'A', "b/c": 'M', "README.md": 'D'}}
		for i := range l.Claims {
			st := Invalidate(&l.Claims[i], ch, func(string) (string, bool) { return "", false })
			if st.Status != Fresh && st.Status != Stale && st.Status != Recheck {
				t.Fatalf("status %q", st.Status)
			}
		}
	})
}
