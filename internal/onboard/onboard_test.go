package onboard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/config"
)

const fixtures = "../../testdata/onboard/"

func guess(gs []Guess, v string) (Guess, bool) {
	for _, g := range gs {
		if g.Value == v {
			return g, true
		}
	}
	return Guess{}, false
}

func TestInspectGoCLI(t *testing.T) {
	p, err := Inspect(fixtures + "gocli")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/*.md":                           "2 Markdown files at the root: CONTRIBUTING.md, README.md",
		"docs/**/*.md":                    "docs/ contains 2 .md files",
		"AGENTS.md":                       "found AGENTS.md",
		"CLAUDE.md":                       "none yet",
		".github/copilot-instructions.md": "found .github/copilot-instructions.md",
		".cursorrules":                    "found .cursorrules",
	}
	for v, why := range want {
		g, ok := guess(p.Docs, v)
		if !ok || !strings.Contains(g.Why, why) {
			t.Errorf("docs %s: %+v, want a reason containing %q", v, g, why)
		}
	}
	if _, ok := guess(p.Docs, "cmd/**/*.md"); ok {
		t.Error("cmd/ has no Markdown but was proposed")
	}
	for _, v := range []string{"testdata/", "CHANGELOG*.md"} {
		if _, ok := guess(p.Exclude, v); !ok {
			t.Errorf("exclude %s missing: %+v", v, p.Exclude)
		}
	}
	if _, ok := guess(p.Exclude, "**/testdata/"); ok {
		t.Error("**/testdata/ adds nothing over testdata/ here")
	}
	if len(p.Rules) != 1 || p.Rules[0].Name != "cli" || !strings.Contains(p.Rules[0].Why, "cmd/ holds") {
		t.Errorf("rules %+v", p.Rules)
	}
	// README, CONTRIBUTING, 2 under docs/, AGENTS.md, copilot instructions, .cursorrules
	if p.DocFiles != 7 {
		t.Errorf("doc files = %d, want 7", p.DocFiles)
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("proposal does not parse: %v\n%s", err, p.YAML())
	}
	cfg, err := config.Parse([]byte(p.YAML()))
	if err != nil || len(cfg.Docs) != len(p.Docs) || len(cfg.Impact.Rules) != 1 {
		t.Fatalf("parsed %+v, %v", cfg, err)
	}
	y := p.YAML()
	for _, s := range []string{"# docs/ contains 2 .md files", "# found AGENTS.md", "# these globs match 7 documentation files now"} {
		if !strings.Contains(y, s) {
			t.Errorf("YAML lacks %q:\n%s", s, y)
		}
	}
}

func TestInspectBareRepo(t *testing.T) {
	p, err := Inspect(fixtures + "bare")
	if err != nil {
		t.Fatal(err)
	}
	if p.DocFiles != 0 || len(p.Rules) != 0 || len(p.Exclude) != 0 {
		t.Errorf("bare: %+v", p)
	}
	// the AI-instruction files are proposed even when absent
	for _, a := range AIInstructionFiles {
		if _, ok := guess(p.Docs, a); !ok {
			t.Errorf("%s not proposed", a)
		}
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

// env is a fake environment: only the names in it are set.
func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func lookPath(have ...string) func(string) (string, error) {
	return func(n string) (string, error) {
		for _, h := range have {
			if h == n {
				return "/usr/bin/" + n, nil
			}
		}
		return "", errors.New("not found")
	}
}

func levelOf(r *Report, name, substr string) string {
	for _, c := range r.Checks {
		if c.Name == name && strings.Contains(c.Message, substr) {
			return c.Level
		}
	}
	return ""
}

func TestDoctorGreen(t *testing.T) {
	const secret = "s3cr3t-token-value"
	rep := Doctor(context.Background(), DoctorOptions{
		Root: fixtures + "gocli", Getenv: env(map[string]string{"GITHUB_ACTIONS": "true", DefaultTokenEnv: secret}),
		LookPath: lookPath("git", "claude"),
	})
	if n := rep.Count(Fail); n != 0 {
		t.Fatalf("failed checks: %+v", rep.Checks)
	}
	for _, c := range [][3]string{
		{"config", "no .docsp.yaml", Info},
		{"docs", "match", OK},
		{"token", "is set (value not shown)", OK},
		{"judge", "`claude` is on PATH", OK},
		{"permissions", "suffice", OK},
		{"checkout", "fetch-depth: 0", OK},
		{"budget", "sane", OK},
		{"triggers", "PR only", Info},
	} {
		if got := levelOf(rep, c[0], c[1]); got != c[2] {
			t.Errorf("%s %q: level %q, want %q", c[0], c[1], got, c[2])
		}
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), secret) {
		t.Fatal("the token's value leaked into the report")
	}
}

func TestDoctorFailures(t *testing.T) {
	rep := Doctor(context.Background(), DoctorOptions{
		Root: fixtures + "badwf", Getenv: env(map[string]string{"GITHUB_ACTIONS": "true"}), LookPath: lookPath(),
	})
	for _, c := range [][3]string{
		{"git", "not on PATH", Fail},
		{"token", "is not set in this job", Warn},
		{"permissions", "pull-requests: write is needed", Fail},
		{"permissions", "actions: read is needed", Fail},
		{"checkout", "without fetch-depth: 0", Warn},
		{"inputs", `task "review"`, Fail},
		{"token", "no claude-oauth-token input", Warn},
		{"budget", `bootstrap-usd "lots"`, Fail},
		{"budget", "run-usd (9) is above monthly-usd (5)", Warn},
		{"cache", "weekly schedule", Info},
	} {
		if got := levelOf(rep, c[0], c[1]); got != c[2] {
			t.Errorf("%s %q: level %q, want %q\n%+v", c[0], c[1], got, c[2], rep.Checks)
		}
	}
}

func TestDoctorConfigAndGlobs(t *testing.T) {
	rep := Doctor(context.Background(), DoctorOptions{Root: fixtures + "badconfig", Getenv: env(nil), LookPath: lookPath("git")})
	if levelOf(rep, "config", "doks") != Fail {
		t.Errorf("unknown field not reported: %+v", rep.Checks)
	}
	if levelOf(rep, "workflow", "no workflow runs") != Warn {
		t.Errorf("missing workflow not reported: %+v", rep.Checks)
	}
	rep = Doctor(context.Background(), DoctorOptions{Root: fixtures + "bare", Getenv: env(nil), LookPath: lookPath("git")})
	if levelOf(rep, "docs", "match no file") != Fail {
		t.Errorf("no docs not reported: %+v", rep.Checks)
	}
	if levelOf(rep, "token", "not set here") != Info {
		t.Errorf("token outside CI: %+v", rep.Checks)
	}
}

func TestDoctorInstance(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "instance.yaml")
	y := "version: 1\nai:\n  judge:\n    provider: claude-code\n    model: m\n    claude_code: {program: /nonexistent/claude, pass_env: [MY_TOKEN]}\n"
	if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := Doctor(context.Background(), DoctorOptions{Root: fixtures + "gocli", InstanceFile: p, Getenv: env(nil), LookPath: lookPath("git")})
	for _, c := range [][3]string{
		{"instance", "parses (judge provider claude-code)", OK},
		{"judge", `"/nonexistent/claude" is not found`, Fail},
		{"token", "MY_TOKEN (passed to the judge) is not set", Warn},
		{"budget", "max_usd is not set", Warn},
	} {
		if got := levelOf(rep, c[0], c[1]); got != c[2] {
			t.Errorf("%s %q: level %q, want %q\n%+v", c[0], c[1], got, c[2], rep.Checks)
		}
	}
	if err := os.WriteFile(p, []byte("version: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep = Doctor(context.Background(), DoctorOptions{Root: fixtures + "gocli", InstanceFile: p, Getenv: env(nil), LookPath: lookPath("git")})
	if rep.Count(Fail) == 0 {
		t.Errorf("bad instance accepted: %+v", rep.Checks)
	}
}
