package draft

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

func TestConstrain(t *testing.T) {
	orig := "The API listens on port 8080 and logs to stdout."
	ev := []string{"port: 9090", "docs: see config.yaml"}
	tests := []struct {
		name, draft, action string
		ok                  bool
	}{
		{"value replaced from the evidence", "The API listens on port 9090 and logs to stdout.", ActionReplace, true},
		{"value not in the evidence", "The API listens on port 7070 and logs to stdout.", ActionReplace, false},
		{"extra volatile detail", "The API listens on port 9090 (since v2.3) and logs to stdout.", ActionReplace, false},
		{"detail removed", "The API logs to stdout.", ActionReplace, true},
		{"link instead", "The API listens on the port set in [config.yaml](config.yaml) and logs to stdout.", ActionLink, true},
		{"rewrite", "Requests are served over HTTP; all output goes to standard output streams in JSON form today.", ActionReplace, false},
		{"delete", "DELETE", ActionDelete, true},
		{"no change", orig, ActionReplace, false},
		{"empty", "  ", ActionReplace, false},
	}
	for _, tt := range tests {
		c := Constrain(orig, tt.draft, ev)
		if c.OK != tt.ok || c.Action != tt.action {
			t.Errorf("%s: ok=%v action=%s problems=%v", tt.name, c.OK, c.Action, c.Problems)
		}
	}
	if d := diffSize(tokens("a b c d"), tokens("a x c d e")); d != 3 {
		t.Fatalf("diff %d", d)
	}
}

func TestRequestCarriesOnlyTheWrongFacts(t *testing.T) {
	c := &ledger.Claim{ID: "abc", Repo: "r", Path: "README.md", Line: 3, Text: "Port 8080.", Category: "contradicted",
		Facts: []ledger.Fact{{Fact: "the port is 8080", Status: "contradicted", Valid: true}, {Fact: "it is HTTP", Status: "confirmed", Valid: true},
			{Fact: "unvalidated", Status: "contradicted", Valid: false}}}
	r := Request(c, []judgev1.Evidence{{Path: "config.yaml", From: 1, To: 1, Text: "port: 9090"}})
	u := r.Prompt.User
	if !strings.Contains(u, "- the port is 8080 (contradicted)") || strings.Contains(u, "it is HTTP") || strings.Contains(u, "unvalidated") ||
		!strings.Contains(u, `exactly "DELETE"`) || !strings.Contains(u, "[E1] config.yaml lines 1-1") || r.Task.Type != judgev1.TaskDraftEdit || r.Validate() != nil {
		t.Fatalf("%s", u)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitDoc(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "doc")
}

func TestLogLifecycle(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	commitDoc(t, dir, "# R\nPort 8080.\nOld line.\nLink here.\n")
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	at := now.Format(time.RFC3339)
	l := &Log{Entries: []Entry{
		{ClaimID: "a", Repo: "r", Path: "README.md", Original: "Port 8080.", Draft: "Port 9090.", Action: ActionReplace, DiffTokens: 2, Status: Proposed, ProposedAt: at},
		{ClaimID: "b", Repo: "r", Path: "README.md", Original: "Old line.", Draft: DeleteMarker, Action: ActionDelete, DiffTokens: 3, Status: Proposed, ProposedAt: at},
		{ClaimID: "c", Repo: "r", Path: "README.md", Original: "Link here.", Draft: "See [x](x).", Action: ActionLink, DiffTokens: 6, Status: Proposed, ProposedAt: at},
		{ClaimID: "d", Repo: "r", Path: "README.md", Original: "x", Draft: "y", Status: RejectedByHarness, ProposedAt: at},
	}}
	// a accepted, b deleted as proposed, c edited another way
	commitDoc(t, dir, "# R\nPort 9090.\nLinks are elsewhere.\n")
	if err := l.Track(context.Background(), dir, "r", "HEAD", now); err != nil {
		t.Fatal(err)
	}
	got := []string{l.Entries[0].Status, l.Entries[1].Status, l.Entries[2].Status, l.Entries[3].Status}
	if strings.Join(got, ",") != "accepted,accepted,superseded,rejected_by_harness" {
		t.Fatalf("%v", got)
	}
	// a is reverted
	commitDoc(t, dir, "# R\nPort 8080.\nLinks are elsewhere.\n")
	_ = l.Track(context.Background(), dir, "r", "HEAD", now)
	if l.Entries[0].Status != Reverted {
		t.Fatalf("revert: %s", l.Entries[0].Status)
	}
	s := l.Stats()
	if s.Proposed != 3 || s.RejectedByHarness != 1 || s.Decided != 3 || *s.AcceptanceRate != 2.0/3 || *s.RevertRate != 0.5 || s.MeanDiffTokens != 11.0/3 {
		t.Fatalf("%+v", s)
	}
	if recs := l.Recommend(3, now); len(recs) != 1 || recs[0].Path != "README.md" {
		t.Fatalf("%+v", recs)
	}
	if recs := l.Recommend(3, now.Add(100*24*time.Hour)); len(recs) != 0 {
		t.Fatal("old drafts recommended")
	}
	p := filepath.Join(t.TempDir(), "drafts.jsonl")
	if err := l.Save(p); err != nil {
		t.Fatal(err)
	}
	back, err := LoadLog(p)
	if st, _ := os.Stat(p); err != nil || len(back.Entries) != 4 || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %d", err, len(back.Entries))
	}
	if back.Open("a") || !(&Log{Entries: []Entry{{ClaimID: "z", Status: Proposed}}}).Open("z") {
		t.Fatal("open")
	}
}

func FuzzConstrain(f *testing.F) {
	f.Add("Port 8080 on host-1.", "Port 9090 on host-1.", "port: 9090")
	f.Add("`a` [b](c) 1.2.3", "DELETE", "")
	f.Fuzz(func(t *testing.T, orig, draft, ev string) {
		c := Constrain(orig, draft, []string{ev})
		if c.OK && len(c.Problems) > 0 || c.DiffTokens < 0 {
			t.Fatalf("%+v", c)
		}
	})
}
