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
	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

func TestDraftVerifyAndApply(t *testing.T) {
	dir := t.TempDir()
	doc := "# Demo\n\nThe server listens on port 8080.\n"
	for p, c := range map[string]string{"config.yaml": "port: 9090\n", "README.md": doc} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitc := func(a ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e"}, a...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitc("init", "-q")
	gitc("add", "-A")
	gitc("commit", "-qm", "c")
	name := filepath.Base(dir)
	line := "The server listens on port 8080."
	c := ledger.Claim{
		ID: ledger.ClaimID(name, "README.md", line), Repo: name, Path: "README.md", Line: 3, Text: line,
		DocBlob: gitc("rev-parse", "HEAD:README.md"), Category: "contradicted", VerifiedRef: gitc("rev-parse", "HEAD"),
		Evidence: ledger.Evidence{Quotes: []ledger.Quote{{Path: "config.yaml", Line: 1, Blob: gitc("rev-parse", "HEAD:config.yaml")}}},
	}
	work := t.TempDir()
	lf, logf := filepath.Join(work, "ledger.json"), filepath.Join(work, "drafts.jsonl")
	if err := (&ledger.Ledger{SchemaVersion: ledger.SchemaVersion, Claims: []ledger.Claim{c}}).Save(lf); err != nil {
		t.Fatal(err)
	}
	dl := &draft.Log{Entries: []draft.Entry{
		{ClaimID: c.ID, Repo: name, Path: "README.md", Line: 3, Original: line, Draft: "The server listens on port 9090.", Action: draft.ActionReplace, Status: draft.Proposed, DocBlob: c.DocBlob},
	}}
	if err := dl.Save(logf); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run(context.Background(), []string{"draft", "verify", "-log", logf, "-ledger", lf, "-format", "json", "-no-sandbox", dir}, &out, &errb)
	if code != exitOK {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	s, err := jsonschema.Compile(contracts.DraftVerify)
	if err != nil {
		t.Fatal(err)
	}
	if errs := s.ValidateJSON(out.Bytes()); len(errs) > 0 {
		t.Fatalf("draft-verify/v1: %v", errs)
	}
	var v verifyOutput
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || len(v.Results) != 1 || !v.Results[0].Verified {
		t.Fatalf("%v %s", err, out.String())
	}

	out.Reset()
	if code := run(context.Background(), []string{"draft", "apply", "-log", logf, "-claim", c.ID, "-ledger", lf, dir}, &out, &errb); code != exitOK {
		t.Fatalf("apply: exit %d %s", code, errb.String())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "README.md")); !strings.Contains(string(b), "port 9090.") {
		t.Fatalf("not applied: %s", b)
	}
	// the document moved: verify fails the guard (the line is gone), apply refuses
	out.Reset()
	if code := run(context.Background(), []string{"draft", "verify", "-log", logf, "-format", "text", "-no-sandbox", dir}, &out, &errb); code != exitFindings || !strings.Contains(out.String(), "NOT verified") {
		t.Fatalf("verify after apply: exit %d %s", code, out.String())
	}
	if code, _ := cmdDraftApply(context.Background(), []string{"-log", logf, "-claim", c.ID, dir}, &out, &errb); code != exitFindings {
		t.Fatalf("a second apply must be refused: exit %d", code)
	}
	if code := run(context.Background(), []string{"draft", "verify", dir}, &out, &errb); code != exitUsage {
		t.Fatalf("no drafts given: exit %d", code)
	}
}
