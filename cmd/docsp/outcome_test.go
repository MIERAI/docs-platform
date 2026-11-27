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
	"time"

	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/outcome"
	"github.com/MIERAI/docs-platform/internal/prreview"
)

// TestOutcomeCommands runs the precision loop end to end on a real git repository with a fake
// gh: diff after a run, PR ticks read twice (appended once), an audit sheet sampled, filled
// and imported twice (appended once), and the report.
func TestOutcomeCommands(t *testing.T) {
	dir, tmp := t.TempDir(), t.TempDir()
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# demo\n\nSet `X` to 1.\n")
	write("main.go", "package main\n\nconst X = 2\n")
	gitT(t, dir, "init", "-q")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "c")
	head := revParse(t, dir, "HEAD")
	blob := revParse(t, dir, "HEAD:main.go")

	mk := func(text, cat, at string, line int) ledger.Claim {
		return ledger.Claim{
			ID: ledger.ClaimID("demo", "README.md", text), Repo: "demo", Path: "README.md", Line: line, Text: text,
			Category: cat, Verdict: cat, VerifiedAt: at, VerifiedRef: head,
			Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "main.go", Line: 3, Blob: blob}}},
		}
	}
	before := &ledger.Ledger{Claims: []ledger.Claim{
		mk("Set `X` to 1.", "confirmed", "2026-11-20T00:00:00Z", 3),
		mk("Run `make old`.", "contradicted", "2026-11-20T00:00:00Z", 5), // the line was edited since
	}}
	after := &ledger.Ledger{Claims: []ledger.Claim{mk("Set `X` to 1.", "contradicted", "2026-11-27T00:00:00Z", 3)}}
	bp, ap, sp := filepath.Join(tmp, "before.json"), filepath.Join(tmp, "after.json"), filepath.Join(tmp, "l", "outcomes.jsonl")
	for p, l := range map[string]*ledger.Ledger{bp: before, ap: after} {
		if err := l.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	defer func(f func() time.Time) { outcomeNow = f }(outcomeNow)
	outcomeNow = func() time.Time { return time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC) }

	out := runOK(t, "outcome", "diff", "-before", bp, "-after", ap, "-repo-name", "demo", "-stream", sp, dir)
	if !strings.Contains(out, "1 reported, 1 resolved, 1 flips") {
		t.Fatalf("diff: %s", out)
	}

	defer func(f func(context.Context, string, int, string) (string, string, error)) { fetchComment = f }(fetchComment)
	calls := 0
	fetchComment = func(_ context.Context, repo string, pr int, marker string) (string, string, error) {
		calls++
		if repo != "o/demo" || pr != 12 || marker != prreview.Marker {
			t.Fatalf("fetch %s %d %s", repo, pr, marker)
		}
		return "1", prreview.Marker + "\n- [x] a <!-- docsp:fb README.md:3 not_a_problem contradicted read -->\n" +
			"- [ ] b <!-- docsp:fb README.md:3 design_stale contradicted read -->\n", nil
	}
	ticks := []string{"outcome", "ticks", "-github-repo", "o/demo", "-pr", "12", "-ref", head, "-repo-name", "demo", "-ledger", ap, "-stream", sp}
	if out := runOK(t, ticks...); !strings.Contains(out, "1 read, 1 new") {
		t.Fatalf("ticks: %s", out)
	}
	if out := runOK(t, ticks...); !strings.Contains(out, "1 read, 0 new") || calls != 2 {
		t.Fatalf("ticks again: %s", out)
	}

	csvPath := filepath.Join(tmp, "audit.csv")
	runOK(t, "outcome", "sample", "-ledger", ap, "-stream", sp, "-n", "5", "-seed", "3", "-github-repo", "o/demo", "-out", csvPath, dir)
	sheet, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sheet), "const X = 2") || !strings.Contains(string(sheet), "https://github.com/o/demo/blob/"+head+"/README.md#L3") {
		t.Fatalf("sheet:\n%s", sheet)
	}
	filled := strings.Replace(string(sheet), ",,,README.md,3,", ",tp,,README.md,3,", 1)
	if err := os.WriteFile(csvPath, []byte(filled), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 read, 1 new", "1 read, 0 new"} {
		if out := runOK(t, "outcome", "label", "-csv", csvPath, "-stream", sp); !strings.Contains(out, want) {
			t.Fatalf("label: %s (want %s)", out, want)
		}
	}
	if out := runOK(t, "outcome", "sample", "-ledger", ap, "-stream", sp, dir); strings.Count(out, "\n") != 1 {
		t.Fatalf("an audited claim was sampled again:\n%s", out)
	}

	var r outcome.Summary
	if err := json.Unmarshal([]byte(runOK(t, "outcome", "report", "-stream", sp, "-format", "json")), &r); err != nil {
		t.Fatal(err)
	}
	tot := r.Total
	if tot.Reported != 1 || tot.Open != 1 || tot.Resolved[outcome.HowDocEdited] != 1 || tot.Flips != 1 ||
		tot.Labels[outcome.SourcePRTick].FP != 1 || tot.Labels[outcome.SourceProxy].TP != 1 ||
		tot.Precision.N != 1 || tot.Precision.Bad != 0 || tot.Budget != nil {
		t.Fatalf("report: %+v", tot)
	}
	if md := runOK(t, "outcome", "report", "-stream", sp, "-format", "markdown"); !strings.Contains(md, "| demo |") {
		t.Fatalf("markdown: %s", md)
	}
}

func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func runOK(t *testing.T, args ...string) string {
	t.Helper()
	var out, errw bytes.Buffer
	if code := run(context.Background(), args, &out, &errw); code != exitOK {
		t.Fatalf("%v: exit %d: %s", args, code, errw.String())
	}
	return out.String()
}
