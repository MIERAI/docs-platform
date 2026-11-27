package ledger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/agentic"
)

func quoteAt(p, src string, n int) Quote {
	return Quote{Path: p, Line: n, Blob: "x", Scope: ComputeScope(p, src, n)}
}

func TestCarryQuote(t *testing.T) {
	q := quoteAt("jobs.go", goSrc, 7) // slaSeconds = 300.0
	h := quoteAt("jobs.go", goSrc, 9) // healthOK = "OK"
	f := quoteAt("jobs.go", goSrc, 19)
	for _, tt := range []struct {
		name    string
		q       Quote
		newSrc  string
		kind    string // "" = must be judged again
		newLine int
	}{
		{"sibling const changed", h, strings.Replace(goSrc, "300.0", "600.0", 1), CarryOutsideScope, 9},
		{"quoted const changed", q, strings.Replace(goSrc, "300.0", "600.0", 1), "", 0},
		{"doc comment of the quoted const changed", q, strings.Replace(goSrc, "SLA in seconds", "SLA (s)", 1), "", 0},
		{"whitespace only", f, strings.Replace(goSrc, "if d > slaSeconds", "if d >  slaSeconds", 1), CarryReformatted, 19},
		{"lines inserted above", f, strings.Replace(goSrc, "import \"fmt\"\n", "import \"fmt\"\n\n// x\nvar x = 1\n", 1), CarryMoved, 22},
		{"function moved below another", f, moveRun(goSrc), CarryMoved, 21},
		{"line inside the function added", f, strings.Replace(goSrc, "\treturn fmt.Sprint", "\td++\n\treturn fmt.Sprint", 1), "", 0},
		{"function renamed", f, strings.Replace(goSrc, "func Summary", "func Summary2", 1), "", 0},
		{"rules table sibling entry", quoteAt("jobs.go", goSrc, 13), strings.Replace(goSrc, `"health": 1`, `"health": 2`, 1), CarryOutsideScope, 13},
		{"file rewritten", h, "package jobs\n\nconst (\n\thealthOK = \"OK\"\n)\n", "", 0},
	} {
		c, why := CarryQuote(tt.q, goSrc, tt.newSrc)
		switch {
		case tt.kind == "" && why == "":
			t.Errorf("%s: carried %+v, want judged again", tt.name, c)
		case tt.kind != "" && (why != "" || c.Kind != tt.kind || c.NewLine != tt.newLine):
			t.Errorf("%s: %+v (%s), want %s at %d", tt.name, c, why, tt.kind, tt.newLine)
		}
	}
}

// moveRun moves Run above Summary.
func moveRun(src string) string {
	run := "func (s *Server) Run() {}\n\n"
	src = strings.Replace(src, run, "", 1)
	return strings.Replace(src, "// Summary sums.", strings.TrimSuffix(run, "\n")+"\n// Summary sums.", 1)
}

func TestCarryQuoteYAML(t *testing.T) {
	img := quoteAt("deploy.yaml", yamlSrc, 11)
	for _, tt := range []struct {
		name   string
		newSrc string
		kind   string
	}{
		{"sibling container changed", strings.Replace(yamlSrc, "envoy:1", "envoy:2", 1), CarryOutsideScope},
		{"replicas changed", strings.Replace(yamlSrc, "replicas: 3", "replicas: 4", 1), CarryOutsideScope},
		{"quoted image changed", strings.Replace(yamlSrc, "app:1.2", "app:1.3", 1), ""},
		{"reindented out of its parent", strings.Replace(yamlSrc, "          image: app:1.2", "        image: app:1.2", 1), ""},
		{"parent key renamed", strings.Replace(yamlSrc, "  template:", "  tmpl:", 1), ""},
		{"trailing whitespace", strings.Replace(yamlSrc, "replicas: 3", "replicas: 3  ", 1), CarryReformatted},
	} {
		c, why := CarryQuote(img, yamlSrc, tt.newSrc)
		if (tt.kind == "") != (why != "") || c.Kind != tt.kind {
			t.Errorf("%s: %+v (%s), want %q", tt.name, c, why, tt.kind)
		}
	}
	lines := quoteAt("main.tf", "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", 5)
	if _, why := CarryQuote(lines, "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", "a\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n"); why != "" {
		t.Errorf("edit 5 lines away from a lines scope: %s", why)
	}
	if _, why := CarryQuote(lines, "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", "a\nb\nc\nd\ne\nf\nG\nh\ni\nj\n"); why == "" {
		t.Error("edit 2 lines away from a lines scope was carried")
	}
}

// scopedRepo is a repository with a Go file holding two rules and a document claiming each.
func scopedRepo(t *testing.T) (*gitRepo, *Ledger) {
	t.Helper()
	g := &gitRepo{t: t, dir: t.TempDir()}
	g.git("init", "-q")
	g.write(map[string]string{"README.md": "# Rules\n\nThe SLA is 300 seconds.\nHealthy jobs report OK.\n", "jobs.go": goSrc})
	g.commit("initial")
	ctx := context.Background()
	b, err := NewBuilder(ctx, g.dir, "demo", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	l := &Ledger{SchemaVersion: SchemaVersion}
	for _, lr := range []agentic.LineResult{
		line(3, agentic.Confirmed, ok(agentic.Evidence{Kind: agentic.KindQuote, Path: "jobs.go", Line: 7})),
		line(4, agentic.Confirmed, ok(agentic.Evidence{Kind: agentic.KindQuote, Path: "jobs.go", Line: 9}), ok(agentic.Evidence{Kind: agentic.KindQuote, Path: "jobs.go", Line: 22})),
	} {
		c, good := b.Claim(ctx, &lr)
		if !good {
			t.Fatal("no claim")
		}
		l.Merge(c)
	}
	return g, l
}

func checkAll(t *testing.T, g *gitRepo, l *Ledger) (*Report, map[int]ClaimStatus) {
	t.Helper()
	r, err := Check(context.Background(), l, g.dir, "demo", "HEAD", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if errs := schema(t, contracts.Stale).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("stale/v1: %v", errs)
	}
	out := map[int]ClaimStatus{}
	for _, c := range r.Claims {
		out[c.Line] = c
	}
	return r, out
}

func TestSiblingRuleDoesNotReopenClaim(t *testing.T) {
	g, l := scopedRepo(t)
	b, _ := json.Marshal(l)
	if errs := schema(t, contracts.Ledger).ValidateJSON(b); len(errs) > 0 {
		t.Fatalf("ledger/v1: %v", errs)
	}
	if s := l.Claims[0].Evidence.Quotes[0].Scope; s == nil || s.Kind != ScopeGoDecl {
		t.Fatalf("no scope recorded: %+v", l.Claims[0].Evidence.Quotes[0])
	}
	// the SLA rule changes: the SLA claim is judged again, the health claim carried forward
	g.write(map[string]string{"jobs.go": "package jobs\n\n// moved down\n" + strings.TrimPrefix(strings.Replace(goSrc, "300.0", "600.0", 1), "package jobs\n")})
	g.commit("sla 600")
	r, st := checkAll(t, g, l)
	if st[3].Status != Stale || !hasReason(st[3], ReasonEvidenceChanged) {
		t.Fatalf("SLA claim: %+v", st[3])
	}
	if st[4].Status != Fresh || len(st[4].Carried) != 2 || st[4].Carried[0].Kind != CarryMoved || st[4].Carried[0].NewLine != 11 {
		t.Fatalf("health claim: %+v", st[4])
	}
	// re-anchoring records the new place and blob; the next check needs no classification
	if n := l.Reanchor(r); n != 2 {
		t.Fatalf("re-anchored %d quotes", n)
	}
	var h Claim
	for _, c := range l.Claims {
		if c.Line == 4 {
			h = c
		}
	}
	q := h.Evidence.Quotes[0]
	if q.Line != 11 || q.Carried != CarryMoved || q.CarriedRef != g.git("rev-parse", "HEAD") || q.Scope.Start != 11 {
		t.Fatalf("re-anchored quote %+v", q)
	}
	if _, st := checkAll(t, g, l); st[4].Status != Fresh || len(st[4].Carried) != 0 {
		t.Fatalf("after re-anchoring: %+v", st[4])
	}
	// the function the health claim quotes changes: judged again
	g.write(map[string]string{"jobs.go": strings.Replace(readFile(t, g, "jobs.go"), "fmt.Sprint(healthOK)", "fmt.Sprint(healthOK, 1)", 1)})
	g.commit("health")
	if _, st := checkAll(t, g, l); st[4].Status != Stale {
		t.Fatalf("quoted function changed: %+v", st[4])
	}
}

func TestQuoteWithoutScopeBehavesAsBefore(t *testing.T) {
	g, l := scopedRepo(t)
	for i := range l.Claims {
		for j := range l.Claims[i].Evidence.Quotes {
			l.Claims[i].Evidence.Quotes[j].Scope = nil // a 1.0 ledger
		}
	}
	g.write(map[string]string{"jobs.go": strings.Replace(goSrc, "300.0", "600.0", 1)})
	g.commit("sla 600")
	_, st := checkAll(t, g, l)
	for _, n := range []int{3, 4} {
		if st[n].Status != Stale || len(st[n].Carried) != 0 {
			t.Errorf("line %d: %+v", n, st[n])
		}
	}
	// no repository behind the change set: no classification either
	ch := &Changes{Paths: map[string]byte{"jobs.go": 'M'}}
	c := l.Claims[0]
	c.Evidence.Quotes = []Quote{quoteAt("jobs.go", goSrc, 9)}
	if s := Invalidate(&c, ch, func(string) (string, bool) { return "", false }); s.Status != Stale {
		t.Fatalf("without a repository: %+v", s)
	}
}

func TestEnumeratingClaimIgnoresScope(t *testing.T) {
	g, l := scopedRepo(t)
	for i := range l.Claims {
		l.Claims[i].Text = "Jobs report OK, DEGRADED, FAILED." // a list: its completeness depends on the whole file
	}
	g.write(map[string]string{"jobs.go": strings.Replace(goSrc, "300.0", "600.0", 1)})
	g.commit("sla 600")
	r, err := Check(context.Background(), l, g.dir, "demo", "HEAD", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Claims {
		if s.Status != Stale || len(s.Carried) != 0 {
			t.Errorf("enumerating claim %d: %+v", s.Line, s)
		}
	}
}
