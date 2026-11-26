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

	"github.com/MIERAI/docs-platform/internal/consistency"
	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

// TestSLOSplitsATwoRepoSample is the regression test of the first weekly baseline: one
// calibration ledger and one accuracy sample covering two repositories; each repository's
// SLO must count only its own sampled claims and act only on its own documents.
func TestSLOSplitsATwoRepoSample(t *testing.T) {
	tmp := t.TempDir()
	l := &ledger.Ledger{SchemaVersion: ledger.SchemaVersion}
	res := &evalmodel.Result{SchemaVersion: evalmodel.SchemaVersion, Provider: "p", Model: "m"}
	dirs := map[string]string{}
	for _, r := range []struct {
		name, doc string
		wrong     int
	}{{"repo-a", "a-docs.md", 3}, {"repo-b", "b-docs.md", 0}} {
		dir := filepath.Join(tmp, r.name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for i := 1; i <= 4; i++ {
			text.WriteString("line " + r.name + " " + string(rune('0'+i)) + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, r.doc), []byte(text.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "c"}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
		}
		head, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		dirs[r.name] = dir
		for i := 1; i <= 4; i++ {
			txt := "line " + r.name + " " + string(rune('0'+i))
			l.Claims = append(l.Claims, ledger.Claim{ID: ledger.ClaimID(r.name, r.doc, txt), Repo: r.name, Path: r.doc, Line: i, Text: txt,
				Category: "confirmed", Verdict: "no", VerifiedAt: "2026-11-26T00:00:00Z", VerifiedRef: strings.TrimSpace(string(head))})
			v := judgev1.No
			if i <= r.wrong {
				v = judgev1.Yes
			}
			res.Items = append(res.Items, evalmodel.ItemResult{ID: consistency.SampleID(r.name, r.doc, i), Source: consistency.SourceAccuracySample, Verdict: v})
		}
	}
	lf, rf := filepath.Join(tmp, "ledger.json"), filepath.Join(tmp, "result.json")
	if err := l.Save(lf); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if err := os.WriteFile(rf, b, 0o600); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		acc     float64
		actions int
	}{"repo-a": {0.25, 1}, "repo-b": {1, 0}}
	for name, w := range want {
		var out, errb bytes.Buffer
		if code := run(context.Background(), []string{"slo", "-ledger", lf, "-repo-name", name, "-sample-result", rf, "-format", "json", dirs[name]}, &out, &errb); code != exitOK {
			t.Fatalf("%s: exit %d: %s", name, code, errb.String())
		}
		var r consistency.SLOReport
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Accuracy.Value == nil || *r.Accuracy.Value != w.acc || r.Accuracy.N != 4 || len(r.Actions) != w.actions {
			t.Fatalf("%s: accuracy %+v actions %+v", name, r.Accuracy, r.Actions)
		}
		for _, a := range r.Actions {
			if !strings.HasPrefix(a.Path, name[len("repo-"):]+"-") {
				t.Fatalf("%s acts on another repository's document: %s", name, a.Path)
			}
		}
	}
}
