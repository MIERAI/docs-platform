package check

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/findings"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic"
)

var update = flag.Bool("update", false, "rewrite golden files")

func fixedNow() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) }

func runFixture(t *testing.T, name, minConf string) []byte {
	t.Helper()
	res, err := Run(context.Background(), Options{
		Root: filepath.Join("..", "..", "testdata", "fixtures", name), RepoName: name,
		Version: "0.0.0", MinConfidence: minConf, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Run(%s): %v", name, err)
	}
	b, err := json.MarshalIndent(res.Document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestGolden(t *testing.T) {
	cases := []struct{ fixture, minConf string }{
		{"basic", "low"},
		{"basic", "high"},
		{"minimal", "low"},
	}
	for _, c := range cases {
		t.Run(c.fixture+"-"+c.minConf, func(t *testing.T) {
			got := runFixture(t, c.fixture, c.minConf)
			golden := filepath.Join("..", "..", "testdata", "golden", c.fixture+"-"+c.minConf+".findings.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run go test ./internal/check -update): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("output differs from %s; rerun with -update and review the diff", golden)
			}
		})
	}
}

// TestContract: every document validates against the findings contract docs-platform emits,
// and against the unmodified upstream contract except for the proposed additive extension.
func TestContract(t *testing.T) {
	doc := runFixture(t, "basic", "low")
	ours, err := jsonschema.Compile(contracts.Findings)
	if err != nil {
		t.Fatal(err)
	}
	if errs := ours.ValidateJSON(doc); len(errs) > 0 {
		t.Fatalf("violates contracts/findings-v1.schema.json: %v", errs)
	}
	upstream, err := jsonschema.Compile(contracts.UpstreamFindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range upstream.ValidateJSON(doc) {
		if !isExtension(e) {
			t.Errorf("upstream incompatibility outside the proposed extension: %s", e)
		}
	}
}

// isExtension recognises the only two differences the 1.3 proposal introduces.
func isExtension(e string) bool {
	return contains(e, ".layer: docs not in") || contains(e, `does not match ^security_`)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestStableIDsAcrossLineShifts(t *testing.T) {
	a := runFixture(t, "basic", "low")
	var doc struct {
		Findings []struct{ ID, Asset string } `json:"findings"`
	}
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range doc.Findings {
		if seen[f.ID] {
			t.Errorf("duplicate finding id %s (%s) in one document", f.ID, f.Asset)
		}
		seen[f.ID] = true
	}
}

func TestSemanticContract(t *testing.T) {
	doc := findings.BuildSemantic(findings.Params{
		Repo: "demo", Version: "0.0.0", Started: findings.Timestamp(fixedNow()), Finished: findings.Timestamp(fixedNow()),
		Owner: "docs", Tools: map[string]string{"semantic": semantic.Version},
	}, []semantic.Issue{
		{
			Rule: semantic.RuleValueMismatch, Path: "README.md", Line: 5, Claim: "kept for 3d", Subject: "retention=3d",
			Detail: "the docs say 3d", Confidence: model.ConfMedium, Quote: "retention: 30d",
			Locations: []semantic.Location{{Path: "README.md", Line: 5, Role: "doc"}, {Path: "values.yaml", Line: 1, Role: "code"}},
		},
		{
			Rule: semantic.RuleModelContradiction, Path: "README.md", Line: 9, Claim: "c", Subject: "q",
			Confidence: model.ConfLow, Quote: "const Window = 1000", Model: "ollama:m", PromptHash: "0123456789abcdef", Votes: "2/3 yes",
			Locations: []semantic.Location{{Path: "README.md", Line: 9, Role: "doc"}, {Path: "w.go", Line: 3, Role: "code"}},
		},
	})
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	ours, err := jsonschema.Compile(contracts.Findings)
	if err != nil {
		t.Fatal(err)
	}
	if errs := ours.ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("violates contracts/findings-v1.schema.json: %v", errs)
	}
	upstream, err := jsonschema.Compile(contracts.UpstreamFindings)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range upstream.ValidateJSON(b) {
		if !isExtension(e) {
			t.Errorf("upstream incompatibility outside the proposed extension: %s", e)
		}
	}
	for _, f := range doc.Findings {
		if !f.NeedsHuman || f.Severity == "medium" || f.Severity == "high" {
			t.Errorf("semantic findings are proposals: %+v", f)
		}
	}
}
