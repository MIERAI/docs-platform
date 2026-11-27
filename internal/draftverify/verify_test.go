package draftverify

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/sandbox"
)

const readme = `# Demo

The server listens on port 8080 (see config.yaml).
Run the tool to greet.

` + "```sh" + `
demo hello
` + "```" + `
`

// fixture is a small Go repository with a binary (cmd/demo), a config and a README, plus
// a ledger claim for the README's port line judged against config.yaml.
type fixture struct {
	dir  string
	head string
	l    *ledger.Ledger
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		f := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{
		"go.mod":           "module example.com/demo\n\ngo 1.22\n",
		"cmd/demo/main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tif len(os.Args) > 1 && os.Args[1] == \"hello\" {\n\t\tfmt.Println(\"hello\")\n\t\treturn\n\t}\n\tos.Exit(2)\n}\n",
		"config.yaml":      "# settings\nport: 9090\n",
		"README.md":        readme,
	})
	git(t, dir, "init", "-q")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	head := git(t, dir, "rev-parse", "HEAD")
	text := "The server listens on port 8080 (see config.yaml)."
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion, Claims: []ledger.Claim{{
		ID: ledger.ClaimID("demo", "README.md", text), Repo: "demo", Path: "README.md", Line: 3, Text: text,
		DocBlob: git(t, dir, "rev-parse", "HEAD:README.md"), Category: "contradicted", VerifiedRef: head,
		Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "config.yaml", Line: 2, Blob: git(t, dir, "rev-parse", "HEAD:config.yaml")}}},
	}}}
	return &fixture{dir: dir, head: head, l: l}
}

func (f *fixture) input(t *testing.T, newText string) *Input {
	t.Helper()
	return &Input{
		Root: f.dir, RepoName: "demo", Path: "README.md", Old: readme, New: newText,
		BaseBlob: f.l.Claims[0].DocBlob, Ledger: f.l,
	}
}

func gate(r *Result, name string) Gate {
	for _, g := range r.Gates {
		if g.Name == name {
			return g
		}
	}
	return Gate{}
}

func TestDiffLines(t *testing.T) {
	cs := diffLines([]string{"a", "b", "c", "d"}, []string{"a", "B", "c", "x", "d", "e"})
	want := []Change{{Old: 2, New: 2, OldText: "b", NewText: "B"}, {New: 4, NewText: "x"}, {New: 6, NewText: "e"}}
	if len(cs) != len(want) {
		t.Fatalf("%+v", cs)
	}
	for i := range want {
		if cs[i] != want[i] {
			t.Fatalf("change %d: %+v, want %+v", i, cs[i], want[i])
		}
	}
	if cs := diffLines([]string{"a", "b"}, []string{"a"}); len(cs) != 1 || cs[0].Old != 2 || cs[0].New != 0 {
		t.Fatalf("deletion: %+v", cs)
	}
	// a line deleted inside a block touches the block
	if m := touchedNew(diffLines([]string{"```sh", "x", "y", "```"}, []string{"```sh", "x", "```"})); !m[2] || !m[3] {
		t.Fatalf("touched %v", m)
	}
}

func TestApplyLine(t *testing.T) {
	text := "x\n  - port 8080\ny\n"
	got, err := ApplyLine(text, 9, "- port 8080", "- port 9090", false)
	if err != nil || got != "x\n  - port 9090\ny\n" {
		t.Fatalf("%q %v", got, err)
	}
	if got, _ := ApplyLine(text, 2, "- port 8080", "", true); got != "x\ny\n" {
		t.Fatalf("delete: %q", got)
	}
	if _, err := ApplyLine(text, 2, "gone", "x", false); !errors.Is(err, ErrLineGone) {
		t.Fatalf("missing line: %v", err)
	}
}

func TestFormatGate(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, path, old, new string
		want                 Status
	}{
		{"markdown fine", "a.md", "# A\n\ntext\n", "# A\n\nnew `text`\n", Pass},
		{"unclosed fence", "a.md", "# A\n", "# A\n\n```go\nfunc x() {}\n", Fail},
		{"broken go block", "a.md", "```go\nx := 1\n```\n", "```go\nx := (1\n```\n", Fail},
		{"go fragments parse", "a.md", "t\n", "```go\nfunc F() int { return 1 }\n```\n\n```go\nx := F()\n```\n", Pass},
		{"broken before stays allowed", "a.md", "```json\n{\"a\":\n```\ntext\n", "```json\n{\"a\":\n```\ntext 2\n", Pass},
		{"broken yaml block", "a.md", "t\n", "```yaml\na: [1\n```\n", Fail},
		{"broken shell block", "a.md", "t\n", "```sh\nif true; then echo\n```\n", Fail},
		{"console block lexes prompts only", "a.md", "t\n", "```console\n$ echo hi\nhi (output)\n```\n", Pass},
		{"unbalanced inline code", "a.md", "a `b` c\n", "a `b c\n", Fail},
		{"unclosed link", "a.md", "see [x](a.md)\n", "see [x](a.md\n", Fail},
		{"front matter", "a.md", "---\na: 1\n---\nt\n", "---\na: [\n---\nt\n", Fail},
		{"yaml document", "c.yaml", "a: 1\n", "a: 1\nb: [\n", Fail},
		{"json document", "c.json", "{}", "{\"a\": 1}", Pass},
		{"json broken", "c.json", "{}", "{\"a\": }", Fail},
		{"toml fine", "c.toml", "a = 1\n", "[t]\na = \"x # y\"\nb = [\n  1, 2,\n]\nc = '''\nz\n'''\n", Pass},
		{"toml broken", "c.toml", "a = 1\n", "a = \"x\n", Fail},
		{"other files", "a.txt", "a", "b", NotApplicable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			changed := changedNew(diffLines(splitLines(c.old), splitLines(c.new)))
			if g := formatGate(ctx, c.path, c.old, c.new, changed); g.Status != c.want {
				t.Fatalf("status %s, want %s: %+v", g.Status, c.want, g)
			}
		})
	}
}

func TestCheckTOML(t *testing.T) {
	for _, ok := range []string{"", "# c\n", "a.b = true\n[x.y]\n\"q\" = 1979-05-27\n[[arr]]\nv = { a = 1, b = \"}\" }\n"} {
		if err := checkTOML(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a\n", "a =\n", "[x\n", "a = [1, 2\n", "a = \"\"\"\nopen\n", "a = }\n"} {
		if err := checkTOML(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestClaimsGate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	line := "The server listens on port 8080 (see config.yaml)."
	cases := []struct {
		name, draft string
		want        Status
		detail      string
	}{
		{"value quoted by the evidence", "The server listens on port 9090 (see config.yaml).", Pass, "quoted verbatim"},
		{"value the evidence does not show", "The server listens on port 7070 (see config.yaml).", Fail, "which no evidence quote"},
		{"prose rewording", "The server usually listens on port 9090 (see config.yaml).", NeedsModel, "rewords prose"},
		{"link to the evidence file", "The server listens on the port in [config.yaml](config.yaml).", NeedsModel, "rewords prose"},
		{"deleted detail only", "The server listens on port (see config.yaml).", Pass, "removes detail only"},
		{"reference that does not resolve", "The server listens on port 9090 (see `scripts/missing.sh`).", Fail, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Verify(ctx, f.input(t, strings.Replace(readme, line, c.draft, 1)))
			g := gate(r, GateClaims)
			if g.Status != c.want {
				t.Fatalf("claims %s, want %s: %+v", g.Status, c.want, g.Items)
			}
			if c.detail != "" && !strings.Contains(asJSON(t, g), c.detail) {
				t.Fatalf("detail lacks %q: %+v", c.detail, g.Items)
			}
		})
	}
	t.Run("deleted line", func(t *testing.T) {
		r := Verify(ctx, f.input(t, strings.Replace(readme, line+"\n", "", 1)))
		if g := gate(r, GateClaims); g.Status != Pass {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("no ledger", func(t *testing.T) {
		in := f.input(t, strings.Replace(readme, "8080", "9090", 1))
		in.Ledger = nil
		if g := gate(Verify(ctx, in), GateClaims); g.Status != NeedsModel {
			t.Fatalf("%+v", g)
		}
	})
	t.Run("evidence changed since judged", func(t *testing.T) {
		g2 := newFixture(t)
		write(t, g2.dir, map[string]string{"config.yaml": "# settings\nport: 9191\n"})
		git(t, g2.dir, "commit", "-qam", "port")
		r := Verify(ctx, g2.input(t, strings.Replace(readme, "8080", "9090", 1)))
		if g := gate(r, GateClaims); g.Status != NeedsModel || !strings.Contains(asJSON(t, g), "evidence changed") {
			t.Fatalf("%+v", g)
		}
	})
	// ledger 1.1: a scoped quote whose file changed only outside the scope is carried
	// forward - the claim stays fresh and the evidence is read at the quote's new line
	scoped := func(t *testing.T) *fixture {
		g := newFixture(t)
		q := &g.l.Claims[0].Evidence.Quotes[0]
		q.Scope = ledger.ComputeScope("config.yaml", "# settings\nport: 9090\n", 2)
		if q.Scope == nil {
			t.Fatal("no scope")
		}
		return g
	}
	t.Run("evidence carried forward", func(t *testing.T) {
		g3 := scoped(t)
		write(t, g3.dir, map[string]string{"config.yaml": "# settings\nname: demo\nhost: a\nother: b\nmore: c\nport: 9090\n"})
		git(t, g3.dir, "commit", "-qam", "unrelated keys")
		r := Verify(ctx, g3.input(t, strings.Replace(readme, "8080", "9090", 1)))
		if g := gate(r, GateClaims); g.Status != Pass || !strings.Contains(asJSON(t, g), "carried forward") {
			t.Fatalf("%+v", g)
		}
		// draft.Evidence follows a re-anchored quote to the revision it now refers to
		q := &g3.l.Claims[0].Evidence.Quotes[0]
		q.Line, q.CarriedRef = 6, git(t, g3.dir, "rev-parse", "HEAD")
		if ev := draft.Evidence(ctx, g3.dir, &g3.l.Claims[0]); len(ev) != 1 || !strings.Contains(ev[0].Text, "port: 9090") || ev[0].From != 4 {
			t.Fatalf("re-anchored evidence: %+v", ev)
		}
	})
	t.Run("scoped evidence changed in scope", func(t *testing.T) {
		g4 := scoped(t)
		write(t, g4.dir, map[string]string{"config.yaml": "# settings\nport: 9191\n"})
		git(t, g4.dir, "commit", "-qam", "port")
		if g := gate(Verify(ctx, g4.input(t, strings.Replace(readme, "8080", "9090", 1))), GateClaims); g.Status != NeedsModel {
			t.Fatalf("%+v", g)
		}
	})
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGuardAndApply(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if got, want := BlobID(ctx, f.dir, readme), f.l.Claims[0].DocBlob; got != want {
		t.Fatalf("blob id %s, git says %s", got, want)
	}
	r := Verify(ctx, f.input(t, strings.Replace(readme, "8080", "9090", 1)))
	if g := gate(r, GateGuard); g.Status != Pass || !r.Verified {
		t.Fatalf("guard %+v, verified %v: %s", g, r.Verified, asJSON(t, r))
	}
	in := f.input(t, strings.Replace(readme, "8080", "9090", 1))
	in.BaseBlob = "0000000000000000000000000000000000000000"
	if r := Verify(ctx, in); gate(r, GateGuard).Status != Fail || r.Verified {
		t.Fatalf("a moved blob must fail the guard: %s", asJSON(t, r))
	}
	in.BaseBlob = ""
	if r := Verify(ctx, in); gate(r, GateGuard).Status != Fail {
		t.Fatal("no base blob must fail the guard")
	}

	base := f.l.Claims[0].DocBlob
	if err := Apply(ctx, f.dir, "README.md", "0000", readme); !errors.Is(err, ErrMoved) {
		t.Fatalf("apply against another blob: %v", err)
	}
	if err := Apply(ctx, f.dir, "README.md", base, readme+"\n```go\nfunc(\n"); err == nil {
		t.Fatal("apply of a draft that does not parse must be refused")
	}
	want := strings.Replace(readme, "8080", "9090", 1)
	if err := Apply(ctx, f.dir, "README.md", base, want); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.dir, "README.md")); string(b) != want {
		t.Fatalf("applied: %q", b)
	}
	if err := Apply(ctx, f.dir, "README.md", base, readme); !errors.Is(err, ErrMoved) {
		t.Fatalf("a second apply against the old blob must be refused: %v", err)
	}
	if err := Apply(ctx, f.dir, "../x.md", base, readme); err == nil {
		t.Fatal("a path outside the repository must be refused")
	}
}

func TestResultMatchesContract(t *testing.T) {
	f := newFixture(t)
	r := Verify(context.Background(), f.input(t, strings.Replace(readme, "8080", "7070", 1)))
	b, err := json.Marshal(map[string]any{"schema_version": SchemaVersion, "schema": Schema, "results": []*Result{r, Refused(context.Background(), f.input(t, readme), "gone")}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := jsonschema.Compile(contracts.DraftVerify)
	if err != nil {
		t.Fatal(err)
	}
	if errs := s.ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("draft-verify/v1: %v\n%s", errs, b)
	}
}

// fakeRunner records specs and answers with a fixed result.
type fakeRunner struct {
	specs []sandbox.Spec
	res   sandbox.Result
}

func (*fakeRunner) Name() string { return "fake" }
func (*fakeRunner) Arch() string { return "amd64" }
func (f *fakeRunner) Run(_ context.Context, s sandbox.Spec) (sandbox.Result, error) {
	f.specs = append(f.specs, s)
	return f.res, nil
}

func TestRunnableBlocks(t *testing.T) {
	bins := map[string]string{"demo": "./cmd/demo"}
	cases := []struct {
		info, body string
		run        bool
		why        string
	}{
		{"sh", "demo hello", true, "every command"},
		{"sh", "demo hello\nmake build", false, ""},
		{"sh runnable", "make build", true, "marked"},
		{"sh norun", "demo hello", false, ""},
		{"console", "$ FOO=1 demo hello\nhello", true, "every command"},
		{"go", "package main\nfunc main() {}", false, ""},
		{"go {.runnable}", "package main\nfunc main() {}", true, "marked"},
		{"python runnable", "print(1)", true, "marked"},
	}
	for _, c := range cases {
		text := "```" + c.info + "\n" + c.body + "\n```\n"
		fs := fences(text)
		if len(fs) != 1 {
			t.Fatalf("%q: %d fences", text, len(fs))
		}
		ex, ok := runnable(fs[0], bins)
		if ok != c.run || ok && !strings.Contains(ex.why, c.why) {
			t.Errorf("%q: run %v (%s), want %v (%s)", text, ok, ex.why, c.run, c.why)
		}
	}
	if fs := fences("```sh exit=2\nx\n```\n"); len(fs) != 1 {
		t.Fatal("fence")
	} else if ex, _ := runnable(fs[0], map[string]string{"x": "./cmd/x"}); ex.expect != 2 {
		t.Fatalf("exit=2: %+v", ex)
	}
}

func TestExamplesGateWithoutSandboxNeverRuns(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, strings.Replace(readme, "demo hello", "demo hello  ", 1))
	g := examplesGate(context.Background(), in, changedNew(diffLines(splitLines(in.Old), splitLines(in.New))))
	if g.Status != Skipped || !strings.Contains(g.Detail, "no sandbox") {
		t.Fatalf("%+v", g)
	}
	in.New = strings.Replace(readme, "Run the tool", "Run this tool", 1)
	if g := examplesGate(context.Background(), in, changedNew(diffLines(splitLines(in.Old), splitLines(in.New)))); g.Status != NotApplicable {
		t.Fatalf("no example changed: %+v", g)
	}
}

func TestExamplesGateSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the fixture's binary")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	f := newFixture(t)
	fr := &fakeRunner{res: sandbox.Result{ExitCode: 0, Output: "hello\n"}}
	in := f.input(t, strings.Replace(readme, "demo hello", "demo hello\ndemo hello", 1))
	in.Sandbox = fr
	g := examplesGate(context.Background(), in, changedNew(diffLines(splitLines(in.Old), splitLines(in.New))))
	if g.Status != Pass || len(fr.specs) != 1 {
		t.Fatalf("%+v (%d runs)", g, len(fr.specs))
	}
	s := fr.specs[0]
	if len(s.Mounts) != 2 || s.Mounts[0].Container != repoMount || s.Mounts[1].Container != binMount {
		t.Fatalf("mounts %+v", s.Mounts)
	}
	if !strings.Contains(s.Script, "set -e") || !strings.Contains(s.Script, "demo hello\ndemo hello") {
		t.Fatalf("script %q", s.Script)
	}
	fr.res = sandbox.Result{ExitCode: 2}
	if g := examplesGate(context.Background(), in, changedNew(diffLines(splitLines(in.Old), splitLines(in.New)))); g.Status != Fail {
		t.Fatalf("exit 2 must fail: %+v", g)
	}
}

// TestExamplesInRealSandbox runs changed examples in docker: the repository's binary works,
// a Go example runs, and an example that needs the network fails.
func TestExamplesInRealSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("sandbox tests need docker (skipped in -short)")
	}
	d, err := sandbox.Detect(context.Background())
	if err != nil {
		t.Skip("skipped: no sandbox (docker not available)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	f := newFixture(t)
	doc := readme + "\n```go runnable\npackage main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"from go\") }\n```\n" +
		"\n```sh runnable\nwget -q -T 3 -O /dev/null http://1.1.1.1/\n```\n" +
		"\n```sh runnable\ntest ! -e .env && test ! -e .git && test -f config.yaml && grep -q 'demo hello' README.md\n```\n"
	write(t, f.dir, map[string]string{".env": "TOKEN=secret\n"}) // untracked: never staged
	in := f.input(t, strings.Replace(doc, "demo hello", "demo hello\ndemo hello", 1))
	in.Old = readme
	in.Sandbox = d
	g := examplesGate(context.Background(), in, changedNew(diffLines(splitLines(in.Old), splitLines(in.New))))
	if len(g.Items) != 4 {
		t.Fatalf("%+v", g)
	}
	if it := g.Items[3]; it.Status != Pass {
		t.Fatalf("the sandbox must see the tracked files with the draft applied, no .env, no .git: %+v", it)
	}
	if len(g.Items) != 4 {
		t.Fatalf("%+v", g)
	}
	if it := g.Items[0]; it.Status != Pass || !strings.Contains(it.Output, "hello") {
		t.Fatalf("repo binary: %+v", it)
	}
	if it := g.Items[1]; it.Status != Pass || !strings.Contains(it.Output, "from go") {
		t.Fatalf("go example: %+v", it)
	}
	if it := g.Items[2]; it.Status != Fail {
		t.Fatalf("network example must fail in the sandbox: %+v", it)
	}
}
