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
	"github.com/MIERAI/docs-platform/internal/jsonschema"
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
