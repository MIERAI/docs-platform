package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

// drafter answers draft requests with a canned text chosen by the claim.
type drafter struct{ text map[string]string }

func (drafter) Name() string    { return "drafter" }
func (drafter) Model() string   { return "m" }
func (drafter) Cloud() bool     { return false }
func (drafter) Billing() string { return judgev1.BillingNone }
func (drafter) Close() error    { return nil }
func (drafter) Modes() []string { return []string{judgev1.ModeSnippet} }
func (d drafter) Judge(_ context.Context, r *judgev1.Request) (*judgev1.Response, error) {
	a := judgev1.Answer{Verdict: judgev1.Unknown, Draft: d.text[r.Claim.Text]}
	if a.Draft != "" {
		a.Verdict = judgev1.Yes
	}
	return judgev1.NewResponse(r, a, judgev1.Model{Provider: "drafter", ID: "m"}, judgev1.Usage{}), nil
}

func TestProposeDraft(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("port: 9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "c"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	head, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	g, err := judge.NewGuard("draft", drafter{text: map[string]string{
		"Port 8080.":    "Port 9090.",
		"Port 8080 ok.": "Port 7070 ok, version 2.",
	}}, &instance.Instance{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(text string) *ledger.Claim {
		return &ledger.Claim{ID: text, Repo: "r", Path: "README.md", Line: 3, Text: text, Category: "contradicted", VerifiedRef: strings.TrimSpace(string(head)),
			Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "config.yaml", Line: 1}}}}
	}
	now := time.Now()
	if e := proposeDraft(context.Background(), g, dir, mk("Port 8080."), now); e.Status != draft.Proposed || e.Draft != "Port 9090." || e.DiffTokens != 2 {
		t.Fatalf("%+v", e)
	}
	if e := proposeDraft(context.Background(), g, dir, mk("Port 8080 ok."), now); e.Status != draft.RejectedByHarness || len(e.Problems) == 0 {
		t.Fatalf("%+v", e)
	}
	if e := proposeDraft(context.Background(), g, dir, mk("Nothing known."), now); e.Status != draft.RejectedByHarness || !strings.Contains(e.Problems[0], "no correction") {
		t.Fatalf("%+v", e)
	}
}
