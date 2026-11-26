package contract

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
)

func gitT(t *testing.T, dir string, at time.Time, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	ts := at.UTC().Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitAll(t *testing.T, dir string, at time.Time, msg string) string {
	t.Helper()
	gitT(t, dir, at, "add", "-A")
	gitT(t, dir, at, "commit", "-q", "-m", msg)
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func designDoc(ops map[string]string) string {
	var b strings.Builder
	b.WriteString("openapi: 3.0.0\npaths:\n  /graphql:\n    post:\n      requestBody:\n        content:\n          application/json:\n            examples:\n")
	for _, k := range sortedKeys(ops) {
		b.WriteString("              " + k + ":\n                value:\n                  query: \"" + ops[k] + "\"\n")
	}
	return b.String()
}

func kotlinAPI(funs ...string) string {
	return "package api\n\n@GraphQLApi\nclass Api {\n    @Query\n    fun " + strings.Join(funs, "\n\n    @Query\n    fun ") + "\n}\n"
}

// TestHistoryDirection builds a repository whose history produces every direction class:
//
//	t1: design and code agree on alpha, beta, gamma, delta; code also has epsilon; design zeta
//	t2: code changes alpha(zone) and gamma(level), and deletes delta
//	t3: design changes beta(size) and gamma(level)
func TestHistoryDirection(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2, t3 := t1.AddDate(0, 1, 0), t1.AddDate(0, 2, 0)
	gitT(t, dir, t1, "init", "-q")
	design := map[string]string{
		"a": "query A($zone: Int!) { alpha(zone: $zone) }",
		"b": "query B($size: Int!) { beta(size: $size) }",
		"c": "query C($level: Int!) { gamma(level: $level) }",
		"d": "query { delta }",
		"z": "query { zeta }",
	}
	writeFile(t, dir, "spec/api.yaml", designDoc(design))
	writeFile(t, dir, "svc/src/main/kotlin/Api.kt", kotlinAPI("alpha(zone: Int): Int = 0", "beta(size: Int): Int = 0", "gamma(level: Int): Int = 0", "delta(): Int = 0", "epsilon(): Int = 0"))
	c1 := commitAll(t, dir, t1, "both")
	writeFile(t, dir, "svc/src/main/kotlin/Api.kt", kotlinAPI("alpha(zone: String): Int = 0", "beta(size: Int): Int = 0", "gamma(level: Long): Int = 0", "epsilon(): Int = 0"))
	c2 := commitAll(t, dir, t2, "code moves")
	design["b"] = "query B($size: String!) { beta(size: $size) }"
	design["c"] = "query C($level: String!) { gamma(level: $level) }"
	writeFile(t, dir, "spec/api.yaml", designDoc(design))
	c3 := commitAll(t, dir, t3, "design moves")

	ctx := context.Background()
	ds, err := OpenSource(ctx, "spec", dir, "HEAD", []string{"spec/**"})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := OpenSource(ctx, "code", dir, "HEAD", []string{"svc/**"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Run(ctx, Options{DesignAdapter: "openapi-graphql", CodeAdapter: "kotlin-graphql", Design: ds, Code: cs, History: true, Workers: 4, Authority: AuthorityCode, Generator: "export the schema", Now: t3.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*Direction{}
	for _, it := range doc.Items {
		got[it.Operation+" "+it.Kind+" "+it.Subject] = it.Direction
	}
	want := map[string]string{
		"alpha input.type inputs.zone":         DesignBehindCode,
		"beta input.type inputs.size":          CodeBehindDesign,
		"gamma input.type inputs.level":        Diverged,
		"delta operation.missing_in_code ":     RemovedInCode,
		"epsilon operation.missing_in_design ": Undocumented,
		"zeta operation.missing_in_code ":      CodeBehindDesign,
	}
	if len(got) != len(want) {
		t.Errorf("items %v", keysOf(got))
	}
	for k, class := range want {
		d := got[k]
		if d == nil || d.Class != class {
			t.Errorf("%s: got %+v, want %s", k, d, class)
		}
	}
	if d := got["delta operation.missing_in_code "]; d != nil && (d.RemovedBy == nil || d.RemovedBy.Commit != c2 || d.AddedBy == nil || d.AddedBy.Commit != c1 || d.RemovedBy.Date != t2.Format(time.RFC3339)) {
		t.Errorf("deletion evidence %+v %+v", d.RemovedBy, d.AddedBy)
	}
	if d := got["beta input.type inputs.size"]; d != nil && (d.DesignLast == nil || d.DesignLast.Commit != c3 || d.CodeLast == nil || d.CodeLast.Commit != c1) {
		t.Errorf("beta evidence %+v %+v", d.DesignLast, d.CodeLast)
	}
	for _, it := range doc.Items {
		if !strings.HasPrefix(it.Message, "design needs update: ") || !strings.Contains(it.Recommendation, "export the schema") {
			t.Errorf("authority code: %q / %q", it.Message, it.Recommendation)
		}
	}
	validateDoc(t, doc)
}

// TestHistoryWorktree: an uncommitted change is the newest touch on its side.
func TestHistoryWorktree(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gitT(t, dir, t1, "init", "-q")
	writeFile(t, dir, "spec/api.yaml", designDoc(map[string]string{"a": "query A($zone: Int!) { alpha(zone: $zone) }"}))
	writeFile(t, dir, "svc/src/main/kotlin/Api.kt", kotlinAPI("alpha(zone: Int): Int = 0"))
	commitAll(t, dir, t1, "both")
	writeFile(t, dir, "svc/src/main/kotlin/Api.kt", kotlinAPI("alpha(zone: Long): Int = 0"))
	ctx := context.Background()
	ds, _ := OpenSource(ctx, "r", dir, "", []string{"spec/**"})
	cs, _ := OpenSource(ctx, "r", dir, "", []string{"svc/**"})
	doc, err := Run(ctx, Options{DesignAdapter: "openapi-graphql", CodeAdapter: "kotlin-graphql", Design: ds, Code: cs, History: true, Now: t1.AddDate(0, 0, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 1 || doc.Items[0].Direction.Class != DesignBehindCode || doc.Items[0].Direction.CodeLast.Commit != worktreeCommit {
		t.Fatalf("items %+v", doc.Items)
	}
	validateDoc(t, doc)
}

func keysOf(m map[string]*Direction) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func validateDoc(t *testing.T, doc *Document) {
	t.Helper()
	sch, err := jsonschema.Compile(contracts.Contract)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if errs := sch.ValidateJSON(b); len(errs) > 0 {
		t.Errorf("contract/v1: %v", errs)
	}
}

func TestTermRegex(t *testing.T) {
	if got := termRegex("unit_price"); got != `(^|[^A-Za-z0-9_])(unit_price|unitPrice|UnitPrice)([^A-Za-z0-9_]|$)` {
		t.Errorf("termRegex %s", got)
	}
	if got := termRegex("sku"); got != `(^|[^A-Za-z0-9_])(sku|Sku)([^A-Za-z0-9_]|$)` {
		t.Errorf("termRegex %s", got)
	}
}

func TestBothPresent(t *testing.T) {
	at := func(day int) *Evidence {
		return &Evidence{Commit: "c", Date: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)}
	}
	for _, tc := range []struct {
		dl, cl []*Evidence
		want   string
	}{
		{nil, nil, DirUnknown},
		{[]*Evidence{at(1)}, []*Evidence{at(2)}, DesignBehindCode},
		{[]*Evidence{at(3)}, []*Evidence{at(2)}, CodeBehindDesign},
		{[]*Evidence{at(3)}, []*Evidence{at(4), at(2)}, Diverged},
		{[]*Evidence{at(4), at(1)}, []*Evidence{at(3)}, Diverged},
		{[]*Evidence{at(2)}, []*Evidence{at(2)}, Diverged},
	} {
		d := &Direction{}
		bothPresent(d, tc.dl, tc.cl)
		if d.Class != tc.want {
			t.Errorf("%v / %v: %s want %s", tc.dl, tc.cl, d.Class, tc.want)
		}
	}
}
