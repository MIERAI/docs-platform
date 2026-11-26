package ledger

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/judgev1"
)

func step(tool, args string, failed bool) judgev1.Step {
	s := judgev1.Step{Tool: tool, Args: json.RawMessage(args)}
	if failed {
		s.Error = "boom"
	}
	return s
}

func TestFootprintsFromTranscript(t *testing.T) {
	steps := []judgev1.Step{
		step(judgev1.ToolSearch, `{"pattern":"x","scope":"*"}`, false),          // whole repository: no footprint
		step(judgev1.ToolSearch, `{"pattern":"x","scope":"clusters/*"}`, false), // scoped
		step(judgev1.ToolGrep, `{"pattern":"y","path":"platform/slo"}`, false),  // scoped
		step(judgev1.ToolGlob, `{"pattern":"**/app.yaml"}`, false),              // scoped, a member set too
		step(judgev1.ToolSearch, `{"pattern":"z","scope":"docs/*"}`, true),      // failed: no footprint
		step(judgev1.ToolListDir, `{"path":"."}`, false),                        // member set
		step(judgev1.ToolReadFile, `{"path":"config.yaml"}`, false),             // a read is not a footprint
		step(judgev1.ToolSearch, `{"pattern":"x","scope":"clusters/*"}`, false), // duplicate
	}
	w, m := Footprints(steps, Rules{Weak: true, Members: true}, true)
	if len(w) != 3 || w[0].Scope != "clusters/*" || w[1].Scope != "platform/slo" || w[2].Scope != "**/app.yaml" {
		t.Fatalf("weak %+v", w)
	}
	if len(m) != 2 || m[0].Glob != "**/app.yaml" || m[1].Dir != "." {
		t.Fatalf("members %+v", m)
	}
	if _, m := Footprints(steps, Rules{Members: true}, false); len(m) != 0 {
		t.Fatal("member sets recorded for a claim that enumerates nothing")
	}
	if w, m := Footprints(steps, Rules{}, true); w != nil || m != nil {
		t.Fatal("footprints recorded with every rule off")
	}
	r, err := ParseRules("weak, members")
	if err != nil || !r.Weak || r.Siblings || !r.Members || r.String() != "weak+members" {
		t.Fatalf("%+v %v", r, err)
	}
	if r, _ := ParseRules("all"); !r.Siblings || !r.Weak || !r.Members {
		t.Fatal("all")
	}
	if _, err := ParseRules("vibes"); err == nil {
		t.Fatal("unknown rule accepted")
	}
	if !Enumerating(&Claim{Text: "jobs: build, test, lint"}) || Enumerating(&Claim{Text: "port 8080"}) ||
		!Enumerating(&Claim{Text: "x", Facts: []Fact{{Status: "incomplete_list"}}}) {
		t.Fatal("enumerating")
	}
}

func TestFootprintRulesOnHistory(t *testing.T) {
	g := newRepo(t)
	g.write(map[string]string{"clusters/lab/extras/log.yaml": "x\n", "apps/a/app.yaml": "a\n"})
	base := g.commit("lab only")
	c := Claim{ID: "c", Repo: "demo", Path: "clusters/lab/extras/log.yaml", Line: 1, Text: "x", Category: "unverifiable_in_repo",
		VerifiedRef: base, Evidence: Evidence{
			Quotes:  []Quote{{Path: "config.yaml", Line: 1}},
			Weak:    []Scope{{Tool: judgev1.ToolSearch, Scope: "platform/*"}},
			Members: []Member{{Dir: "apps"}, {Glob: "**/app.yaml"}},
		}}
	tests := []struct {
		name   string
		files  map[string]string
		rules  Rules
		reason string
	}{
		{"sibling added", map[string]string{"clusters/laptop/extras/log.yaml": "y\n"}, Rules{Siblings: true}, ReasonSiblingAdd},
		{"sibling rule off", map[string]string{"clusters/prod/extras/log.yaml": "y\n"}, Rules{Weak: true, Members: true}, ""},
		{"file changed in a searched scope", map[string]string{"platform/slo/a.yaml": "y\n"}, Rules{Weak: true}, ReasonWeakScope},
		{"weak rule off", map[string]string{"platform/slo/b.yaml": "y\n"}, Rules{}, ""},
		{"new member directory", map[string]string{"apps/b/app.yaml": "b\n"}, Rules{Members: true}, ReasonMemberSet},
		{"change inside an existing member", map[string]string{"apps/a/other.txt": "x\n"}, Rules{Members: true}, ""},
		{"unrelated", map[string]string{"elsewhere/z.txt": "z\n"}, Rules{Weak: true, Siblings: true, Members: true}, ""},
	}
	for _, tt := range tests {
		from := g.git("rev-parse", "HEAD")
		g.write(tt.files)
		to := g.commit(tt.name)
		ch, err := Diff(context.Background(), g.dir, from, to)
		if err != nil {
			t.Fatal(err)
		}
		s := InvalidateWith(&c, ch, ch, func(string) (string, bool) { return "", false }, tt.rules)
		got := ""
		if len(s.Reasons) > 0 {
			got = s.Reasons[0].Kind
		}
		if got != tt.reason || (tt.reason != "" && s.Status != Stale) {
			t.Errorf("%s: %s %+v", tt.name, s.Status, s.Reasons)
		}
		// rules off: exactly the evidence-only answer
		if off := Invalidate(&c, ch, func(string) (string, bool) { return "", false }); off.Status != Recheck && off.Status != Fresh {
			t.Errorf("%s: footprints changed the default: %+v", tt.name, off)
		}
	}
}

func TestBuilderRecordsFootprintsOnlyWhenAsked(t *testing.T) {
	g := newRepo(t)
	lr := line(4, agentic.IncompleteList, ok(agentic.Evidence{Kind: agentic.KindQuote, Path: ".github/workflows/ci.yml", Line: 4, Quote: "lint: {}"}))
	lr.Transcript = []judgev1.Step{step(judgev1.ToolSearch, `{"pattern":"jobs","scope":".github/workflows/*"}`, false), step(judgev1.ToolListDir, `{"path":".github/workflows"}`, false)}
	b, err := NewBuilder(context.Background(), g.dir, "demo", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := b.Claim(context.Background(), &lr)
	if c.Evidence.Weak != nil || c.Evidence.Members != nil {
		t.Fatalf("recorded without rules: %+v", c.Evidence)
	}
	b.Rules = Rules{Weak: true, Members: true}
	c, _ = b.Claim(context.Background(), &lr)
	if len(c.Evidence.Weak) != 1 || len(c.Evidence.Members) != 1 || c.Evidence.Members[0].Dir != ".github/workflows" {
		t.Fatalf("%+v", c.Evidence)
	}
}
