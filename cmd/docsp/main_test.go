package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
)

const fixture = "../../testdata/fixtures/basic"

func TestRunCommands(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		code     int
		inStdout string
	}{
		{"no args", nil, exitUsage, ""},
		{"unknown", []string{"frobnicate"}, exitUsage, ""},
		{"version", []string{"version"}, exitOK, "docsp " + version},
		{"help", []string{"help"}, exitOK, "usage: docsp"},
		{"check json", []string{"check", fixture}, exitOK, `"schema_version": "1.3"`},
		{"check text", []string{"check", "-format", "text", fixture}, exitOK, "docs.reference.unresolved.file_path"},
		{"check fail-on", []string{"check", "-format", "none", "-fail-on", "medium", fixture}, exitFindings, ""},
		{"check fail-on high passes", []string{"check", "-format", "none", "-fail-on", "high", fixture}, exitOK, ""},
		{"check bad fail-on", []string{"check", "-fail-on", "loud", fixture}, exitUsage, ""},
		{"check bad confidence", []string{"check", "-min-confidence", "sure", fixture}, exitUsage, ""},
		{"check missing repo", []string{"check", "/nonexistent/repo"}, exitUsage, ""},
		{"impact needs base", []string{"impact", fixture}, exitUsage, ""},
		{"extract", []string{"extract", fixture}, exitOK, `"kind": "cli_flag"`},
		{"validate golden", []string{"validate", "../../testdata/golden/basic-low.findings.json"}, exitOK, "valid findings/v1"},
		{"check help", []string{"check", "-h"}, exitOK, ""},
		{"import backlog", []string{"import-findings", "-repo-name", "demo", "-repo", "o/demo", "../../testdata/backlog/backlog.jsonl"}, exitOK, `"check": "docs.backlog.wrong"`},
		{"import needs repo-name", []string{"import-findings", "../../testdata/backlog/backlog.jsonl"}, exitUsage, ""},
		{"eval dry run", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-task", "both", "-dry-run"}, exitOK, "dry run (estimate): task draft, claude-code:claude-opus-5-5, 1 items"},
		{"eval needs a provider", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl"}, exitUsage, ""},
		{"eval bad task", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-task", "poem", "-dry-run"}, exitUsage, ""},
		{"eval agentic dry run", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-mode", "agentic", "-worktree", "demo=../..", "-no-drift-cache", "-dry-run"}, exitOK, "agentic mode, line grouping"},
		{"eval agentic doc grouping", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-mode", "agentic", "-group", "doc", "-worktree", "demo=../..", "-no-drift", "-dry-run"}, exitOK, "agentic mode, doc grouping"},
		{"eval agentic bad group", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-mode", "agentic", "-group", "page", "-dry-run"}, exitUsage, ""},
		{"eval bad mode", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-mode", "psychic", "-dry-run"}, exitUsage, ""},
		{"eval agentic draft refused", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-instance-config", "../../testdata/instance/claude-code-dry.yaml", "-mode", "agentic", "-task", "draft", "-dry-run"}, exitUsage, ""},
		{"eval bad worktree flag", []string{"eval-model", "-set", "../../testdata/evalset/mini.jsonl", "-worktree", "nodir"}, exitUsage, ""},
		{"audit bad mode", []string{"audit", "-mode", "psychic", fixture}, exitUsage, ""},
		{"audit agentic without a model", []string{"audit", "-mode", "agentic", "-no-model", fixture}, exitOK, `"name": "docs-audit"`},
		{"judge-tools-mcp needs a spec", []string{"judge-tools-mcp"}, exitUsage, ""},
		{"stale needs a ledger", []string{"stale", fixture}, exitUsage, ""},
		{"schedule needs a ledger", []string{"schedule", fixture}, exitUsage, ""},
		{"sample needs -out", []string{"sample", "-ledger", "/nonexistent/ledger.json", fixture}, exitUsage, ""},
		{"slo apply needs state", []string{"slo", "-ledger", "/nonexistent/ledger.json", "-apply", "../.."}, exitUsage, ""},
		{"recheck needs a schedule", []string{"recheck", "-ledger", "/nonexistent/ledger.json", "../.."}, exitUsage, ""},
		{"schedule empty ledger", []string{"schedule", "-ledger", "/nonexistent/ledger.json", "-repo-name", "docs-platform", "../.."}, exitOK, "0 claims"},
		{"group needs a sub-command", []string{"group"}, exitUsage, ""},
		{"group without groups", []string{"group", "plan"}, exitUsage, ""},
		{"group bad line", []string{"group", "judge", "-line", "nope"}, exitUsage, ""},
		{"audit none", []string{"audit", "-no-model", fixture}, exitOK, `"name": "docs-audit"`},
		{"audit no instance = no model", []string{"audit", fixture}, exitOK, `"name": "docs-audit"`},
		{"audit unknown provider", []string{"audit", "-instance-config", "../../testdata/instance/bad-provider.yaml", fixture}, exitUsage, ""},
		{"audit ollama needs endpoint", []string{"audit", "-instance-config", "../../testdata/instance/ollama-no-endpoint.yaml", fixture}, exitUsage, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(context.Background(), c.args, &out, &errb)
			if code != c.code {
				t.Fatalf("exit = %d, want %d (stderr %s)", code, c.code, errb.String())
			}
			if !strings.Contains(out.String(), c.inStdout) {
				t.Errorf("stdout lacks %q:\n%s", c.inStdout, out.String())
			}
		})
	}
}

func TestCheckOutFileIsPrivate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "f.json")
	var so, se bytes.Buffer
	if code := run(context.Background(), []string{"check", "-out", out, fixture}, &so, &se); code != exitOK {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	st, err := os.Stat(out)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", st.Mode(), err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil || so.Len() != 0 {
		t.Errorf("out file invalid (%v) or JSON also on stdout", err)
	}
	if !strings.Contains(se.String(), "docsp:") {
		t.Errorf("summary line missing on stderr: %q", se.String())
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestImpactDerivesSections(t *testing.T) {
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
	write("README.md", "# Demo\n\n## Running\n\nSet `APP_PORT` and run `cmd/tool.go`.\n\n## Other\n\nNothing here.\n")
	write("cmd/tool.go", "package main\n\nimport \"os\"\n\nvar port = os.Getenv(\"APP_PORT\")\n")
	gitT(t, dir, "init", "-q")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "one")
	write("cmd/tool.go", "package main\n\nimport \"os\"\n\nvar port = os.Getenv(\"APP_LISTEN\")\n")
	gitT(t, dir, "commit", "-qam", "rename env")
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"impact", "-base", "HEAD~1", dir}, &out, &errb)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var rep struct {
		Sections []struct {
			Path, Heading, Status string
			Reasons               []struct{ Kind, Subject, Changed string }
		}
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	sch, err := jsonschema.Compile(contracts.Impact)
	if err != nil {
		t.Fatal(err)
	}
	if errs := sch.ValidateJSON(out.Bytes()); len(errs) > 0 {
		t.Fatalf("impact/v1 contract: %v", errs)
	}
	if len(rep.Sections) != 1 || rep.Sections[0].Heading != "Running" || rep.Sections[0].Status != "reread" {
		t.Fatalf("sections %+v", rep.Sections)
	}
	if !strings.Contains(errb.String(), `re-read README.md:3 "Running"`) {
		t.Errorf("summary %q", errb.String())
	}
}
