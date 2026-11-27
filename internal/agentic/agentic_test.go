package agentic

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// fixture builds a repository with history: old/run.sh renamed to scripts/run.sh and the
// port changed from 8080 to 9090 in a second commit; a side branch holds a commit that is
// not an ancestor of HEAD. It returns the directory and the rename commit.
func fixture(t testing.TB) (string, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(p, c string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e", "-c", "init.defaultBranch=main"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write("README.md", "# Demo\n\nRun `old/run.sh` to start the service on port 8080.\nSee ENVIRONMENTS.md section 4 for the hosts.\nThe CI jobs are build and test.\nThe service is written in Go.\n")
	write("old/run.sh", "#!/bin/sh\nexec ./service\n")
	write("config.yaml", "port: 8080\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("private/notes.md", "the secret plan\n")
	write(".github/workflows/ci.yml", "jobs:\n  build: {}\n  test: {}\n  lint: {}\n")
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "initial")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o750); err != nil {
		t.Fatal(err)
	}
	git("mv", "old/run.sh", "scripts/run.sh")
	write("config.yaml", "port: 9090\n")
	git("commit", "-qam", "move run.sh to scripts/, port 9090")
	rename := git("rev-parse", "--short=10", "HEAD")
	git("checkout", "-q", "-b", "side")
	write("side.txt", "x\n")
	git("add", "side.txt")
	git("commit", "-qm", "side")
	side := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	sideCommit = side
	return dir, rename
}

// sideCommit is the fixture's commit that is not an ancestor of HEAD (set by fixture).
var sideCommit string

func tools(t *testing.T, dir string, cloud bool) *judge.Tools {
	t.Helper()
	pol := judge.Policy{Repo: "demo", Cloud: cloud}
	pol.Class.Default = "public"
	pol.Class.Repos = map[string]string{}
	if cloud {
		pol.Class.Paths = append(pol.Class.Paths, privatePath())
	}
	tl, err := judge.NewPolicyTools(dir, pol, judgev1.ToolLimits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tl.Close() })
	return tl
}

func TestEvidenceValidators(t *testing.T) {
	dir, _ := fixture(t)
	v := &Validator{Tools: tools(t, dir, true), Drift: map[string]bool{"D1": true}, Doc: "README.md", Line: 3}
	ctx := context.Background()
	tests := []struct {
		name string
		e    Evidence
		ok   bool
	}{
		{"quote verbatim", Evidence{Kind: KindQuote, Path: "config.yaml", Line: 1, Quote: "port: 9090"}, true},
		{"quote within ±2 lines", Evidence{Kind: KindQuote, Path: ".github/workflows/ci.yml", Line: 2, Quote: "lint: {}"}, true},
		{"quote whitespace-normalised", Evidence{Kind: KindQuote, Path: ".github/workflows/ci.yml", Line: 4, Quote: "lint:   {}"}, true},
		{"quote not there", Evidence{Kind: KindQuote, Path: "config.yaml", Line: 1, Quote: "port: 7777"}, false},
		{"quote too far", Evidence{Kind: KindQuote, Path: ".github/workflows/ci.yml", Line: 1, Quote: "lint: {}"}, false},
		{"quote too short", Evidence{Kind: KindQuote, Path: "config.yaml", Line: 1, Quote: "port"}, false},
		{"quote is the statement", Evidence{Kind: KindQuote, Path: "README.md", Line: 3, Quote: "to start the service"}, false},
		{"quote private path", Evidence{Kind: KindQuote, Path: "private/notes.md", Line: 1, Quote: "the secret plan"}, false},
		{"quote in .git", Evidence{Kind: KindQuote, Path: ".git/HEAD", Line: 1, Quote: "ref: refs/heads/main"}, false},
		{"absence holds", Evidence{Kind: KindAbsence, SearchPattern: "kubernetes", SearchScope: "*"}, true},
		{"absence fails (case-insensitive)", Evidence{Kind: KindAbsence, SearchPattern: "PORT: 9090", SearchScope: "*.yaml"}, false},
		{"absence ignores the doc under test", Evidence{Kind: KindAbsence, SearchPattern: "ENVIRONMENTS.md section", SearchScope: "*"}, true},
		{"absence sees private files", Evidence{Kind: KindAbsence, SearchPattern: "secret plan", SearchScope: "*"}, false},
		{"absence regex", Evidence{Kind: KindAbsence, SearchPattern: `port: [0-9]+`, SearchScope: "config.yaml", SearchRegex: true}, false},
		{"absence bad regex", Evidence{Kind: KindAbsence, SearchPattern: `(`, SearchRegex: true}, false},
		{"absence pattern too short", Evidence{Kind: KindAbsence, SearchPattern: "ab"}, false},
		{"absence empty scope (typo)", Evidence{Kind: KindAbsence, SearchPattern: "deploy", SearchScope: "scirpts/*"}, false},
		{"absence empty scope = the missing file", Evidence{Kind: KindAbsence, SearchPattern: "ENVIRONMENTS.md", SearchScope: "**/ENVIRONMENTS.md"}, true},
		{"absence scope .github", Evidence{Kind: KindAbsence, SearchPattern: "deploy", SearchScope: ".github/workflows/*"}, true},
		{"drift id validated", Evidence{Kind: KindDrift, DriftID: "D1"}, true},
		{"drift id unknown", Evidence{Kind: KindDrift, DriftID: "D7"}, false},
		{"path missing", Evidence{Kind: KindPathMissing, Path: "ENVIRONMENTS.md"}, true},
		{"path missing (old path)", Evidence{Kind: KindPathMissing, Path: "old/run.sh"}, true},
		{"path exists", Evidence{Kind: KindPathMissing, Path: "scripts/run.sh"}, false},
		{"path exists elsewhere by name", Evidence{Kind: KindPathMissing, Path: "run.sh"}, false},
		{"directory exists by name", Evidence{Kind: KindPathMissing, Path: "workflows"}, false},
		{"path relative to the document", Evidence{Kind: KindPathMissing, Path: "./config.yaml"}, false},
		{"path glob refused", Evidence{Kind: KindPathMissing, Path: "docs/*.md"}, false},
		{"path escaping refused", Evidence{Kind: KindPathMissing, Path: "../x"}, false},
		{"unknown kind", Evidence{Kind: "vibes"}, false},
	}
	for _, tt := range tests {
		if c := v.Check(ctx, tt.e); c.OK != tt.ok {
			t.Errorf("%s: ok=%v want %v (%s)", tt.name, c.OK, tt.ok, c.Reason)
		}
	}
}

func TestPathMissingNextToDocument(t *testing.T) {
	dir, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(dir, ".github", "workflows", "NOTES.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := &Validator{Tools: tools(t, dir, false), Doc: ".github/workflows/ci.yml", Line: 1}
	if c := v.Check(context.Background(), Evidence{Kind: KindPathMissing, Path: "NOTES.md"}); c.OK {
		t.Fatal("a file next to the document was called missing")
	}
}

func TestValidateDrift(t *testing.T) {
	dir, rename := fixture(t)
	tl := tools(t, dir, false)
	raw := []DriftEntry{
		{Kind: "moved", Old: "old/run.sh", New: "scripts/run.sh", Summary: "moved", Commits: []string{rename}},
		{
			Kind: "changed_value", Old: "port 8080", New: "port 9090", Summary: "port", Commits: []string{"deadbeef"},
			Evidence: []QuoteRef{{Path: "config.yaml", Line: 1, Quote: "port: 9090"}},
		},
		{
			Kind: "changed_value", Old: "a", New: "b", Summary: "made up", Commits: []string{sideCommit},
			Evidence: []QuoteRef{{Path: "config.yaml", Line: 1, Quote: "port: 1234"}},
		},
		{Kind: "deleted", Old: "config.yaml", New: "-", Summary: "claims a live file is gone", Commits: []string{rename}},
		{Kind: "renamed", Old: "old/*", New: "scripts/", Summary: "glob of a gone directory", Commits: []string{rename}},
		{Kind: "other", Old: "x", New: "y", Summary: "commit only, nothing removed", Commits: []string{rename}},
	}
	got := ValidateDrift(context.Background(), tl, raw)
	if len(got) != 3 {
		t.Fatalf("validated %d entries, want 3: %+v", len(got), got)
	}
	want := []struct {
		id, old string
		commits int
		absent  bool
	}{{"D1", "old/run.sh", 1, true}, {"D2", "port 8080", 0, false}, {"D3", "old/*", 1, true}}
	for i, w := range want {
		g := got[i]
		if g.ID != w.id || g.Old != w.old || len(g.Commits) != w.commits || g.AbsentOldPath != w.absent {
			t.Errorf("entry %d: %+v, want %+v", i, g, w)
		}
	}
	d := &DriftList{Entries: got}
	if txt := d.Text(); !strings.Contains(txt, "D1 [moved] old/run.sh -> scripts/run.sh") || !strings.Contains(txt, "(now: config.yaml:1)") {
		t.Errorf("drift text:\n%s", txt)
	}
}

func TestDerivePriority(t *testing.T) {
	ok := Evidence{Kind: KindDrift, DriftID: "D1"}
	bad := Evidence{Kind: KindDrift, DriftID: "D9"}
	f := func(status string, ev ...Evidence) Fact { return Fact{Fact: status, Status: status, Evidence: ev} }
	tests := []struct {
		name  string
		facts []Fact
		want  string
	}{
		{"no facts", nil, Unknown},
		{"contradicted beats everything", []Fact{f(OutdatedWording, ok), f(IncompleteList, ok), f(DanglingReference, ok), f(Contradicted, ok)}, Contradicted},
		{"dangling beats incomplete", []Fact{f(IncompleteList, ok), f(DanglingReference, ok)}, DanglingReference},
		{"incomplete beats outdated", []Fact{f(OutdatedWording, ok), f(IncompleteList, ok)}, IncompleteList},
		{"invalid problem is skipped", []Fact{f(Contradicted, bad), f(OutdatedWording, ok)}, OutdatedWording},
		{"problem without evidence is void", []Fact{f(Contradicted), f(Confirmed, ok)}, Unknown},
		{"all confirmed", []Fact{f(Confirmed, ok), f(Confirmed, bad, ok)}, Confirmed},
		{"confirmed + unverifiable", []Fact{f(Confirmed, ok), f(UnverifiableInRepo)}, UnverifiableInRepo},
		{"unverifiable needs no evidence", []Fact{f(UnverifiableInRepo)}, UnverifiableInRepo},
		{"unvalidated confirmed", []Fact{f(Confirmed, bad)}, Unknown},
		{"unknown status", []Fact{f("maybe", ok)}, Unknown},
	}
	v := &Validator{Drift: map[string]bool{"D1": true}}
	for _, tt := range tests {
		d := Derive(context.Background(), v, tt.facts)
		if d.Category != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, d.Category, tt.want)
		}
	}
	if Verdict(Confirmed) != judgev1.No || Verdict(UnverifiableInRepo) != judgev1.Unknown || Verdict(OutdatedWording) != judgev1.Yes {
		t.Fatal("verdict mapping")
	}
}

// TestSourcesConflict: a sources_conflict fact needs validated quotes from two places; it
// ranks below the problems (the document is not shown wrong) and maps to no verdict.
func TestSourcesConflict(t *testing.T) {
	dir, _ := fixture(t)
	v := &Validator{Tools: tools(t, dir, true), Drift: map[string]bool{}, Doc: "README.md", Line: 3}
	cfg := Evidence{Kind: KindQuote, Path: "config.yaml", Line: 1, Quote: "port: 9090"}
	ci := Evidence{Kind: KindQuote, Path: ".github/workflows/ci.yml", Line: 4, Quote: "lint: {}"}
	f := func(status string, ev ...Evidence) Fact { return Fact{Fact: status, Status: status, Evidence: ev} }
	tests := []struct {
		name  string
		facts []Fact
		want  string
	}{
		{"two places", []Fact{f(SourcesConflict, cfg, ci), f(Confirmed, cfg)}, SourcesConflict},
		{"one place is no conflict", []Fact{f(SourcesConflict, cfg, cfg)}, Unknown},
		{"a problem still decides", []Fact{f(SourcesConflict, cfg, ci), f(Contradicted, cfg)}, Contradicted},
	}
	for _, tt := range tests {
		if d := Derive(context.Background(), v, tt.facts); d.Category != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, d.Category, tt.want)
		}
	}
	if IsProblem(SourcesConflict) || Verdict(SourcesConflict) != judgev1.Unknown {
		t.Error("sources_conflict is reported, not a problem verdict")
	}
}

func TestParseAnswersLenient(t *testing.T) {
	a, err := ParseLineAnswer(json.RawMessage(`{"facts":[{"fact":"x","status":" Contradicted ","evidence":{"quote":"port: 9090","path":"config.yaml","line":"1"}},
		{"fact":"y","status":"confirmed","evidence":[{"kind":"drift_id","drift_id":"D2"},7]},"junk"],"reason":"r"}`))
	if err != nil || len(a.Facts) != 2 {
		t.Fatalf("%v %+v", err, a)
	}
	e := a.Facts[0].Evidence[0]
	if a.Facts[0].Status != Contradicted || e.Kind != KindQuote || e.Line != 1 || a.Facts[1].Evidence[0].Kind != KindDrift {
		t.Fatalf("%+v", a)
	}
	if _, err := ParseLineAnswer(json.RawMessage(`{"verdict":"yes"}`)); err == nil {
		t.Fatal("answer without facts accepted")
	}
	d, err := ParseDocAnswer(json.RawMessage(`{"lines":[{"line":3,"facts":[],"reason":""},{"line":"5","facts":[{"fact":"f","status":"confirmed","evidence":[]}]},{"line":3,"facts":[]},{"facts":[]}]}`))
	if err != nil || len(d) != 2 || len(d[5].Facts) != 1 {
		t.Fatalf("%v %+v", err, d)
	}
	dr, err := ParseDriftAnswer(json.RawMessage(`{"drift":[{"kind":"moved","old":"a/","new":"b/","summary":"s","commits":["abc1234",5],"evidence":[{"path":"p","line":2,"quote":"q"}]}]}`))
	if err != nil || len(dr) != 1 || len(dr[0].Commits) != 2 || dr[0].Evidence[0].Line != 2 {
		t.Fatalf("%v %+v", err, dr)
	}
}

func FuzzParseLineAnswer(f *testing.F) {
	for _, s := range []string{
		`{"facts":[{"fact":"a","status":"confirmed","evidence":[{"kind":"quote","path":"a","line":1,"quote":"q"}]}],"reason":""}`,
		`{"facts":{}}`, `{"facts":[1,"x",{"evidence":{"line":1e300}}]}`, `[]`, `{"facts":[{"evidence":[{"line":"-3"}]}]}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		a, err := ParseLineAnswer(json.RawMessage(s))
		if err != nil {
			return
		}
		if len(a.Facts) > maxFacts {
			t.Fatalf("%d facts", len(a.Facts))
		}
		for _, fc := range a.Facts {
			if len(fc.Evidence) > maxEvidence {
				t.Fatalf("%d evidence", len(fc.Evidence))
			}
			for _, e := range fc.Evidence {
				if e.Line < 0 {
					t.Fatalf("negative line %d", e.Line)
				}
			}
		}
		_ = Derive(context.Background(), &Validator{Drift: map[string]bool{}}, onlyDrift(a.Facts))
	})
}

// onlyDrift keeps drift evidence (checkable without a worktree).
func onlyDrift(fs []Fact) []Fact {
	out := make([]Fact, 0, len(fs))
	for _, f := range fs {
		g := Fact{Fact: f.Fact, Status: f.Status}
		for _, e := range f.Evidence {
			if e.Kind == KindDrift {
				g.Evidence = append(g.Evidence, e)
			}
		}
		out = append(out, g)
	}
	return out
}

func FuzzParseDocAnswer(f *testing.F) {
	for _, s := range []string{`{"lines":[{"line":1,"facts":[],"reason":""}]}`, `{"lines":{}}`, `{"lines":[{"line":"2","facts":[{}]}]}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParseDocAnswer(json.RawMessage(s))
		if err != nil {
			return
		}
		if len(m) > maxDocLines {
			t.Fatalf("%d lines", len(m))
		}
		for n := range m {
			if n <= 0 {
				t.Fatalf("line %d", n)
			}
		}
	})
}

func FuzzParseDriftAnswer(f *testing.F) {
	for _, s := range []string{`{"drift":[{"kind":"moved","old":"a","new":"b","summary":"s","commits":["abc"],"evidence":[]}]}`, `{"drift":7}`, `{"drift":[{"commits":[1,2,{}]}]}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDriftAnswer(json.RawMessage(s))
		if err != nil {
			return
		}
		if len(d) > maxDriftItems {
			t.Fatalf("%d entries", len(d))
		}
	})
}
