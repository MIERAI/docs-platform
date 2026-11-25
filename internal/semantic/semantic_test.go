package semantic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/ai"
	"github.com/MIERAI/docs-platform/internal/extract/markdown"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
)

func TestCronMatches(t *testing.T) {
	cases := []struct {
		expr string
		v    claim.Value
		want bool
	}{
		{"0 0 * * *", claim.Value{Kind: claim.Clock, Num: 9 * 60, TZ: "JST"}, true},
		{"0 0 * * *", claim.Value{Kind: claim.Clock, Num: 9 * 60}, false},
		{"30 1 * * *", claim.Value{Kind: claim.Clock, Num: 90, TZ: "UTC"}, true},
		{"*/2 * * * *", claim.Value{Kind: claim.Every, Num: 120}, true},
		{"*/5 * * * *", claim.Value{Kind: claim.Every, Num: 120}, false},
		{"0 3 * * *", claim.Value{Kind: claim.Every, Num: 86400}, true},
		{"0 3 * * 1", claim.Value{Kind: claim.Every, Num: 86400}, false},
		{"0 3 2 * *", claim.Value{Kind: claim.Every, Num: 2592000}, true},
		{"0 9 * * 1-5", claim.Value{Kind: claim.Cron, Text: "0 9 * * 1-5"}, true},
		{"0 */6 * * *", claim.Value{Kind: claim.Every, Num: 6 * 3600}, true},
	}
	for _, c := range cases {
		if got := cronMatches(c.expr, c.v); got != c.want {
			t.Errorf("cronMatches(%q, %+v) = %v, want %v", c.expr, c.v, got, c.want)
		}
	}
}

func TestParseReplyAndQuote(t *testing.T) {
	snips := []snippet{{Path: "a.go", From: 1, Text: "const keep = 30 * 24 * time.Hour\nfunc x() {}"}}
	cases := []struct {
		reply, ans string
		verified   bool
	}{
		{`{"answer":"yes","quote":"const keep = 30 * 24 * time.Hour"}`, "yes", true},
		{`{"answer":"yes","quote":"func x() {}"}`, "yes", false},   // shares no word with the claim
		{`{"answer":"yes","quote":"keep = 3 days"}`, "yes", false}, // not in the evidence
		{`{"answer":"yes","quote":"func x"}`, "yes", false},        // too short
		{`{"answer":"yes","quote":"retention (keep) is 3 days"}`, "yes", false},
		{`{"answer":"NO","quote":""}`, "no", false},
		{`answer: unknown`, "unknown", false},
		{`garbage`, "unknown", false},
	}
	for _, c := range cases {
		ans, q := parseReply(c.reply)
		_, ok := verifyQuote(q, "retention (keep) is 3 days", snips)
		if ans != c.ans || ok != c.verified {
			t.Errorf("%s: answer %q verified %v", c.reply, ans, ok)
		}
	}
}

func TestRotate(t *testing.T) {
	s := []snippet{{Path: "a"}, {Path: "b"}, {Path: "c"}}
	seen := map[string]bool{}
	for k := range 3 {
		var p []string
		for _, x := range rotate(s, k) {
			p = append(p, x.Path)
		}
		if len(p) != 3 {
			t.Fatalf("rotate lost snippets: %v", p)
		}
		seen[strings.Join(p, "")] = true
	}
	if len(seen) != 3 {
		t.Errorf("orderings are not distinct: %v", seen)
	}
}

// fakeJudge answers from a function and counts calls.
type fakeJudge struct {
	mu    sync.Mutex
	n     int
	reply func(prompt string) string
}

func (*fakeJudge) Name() string  { return "fake" }
func (*fakeJudge) Model() string { return "m" }
func (f *fakeJudge) Used() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeJudge) Ask(_ context.Context, q ai.Question) (ai.Reply, error) {
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return ai.Reply{Text: f.reply(q.Prompt)}, nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, dir, p, content string) {
	t.Helper()
	full := filepath.Join(dir, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string, at time.Time) {
	t.Helper()
	git(t, dir, "add", "-A")
	cmd := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "-m", msg)
	ts := at.Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
}

// fixtureRepo builds a small git repository whose docs drift from its code over 3 commits.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	write(t, dir, "README.md", "# Demo\n\n状态: 实现中\n\n"+
		"Logs are kept for `retention: 3d`.\n\n"+
		"The `nightly.yml` workflow runs daily at 03:00 UTC.\n\n"+
		"Export to `exporter.go` is not implemented yet.\n\n"+
		"Tracing (v0.2.1) is on by default.\n\n"+
		"The `Window` constant in `window.go` holds 100 samples.\n")
	write(t, dir, "values.yaml", "retention: 30d\n")
	write(t, dir, ".github/workflows/nightly.yml", "on:\n  schedule:\n    - cron: '0 3 * * *'\n")
	write(t, dir, "window.go", "package demo\n\nconst Window = 100\n")
	commit(t, dir, "init", t0)
	git(t, dir, "tag", "v0.2.0")
	write(t, dir, "exporter.go", "package demo\n\nfunc Export() {}\n")
	write(t, dir, ".github/workflows/nightly.yml", "on:\n  workflow_dispatch: {}\n#  schedule:\n#    - cron: '0 3 * * *'\n")
	commit(t, dir, "exporter", t0.Add(24*time.Hour))
	write(t, dir, "window.go", "package demo\n\n// Window is the sample size.\nconst Window = 1000\n")
	commit(t, dir, "bigger window", t0.Add(48*time.Hour))
	write(t, dir, "window.go", "package demo\n\n// Window is the sample size of the hot-key monitor.\nconst Window = 1000\n")
	commit(t, dir, "doc window", t0.Add(72*time.Hour))
	git(t, dir, "tag", "v0.3.0")
	return dir
}

func runFixture(t *testing.T, judge ai.Asker) *Result {
	t.Helper()
	dir := fixtureRepo(t)
	r, err := repo.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv := scan.Run(r, []plugin.Extractor{markdown.Extractor{}}, scan.Options{Docs: []string{"*.md"}})
	res, err := Run(context.Background(), r, inv, Options{StaleCommits: 2, Judge: judge})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRunDeterministic(t *testing.T) {
	res := runFixture(t, nil)
	got := map[string]int{}
	for _, is := range res.Issues {
		got[is.Rule] = is.Line
		if len(is.Locations) == 0 || is.Locations[0].Role != "doc" {
			t.Errorf("%s: first location must be the doc: %+v", is.Rule, is.Locations)
		}
	}
	want := map[string]int{
		RuleValueMismatch: 5, RuleScheduleDisabled: 7, RuleStateContradiction: 9,
		RuleVersionUnreleased: 11, RuleStaleClaim: 13,
	}
	for rule, line := range want {
		if got[rule] != line {
			t.Errorf("rule %s: line %d, want %d (all: %+v)", rule, got[rule], line, res.Issues)
		}
	}
	if res.Stats.ModelRequests != 0 || res.Stats.GitCalls == 0 {
		t.Errorf("stats %+v", res.Stats)
	}
}

func TestRunJudge(t *testing.T) {
	// The judge claims a contradiction with a quote that is in the evidence: accepted.
	good := &fakeJudge{reply: func(string) string {
		return `{"answer":"yes","quote":"const Window = 1000"}`
	}}
	res := runFixture(t, good)
	var model *Issue
	for i := range res.Issues {
		if res.Issues[i].Rule == RuleModelContradiction {
			model = &res.Issues[i]
		}
	}
	if model == nil || model.Line != 13 || model.Quote != "const Window = 1000" || model.Votes != "2/2 yes" || model.PromptHash == "" {
		t.Fatalf("model issue %+v (all %+v)", model, res.Issues)
	}
	// A hallucinated quote is discarded: no model finding, rejections counted.
	bad := &fakeJudge{reply: func(string) string { return `{"answer":"yes","quote":"const Window = 5"}` }}
	res = runFixture(t, bad)
	for _, is := range res.Issues {
		if is.Rule == RuleModelContradiction {
			t.Fatalf("unverified quote accepted: %+v", is)
		}
	}
	if res.Stats.QuotesRejected == 0 {
		t.Errorf("rejections not counted: %+v", res.Stats)
	}
}

func TestPromptHashStable(t *testing.T) {
	a, b := PromptHash(), PromptHash()
	if a != b || len(a) != 16 {
		t.Error("prompt hash must be stable")
	}
}

func FuzzParseReply(f *testing.F) {
	f.Add(`{"answer":"yes","quote":"const Window = 1000"}`, "claim")
	f.Add(`answer: maybe`, "")
	f.Fuzz(func(t *testing.T, reply, claimText string) {
		ans, q := parseReply(reply)
		if ans != "yes" && ans != "no" && ans != "unknown" && ans != "" && !strings.Contains(strings.ToLower(reply), ans) {
			t.Fatalf("answer %q not from reply", ans)
		}
		s := []snippet{{Path: "x", From: 1, Text: "const Window = 1000"}}
		if got, ok := verifyQuote(q, claimText, s); ok && !strings.Contains(got.Text, strings.TrimSpace(q)) {
			t.Fatalf("verified quote %q not in snippet", q)
		}
	})
}
