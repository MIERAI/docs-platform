package backlog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/findings"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
)

const sample = `{"repo": "o/r", "file": "README.md", "line": 13, "claim": "state = R3", "actual": "R5 done", "type": "obsolete", "fix": "say R5", "evidence_paths": ["a.go"]}

{"repo": "o/other", "file": "x.md", "line": null, "claim": "c", "type": "missing"}
{"repo": "o/r", "file": "docs/a.md", "line": null, "claim": "c2", "type": "wrong", "extra": 1}
`

func TestParseAndBuild(t *testing.T) {
	items, err := Parse([]byte(sample))
	if err != nil || len(items) != 3 {
		t.Fatalf("%v %d", err, len(items))
	}
	p := findings.Params{Repo: "r", Version: "0.3.0", Started: "2026-11-25T00:00:00Z", Finished: "2026-11-25T00:00:01Z", Owner: "o"}
	doc, skipped := Build(p, "o/r", items)
	if skipped != 1 || len(doc.Findings) != 2 {
		t.Fatalf("skipped %d findings %d", skipped, len(doc.Findings))
	}
	f := doc.Findings[0]
	if f.Check != "docs.backlog.obsolete" || f.Severity != "low" || f.NeedsHuman || !strings.HasPrefix(f.Asset, "doc:r/README.md#backlog:") {
		t.Fatalf("finding %+v", f)
	}
	again, _ := Build(p, "o/r", items)
	if again.Findings[0].ID != f.ID {
		t.Fatal("import is not idempotent")
	}
	sch, err := jsonschema.Compile(contracts.Findings)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	if errs := sch.ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("contract: %v", errs)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"bad json", "{", "line 1"},
		{"no claim", `{"file":"a.md","type":"wrong"}`, "required"},
		{"bad type", `{"file":"a.md","claim":"c","type":"typo"}`, "want wrong"},
		{"absolute", `{"file":"/etc/x","claim":"c","type":"wrong"}`, "relative"},
		{"traversal", `{"file":"a/../../x","claim":"c","type":"wrong"}`, "relative"},
		{"negative line", `{"file":"a.md","line":-1,"claim":"c","type":"wrong"}`, "negative"},
	}
	for _, tt := range tests {
		if _, err := Parse([]byte(tt.in)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(sample))
	f.Add([]byte(`{"file":"a","claim":"b","type":"wrong","line":3}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		items, err := Parse(b)
		if err != nil {
			return
		}
		doc, _ := Build(findings.Params{Repo: "r"}, "", items)
		if len(doc.Findings) != len(items) {
			t.Fatal("items lost")
		}
	})
}
