package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/related"
)

func runCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestInitCommand(t *testing.T) {
	code, out, _ := runCmd(t, "init", "-dry-run", "../../testdata/onboard/gocli")
	if code != exitOK || !strings.Contains(out, "# found AGENTS.md") || !strings.Contains(out, "docs/ contains 2 .md files") {
		t.Fatalf("dry run: %d\n%s", code, out)
	}
	dest := filepath.Join(t.TempDir(), "docsp.yaml")
	if code, _, errs := runCmd(t, "init", "-out", dest, "../../testdata/onboard/gocli"); code != exitOK {
		t.Fatalf("init: %d %s", code, errs)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != out {
		t.Fatalf("written file differs from the dry run: %v", err)
	}
	if code, _, errs := runCmd(t, "init", "-out", dest, "../../testdata/onboard/gocli"); code != exitUsage || !strings.Contains(errs, "exists") {
		t.Errorf("overwrote an existing file: %d %s", code, errs)
	}
	if code, _, _ := runCmd(t, "init", "-force", "-out", dest, "../../testdata/onboard/gocli"); code != exitOK {
		t.Errorf("-force: %d", code)
	}
}

func TestDoctorCommand(t *testing.T) {
	if code, out, _ := runCmd(t, "doctor", "../../testdata/onboard/gocli"); code != exitOK || !strings.Contains(out, "docsp doctor:") {
		t.Errorf("gocli: %d\n%s", code, out)
	}
	code, out, _ := runCmd(t, "doctor", "-format", "json", "../../testdata/onboard/badwf")
	if code != exitFindings {
		t.Errorf("badwf: exit %d, want %d", code, exitFindings)
	}
	var rep struct{ Checks []struct{ Level string } }
	if err := json.Unmarshal([]byte(out), &rep); err != nil || len(rep.Checks) == 0 {
		t.Errorf("json: %v\n%s", err, out)
	}
	if code, _, _ := runCmd(t, "doctor", "/nonexistent/repo"); code != exitUsage {
		t.Errorf("missing repo: %d", code)
	}
	if code, _, _ := runCmd(t, "doctor", "-format", "yaml", "."); code != exitUsage {
		t.Errorf("bad format: %d", code)
	}
}

// TestRelatedAndAuditSections drives the zero-config PR mode's deterministic half on a
// throwaway repository: the section the change is about is selected, and the audit sees only
// the claims inside it.
func TestRelatedAndAuditSections(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	put := func(rel, s string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("README.md", "# demo\n\n## Install\n\nThe binary is installed in 3 steps.\nIt needs 2 GB of memory.\n\n"+
		"## Retries\n\nSet RETRY_LIMIT to 5 to retry a failed upload 5 times.\nThe default backoff is 10 seconds.\n\n"+
		"## Contributing\n\nReviews take 2 days.\n")
	put("upload.go", "package demo\n\nconst backoff = 10\n")
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-qm", "init")
	git("checkout", "-qb", "feature")
	put("upload.go", "package demo\n\nimport \"os\"\n\nconst backoff = 10\n\nvar limit = os.Getenv(\"RETRY_LIMIT\")\n")
	git("commit", "-qam", "read RETRY_LIMIT")

	rel := filepath.Join(t.TempDir(), "related.json")
	code, out, errs := runCmd(t, "related", "-base", "main", "-out", rel, dir)
	if code != exitOK || !strings.Contains(out, `"Retries"`) || strings.Contains(out, `"Install"`) {
		t.Fatalf("related: %d %s\n%s", code, errs, out)
	}
	r, err := related.Load(rel)
	if err != nil || len(r.Sections) != 1 || r.Sections[0].From != 8 {
		t.Fatalf("related.json: %+v, %v", r, err)
	}
	claims := func(args ...string) int {
		t.Helper()
		code, _, errs := runCmd(t, append([]string{"audit", "-no-model", "-format", "none"}, append(args, dir)...)...)
		m := regexp.MustCompile(`judge none, (\d+) claims`).FindStringSubmatch(errs)
		if code != exitOK || m == nil {
			t.Fatalf("audit: %d %s", code, errs)
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	all, only := claims(), claims("-sections", rel)
	if only == 0 || only >= all {
		t.Errorf("claims: %d with -sections, %d without", only, all)
	}
	if code, _, _ := runCmd(t, "related", dir); code != exitUsage {
		t.Errorf("related without -base: %d", code)
	}
	if code, _, _ := runCmd(t, "audit", "-sections", "/nonexistent.json", dir); code != exitUsage {
		t.Errorf("audit with a missing -sections file: %d", code)
	}
}
