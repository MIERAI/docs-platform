package agentic

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// TestMain lets the test binary act as an agentic judge/v1 provider over the exec transport
// (DOCSP_FAKE=agentic): canned answers, but the drift commit and a quote are taken from real
// tool results, so the tool round trip is exercised.
func TestMain(m *testing.M) {
	if os.Getenv("DOCSP_FAKE") == "agentic" {
		fakeAgentic()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func privatePath() instance.PathClass {
	return instance.PathClass{Repo: "demo", Glob: "private/**", Class: instance.Private}
}

type fakeIO struct {
	sc  *bufio.Scanner
	out *bufio.Writer
}

func (f *fakeIO) send(v any) {
	b, _ := json.Marshal(v)
	_, _ = f.out.Write(append(b, '\n'))
	_ = f.out.Flush()
}

// tool makes one tool call and returns the result content.
func (f *fakeIO) tool(req *judgev1.Request, name string, args any) json.RawMessage {
	a, _ := json.Marshal(args)
	f.send(judgev1.ToolCall{Type: "tool_call", ID: req.ID, Call: name, Tool: name, Args: a})
	if !f.sc.Scan() {
		os.Exit(3)
	}
	var r judgev1.ToolResult
	_ = json.Unmarshal(f.sc.Bytes(), &r)
	return r.Content
}

func fakeAgentic() {
	f := &fakeIO{sc: bufio.NewScanner(os.Stdin), out: bufio.NewWriter(os.Stdout)}
	f.sc.Buffer(make([]byte, 1<<20), 1<<24)
	for f.sc.Scan() {
		var req judgev1.Request
		if json.Unmarshal(f.sc.Bytes(), &req) != nil {
			fmt.Println("garbage")
			continue
		}
		var answer any
		switch req.Task.Type {
		case judgev1.TaskRepoDrift:
			answer = fakeDrift(f, &req)
		case judgev1.TaskClaimFacts:
			answer = fakeLine(req.Claim.Line)
		case judgev1.TaskDocFacts:
			var lines []any
			for _, c := range req.Claims {
				l := fakeLine(c.Line)
				l["line"] = c.Line
				lines = append(lines, l)
			}
			answer = map[string]any{"lines": lines}
		}
		m := judgev1.Model{Provider: "fake", ID: "agent"}
		u := judgev1.Usage{InputTokens: 1000, OutputTokens: 100, CostUSD: 0.01}
		if strings.Contains(req.Prompt.User, "UNPARSABLE") {
			obj, parse := judgev1.ParseObject(nil, "I think it is fine.")
			f.send(judgev1.NewStructuredResponse(&req, obj, parse, m, u))
			continue
		}
		b, _ := json.Marshal(answer)
		// free text around the object: the tolerant parser's job
		obj, parse := judgev1.ParseObject(nil, "Here is the answer:\n```json\n"+string(b)+"\n```")
		f.send(judgev1.NewStructuredResponse(&req, obj, parse, m, u))
	}
}

func fakeDrift(f *fakeIO, req *judgev1.Request) any {
	var ch struct {
		Changes []struct {
			SHA, Path, Status string
		} `json:"changes"`
	}
	_ = json.Unmarshal(f.tool(req, judgev1.ToolGitChanges, map[string]any{"kind": "R"}), &ch)
	commit := "0000000"
	if len(ch.Changes) > 0 {
		commit = ch.Changes[0].SHA
	}
	return map[string]any{"drift": []any{
		map[string]any{"kind": "moved", "old": "old/run.sh", "new": "scripts/run.sh", "summary": "the start script moved", "commits": []string{commit}},
		map[string]any{"kind": "changed_value", "old": "port 1111", "new": "port 2222", "summary": "invented", "commits": []string{"deadbeef"},
			"evidence": []any{map[string]any{"path": "config.yaml", "line": 1, "quote": "port: 2222"}}},
	}}
}

func fakeLine(line int) map[string]any {
	ev := func(e ...map[string]any) []map[string]any { return e }
	fact := func(status string, e []map[string]any) map[string]any {
		return map[string]any{"fact": status + " fact", "status": status, "evidence": e}
	}
	switch line {
	case 3:
		return map[string]any{"facts": []any{
			fact(Contradicted, ev(map[string]any{"kind": "drift", "drift_id": "D1"})),
			fact(Contradicted, ev(map[string]any{"kind": "quote", "path": "config.yaml", "line": 1, "quote": "port: 9090"})),
		}, "reason": "moved and port changed"}
	case 4:
		return map[string]any{"facts": []any{fact(DanglingReference, ev(map[string]any{"kind": "path_missing", "path": "ENVIRONMENTS.md"}))}, "reason": "no such file"}
	case 5:
		return map[string]any{"facts": []any{fact(IncompleteList, ev(map[string]any{"kind": "quote", "path": ".github/workflows/ci.yml", "line": 4, "quote": "lint: {}"}))}, "reason": "lint is missing"}
	case 6:
		return map[string]any{"facts": []any{fact(Confirmed, ev(map[string]any{"kind": "quote", "path": "main.go", "line": 1, "quote": "package mian"}))}, "reason": "typo in quote"}
	}
	return map[string]any{"facts": []any{fact(UnverifiableInRepo, nil)}, "reason": "outside"}
}

func execGuard(t *testing.T, cloud bool) *judge.Guard {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCSP_FAKE", "agentic")
	p, err := judge.NewExec(judge.ExecConfig{Name: "exec", Model: "agent", Command: []string{exe}, PassEnv: []string{"DOCSP_FAKE"},
		Cloud: cloud, Modes: []string{judgev1.ModeAgentic}, Billing: judgev1.BillingAPI, Timeout: 20 * time.Second, MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Parse([]byte("version: 1\nclassification: {repos: {demo: public}, paths: [{repo: demo, glob: 'private/**', class: private}]}\nai: {judge: {budget: {max_usd: 10}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := judge.NewGuard("judge", p, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func runner(g *judge.Guard, dir, cache, group string) *Runner {
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {demo: public}, paths: [{repo: demo, glob: 'private/**', class: private}]}\n"))
	return New(Options{Guard: g, Repo: "demo", Dir: dir, Class: in.Classification, CacheDir: cache, Settings: Settings{Group: group}})
}

// TestEndToEndOffline runs the whole agentic pipeline through the exec transport: drift scan
// (validated, cached by HEAD), per-line and per-document judgments, verdict derivation.
func TestEndToEndOffline(t *testing.T) {
	dir, _ := fixture(t)
	cache := t.TempDir()
	g := execGuard(t, true)
	ctx := context.Background()
	r := runner(g, dir, cache, GroupLine)
	d, err := r.Drift(ctx)
	if err != nil || d.Cached || len(d.Raw) != 2 || len(d.Entries) != 1 || d.Entries[0].ID != "D1" || d.Parse != judgev1.ParseFallback {
		t.Fatalf("drift: %v %+v", err, d)
	}
	files, _ := filepath.Glob(filepath.Join(cache, "demo-*.json"))
	if len(files) != 1 {
		t.Fatalf("cache files %v", files)
	}
	if st, err := os.Stat(files[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode: %v %v", st.Mode(), err)
	}
	want := map[int]string{3: Contradicted, 4: DanglingReference, 5: IncompleteList, 6: Unknown, 1: UnverifiableInRepo}
	for _, group := range []string{GroupLine, GroupDoc} {
		r := runner(g, dir, cache, group)
		res := r.Judge(ctx, "README.md", []int{6, 5, 4, 3, 1, 99})
		if len(res) != 6 || res[0].Line != 1 || res[5].Line != 99 || res[5].Error == "" {
			t.Fatalf("%s: results %+v", group, res)
		}
		for _, lr := range res[:5] {
			if lr.Category != want[lr.Line] || lr.Error != "" {
				t.Errorf("%s line %d: %s (%s), want %s", group, lr.Line, lr.Category, lr.Error, want[lr.Line])
			}
		}
		if res[3].Verdict != judgev1.Yes || res[0].Verdict != judgev1.Unknown {
			t.Errorf("%s: verdicts %s %s", group, res[3].Verdict, res[0].Verdict)
		}
		if dd, _ := r.Drift(ctx); !dd.Cached || len(dd.Entries) != 1 {
			t.Errorf("%s: drift not served from the cache: %+v", group, dd)
		}
	}
	st := g.Stats()
	// 1 drift scan + 5 line requests + 1 doc request; the drift scans of the second and third
	// runner came from the cache
	if st.Requests != 7 || st.ParseFallbacks != 7 || st.ParseFailures != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestEndToEndRefusesPrivateForCloud(t *testing.T) {
	dir, _ := fixture(t)
	g := execGuard(t, true)
	r := runner(g, dir, "", GroupLine)
	res := r.Judge(context.Background(), "private/notes.md", []int{1})
	if len(res) != 1 || !strings.Contains(res[0].Error, judge.ErrClassified.Error()) || g.Stats().Refused != 1 {
		t.Fatalf("private document sent to a cloud provider: %+v", res)
	}
}

func TestEndToEndParseFailureCounted(t *testing.T) {
	dir, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(dir, "NOTE.md"), []byte("UNPARSABLE line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := execGuard(t, false)
	r := New(Options{Guard: g, Repo: "demo", Dir: dir, NoDrift: true})
	res := r.Judge(context.Background(), "NOTE.md", []int{1})
	if res[0].Parse != judgev1.ParseFailed || res[0].Category != Unknown || g.Stats().ParseFailures != 1 {
		t.Fatalf("%+v %+v", res[0], g.Stats())
	}
}

func TestDryRunCallsNoModel(t *testing.T) {
	dir, _ := fixture(t)
	g := execGuard(t, true)
	in, _ := instance.Parse([]byte("version: 1\nclassification: {repos: {demo: public}}\n"))
	r := New(Options{Guard: g, Repo: "demo", Dir: dir, Class: in.Classification, DryRun: true})
	d, err := r.Drift(context.Background())
	if err != nil || len(d.Raw) != 0 {
		t.Fatalf("%v %+v", err, d)
	}
	res := r.Judge(context.Background(), "README.md", []int{3})
	st := g.Stats()
	if res[0].Category != Unknown || !st.Estimated || st.Requests != 2 || st.InputTokens < DefaultLineSteps*1000 {
		t.Fatalf("%+v %+v", res[0], st)
	}
}

func TestHeadAndCacheKey(t *testing.T) {
	dir, _ := fixture(t)
	h, err := Head(context.Background(), dir)
	if err != nil || len(h) != 40 {
		t.Fatalf("%q %v", h, err)
	}
	a, b := cacheFile("/c", "demo", h, "p:m1"), cacheFile("/c", "demo", h, "p:m2")
	if a == b || !strings.HasPrefix(filepath.Base(a), "demo-"+h[:12]) {
		t.Fatalf("%s %s", a, b)
	}
	if _, err := Head(context.Background(), t.TempDir()); err == nil {
		t.Fatal("HEAD of a non-repository")
	}
	if _, ok := loadCache(filepath.Join(t.TempDir(), "missing.json")); ok {
		t.Fatal("missing cache loaded")
	}
	if !errors.Is(nil, nil) {
		t.Fatal()
	}
}
