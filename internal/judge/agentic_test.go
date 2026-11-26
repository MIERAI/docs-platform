package judge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/contracts"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/jsonschema"
	"github.com/MIERAI/docs-platform/judgev1"
)

func serveMCPSpec(spec string) {
	t, err := OpenMCPSpec(spec)
	if err != nil {
		os.Exit(2)
	}
	_ = ServeMCP(context.Background(), os.Stdin, os.Stdout, t)
}

// fakeClaudeAgentic imitates `claude -p` in agentic mode: it checks its argv, starts the MCP
// server named in --mcp-config, lists and calls tools through it, and prints a result whose
// structured output reports what it saw.
func fakeClaudeAgentic() {
	args := os.Args[1:]
	val := func(flag string) string {
		if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	_ = json.Unmarshal([]byte(val("--mcp-config")), &cfg)
	srv := cfg.MCPServers[MCPServerName]
	cmd := exec.Command(srv.Command, srv.Args...)
	in, _ := cmd.StdinPipe()
	outp, _ := cmd.StdoutPipe()
	if cmd.Start() != nil {
		os.Exit(4)
	}
	rd := bufio.NewScanner(outp)
	rd.Buffer(make([]byte, 1<<20), 1<<22)
	id := 0
	rpc := func(method string, params any) map[string]any {
		id++
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		_, _ = in.Write(append(b, '\n'))
		rd.Scan()
		var r map[string]any
		_ = json.Unmarshal(rd.Bytes(), &r)
		return r
	}
	rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	tools := rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	readOK := rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": "main.go"}})["result"].(map[string]any)["isError"] == nil
	privRefused := rpc("tools/call", map[string]any{"name": "read_file", "arguments": map[string]any{"path": "private/keys.md"}})["result"].(map[string]any)["isError"] == true
	_ = in.Close()
	_ = cmd.Wait()
	reason := fmt.Sprintf("tools=%d read=%v private_refused=%v no_builtin=%v allowed=%v turns=%s append=%v", len(tools), readOK, privRefused,
		slices.Contains(args, "--tools") && val("--tools") == "", strings.Contains(val("--allowedTools"), "mcp__docsp__check_quote"), val("--max-turns"),
		val("--append-system-prompt") == "s" && !slices.Contains(args, "--system-prompt"))
	ans, _ := json.Marshal(map[string]any{"facts": []any{}, "reason": reason})
	fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":%s,"total_cost_usd":0.03,"usage":{"input_tokens":5,"output_tokens":40},"modelUsage":{"claude-x":{}}}`, ans)
}

func TestClaudeCodeAgenticThroughMCP(t *testing.T) {
	t.Setenv("DOCSP_FAKE", "claude-agentic")
	cc, err := NewClaudeCode(ClaudeCodeConfig{Program: self(t), Model: "claude-x", PassEnv: []string{"DOCSP_FAKE"}, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	in := testInstance(t, "")
	g, _ := NewGuard("judge", cc, in)
	pol := Policy{Repo: "pub", Class: in.Classification, Cloud: true}
	pol.Class.Paths = append(pol.Class.Paths, instance.PathClass{Repo: "pub", Glob: "private/**", Class: instance.Private})
	tl, err := NewPolicyTools(worktree(t), pol, judgev1.ToolLimits{MaxSteps: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tl.Close() }()
	req := agenticRequest("cc1", judgev1.TaskClaimFacts)
	o, err := g.Judge(WithTools(context.Background(), tl), req)
	if err != nil || o.Response.Error != nil || o.Response.AnswerParse != judgev1.ParseSchema {
		t.Fatalf("%+v %v", o.Response, err)
	}
	var a struct{ Reason string }
	_ = json.Unmarshal(o.Response.Answer, &a)
	want := fmt.Sprintf("tools=%d read=true private_refused=true no_builtin=true allowed=true turns=7 append=true", len(AllTools))
	if a.Reason != want {
		t.Fatalf("fake CLI saw %q, want %q", a.Reason, want)
	}
	if len(o.Response.Transcript) != 2 || o.Response.Transcript[1].Error == "" {
		t.Fatalf("transcript from the tool server: %+v", o.Response.Transcript)
	}
}

func agenticRequest(id, task string) *judgev1.Request {
	usd := 0.5
	r := &judgev1.Request{
		SchemaVersion: judgev1.SchemaVersion, ID: id, Task: judgev1.Task{Type: task}, Mode: judgev1.ModeAgentic,
		Claim:    judgev1.Claim{Repo: "pub", Path: "README.md", Line: 3, Text: "The port is 8080."},
		Evidence: []judgev1.Evidence{},
		Prompt: judgev1.Prompt{
			Version: "t/1", Hash: "0123456789abcdef", System: "s", User: "check the port",
			ReplySchema: json.RawMessage(`{"type":"object","required":["facts","reason"],"properties":{"facts":{"type":"array"},"reason":{"type":"string"}}}`),
		},
		Limits: judgev1.Limits{MaxTurns: 7, MaxRequestUSD: &usd},
	}
	if task == judgev1.TaskDocFacts {
		r.Claims = []judgev1.Claim{r.Claim, {Repo: "pub", Path: "README.md", Line: 4, Text: "x"}}
	}
	return r
}

func TestAgenticContractShapes(t *testing.T) {
	reqSchema, err := jsonschema.Compile(contracts.JudgeRequest)
	if err != nil {
		t.Fatal(err)
	}
	respSchema, _ := jsonschema.Compile(contracts.JudgeResponse)
	tl, _ := NewTools(worktree(t), nil, judgev1.ToolLimits{})
	defer func() { _ = tl.Close() }()
	for _, task := range []string{judgev1.TaskClaimFacts, judgev1.TaskDocFacts, judgev1.TaskRepoDrift} {
		req := agenticRequest("x-"+task, task)
		req.Tools = tl.Spec()
		if err := req.Validate(); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(req)
		if errs := reqSchema.ValidateJSON(b); len(errs) > 0 {
			t.Fatalf("%s request violates judge/v1: %v", task, errs)
		}
		resp := judgev1.NewStructuredResponse(req, json.RawMessage(`{"facts":[]}`), judgev1.ParseFallback, judgev1.Model{Provider: "p", ID: "m"}, judgev1.Usage{})
		fail := judgev1.NewStructuredResponse(req, nil, judgev1.ParseFailed, judgev1.Model{Provider: "p", ID: "m"}, judgev1.Usage{})
		for _, r := range []*judgev1.Response{resp, fail} {
			b, _ := json.Marshal(r)
			if errs := respSchema.ValidateJSON(b); len(errs) > 0 {
				t.Fatalf("%s response violates judge/v1: %v", task, errs)
			}
		}
		if fail.Error == nil || fail.Error.Code != judgev1.ErrCodeBadReply || fail.AnswerParse != judgev1.ParseFailed {
			t.Fatalf("failure %+v", fail)
		}
	}
	bad := agenticRequest("y", judgev1.TaskClaimFacts)
	bad.Mode = judgev1.ModeSnippet
	if bad.Validate() == nil {
		t.Fatal("structured task accepted in snippet mode")
	}
	if len(tl.Spec().Definitions) != len(AllTools) {
		t.Fatal("every tool needs a definition")
	}
	for _, d := range tl.Spec().Definitions {
		if !json.Valid(d.InputSchema) || !slices.Contains(AllTools, d.Name) {
			t.Fatalf("definition %s", d.Name)
		}
	}
}

// fakeOllama serves /api/chat: two tool turns, a plain turn, then the formatted answer.
type fakeOllama struct {
	mu   sync.Mutex
	reqs []chatRequest
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req chatRequest
	_ = json.Unmarshal(b, &req)
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	n := len(f.reqs)
	f.mu.Unlock()
	var msg chatMessage
	switch {
	case req.Format != nil:
		msg.Content = `{"facts":[{"fact":"port","status":"contradicted","evidence":[{"kind":"quote","path":"main.go","line":4,"quote":"const port = 9090"}]}],"reason":"r"}`
	case n == 1:
		msg.ToolCalls = []chatToolCall{toolCall("read_file", `{"path":"main.go"}`), toolCall("read_file", `{"path":"main.go"}`)}
	case n == 2:
		msg.ToolCalls = []chatToolCall{toolCall("search", `"{\"pattern\":\"port\",\"scope\":\"*.go\"}"`)}
	default:
		msg.Content = "done exploring"
	}
	_ = json.NewEncoder(w).Encode(chatResponse{Model: "local-m", Message: msg, PromptEvalCount: 100, EvalCount: 10})
}

func toolCall(name, args string) chatToolCall {
	var c chatToolCall
	c.Function.Name, c.Function.Arguments = name, json.RawMessage(args)
	return c
}

func TestOllamaAgentic(t *testing.T) {
	f := &fakeOllama{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	o, err := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "local-m", NumCtx: 32768})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := NewGuard("judge", o, testInstance(t, ""))
	tl, _ := NewTools(worktree(t), nil, judgev1.ToolLimits{MaxSteps: 10})
	defer func() { _ = tl.Close() }()
	req := agenticRequest("o1", judgev1.TaskClaimFacts)
	out, err := g.Judge(WithTools(context.Background(), tl), req)
	if err != nil || out.Response.Error != nil || out.Response.AnswerParse != judgev1.ParseSchema || !strings.Contains(string(out.Response.Answer), "contradicted") {
		t.Fatalf("%+v %v", out.Response, err)
	}
	if len(f.reqs) != 4 || f.reqs[0].Tools == nil || f.reqs[3].Tools != nil || string(f.reqs[3].Format) != string(req.Prompt.ReplySchema) {
		t.Fatalf("chat requests: %d, final format %s", len(f.reqs), f.reqs[len(f.reqs)-1].Format)
	}
	// the repeated call is answered without running the tool; the string-encoded arguments work
	tr := out.Response.Transcript
	if len(tr) != 2 || tr[0].Tool != "read_file" || tr[1].Tool != "search" || tr[1].Error != "" {
		t.Fatalf("transcript %+v", tr)
	}
	if out.Response.Usage.InputTokens != 400 || g.Stats().ParseFailures != 0 {
		t.Fatalf("usage %+v", out.Response.Usage)
	}
}

func TestNewToolsResults(t *testing.T) {
	dir := worktree(t)
	gitIn(t, dir, "mv", "README.md", "GUIDE.md")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qm", "rename the readme")
	tl, _ := NewTools(dir, publicOnly, judgev1.ToolLimits{MaxSteps: 100})
	defer func() { _ = tl.Close() }()
	tl.SetDoc(Doc{Path: "GUIDE.md", Lines: []int{3}})
	tests := []struct{ tool, args, want string }{
		{judgev1.ToolSearch, `{"pattern":"CONST PORT","scope":"*.go"}`, `"path":"main.go","line":4`},
		{judgev1.ToolSearch, `{"pattern":"port\\s=\\s\\d+","regex":true}`, `"line":4`},
		{judgev1.ToolSearch, `{"pattern":"secret plan"}`, `"matches":[]`},
		{judgev1.ToolGlob, `{"pattern":"*.md"}`, `"GUIDE.md"`},
		{judgev1.ToolGitChanges, `{"kind":"R"}`, `"status":"R100","path":"README.md","new_path":"GUIDE.md"`},
		{judgev1.ToolGitChanges, `{"kind":"A","max_count":2}`, `"status":"A"`},
		{judgev1.ToolFindReferences, `{"name":"port"}`, `DEFINITIONS:\nmain.go:4: const port = 9090`},
		{judgev1.ToolChangesSince, `{"path":"GUIDE.md","line":3}`, `was last written by commit`},
		{judgev1.ToolCheckQuote, `{"path":"main.go","line":5,"text":"const   port = 9090"}`, `OK: quote found`},
		{judgev1.ToolCheckQuote, `{"path":"main.go","line":1,"text":"const port = 9090"}`, `line(s) 4`},
		{judgev1.ToolCheckQuote, `{"path":"GUIDE.md","line":3,"text":"The port is 8080."}`, `statement under test`},
		{judgev1.ToolReadFile, `{"path":".github/x"}`, ``},
	}
	for _, tt := range tests {
		r := call(t, tl, tt.tool, tt.args)
		if tt.want == "" {
			if strings.Contains(r.Error, ".git is not") {
				t.Errorf(".github refused as .git: %s", r.Error)
			}
			continue
		}
		if !r.OK || !strings.Contains(string(r.Content), strings.ReplaceAll(tt.want, "\n", `\n`)) {
			t.Errorf("%s %s: ok=%v %s %s", tt.tool, tt.args, r.OK, r.Error, cutStr(string(r.Content), 600))
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestCompileScope(t *testing.T) {
	tests := []struct {
		scope, path string
		want        bool
	}{
		{"*", "a/b/c.go", true},
		{"", "x", true},
		{"scripts", "scripts/run.sh", true},
		{"scripts/", "scripts/run.sh", true},
		{"scripts", "scriptsx/run.sh", false},
		{"scripts/*", "scripts/sub/run.sh", true},
		{"*.go", "cmd/x/main.go", true},
		{"*.go", "main.go.txt", false},
		{".github/workflows/*", ".github/workflows/ci.yml", true},
		{"**/*.yaml", "a.yaml", true},
		{"**/*.yaml", "deep/er/a.yaml", true},
		{"docs/[ab].md", "docs/a.md", true},
		{"docs/[!ab].md", "docs/a.md", false},
		{"cl?sters/*", "clusters/x", true},
	}
	for _, tt := range tests {
		m, err := CompileScope(tt.scope)
		if err != nil {
			t.Fatalf("%q: %v", tt.scope, err)
		}
		if m(tt.path) != tt.want {
			t.Errorf("scope %q path %q: %v", tt.scope, tt.path, !tt.want)
		}
	}
}

func FuzzCompileScope(f *testing.F) {
	for _, s := range []string{"*", "a/**/b", "[", "[]]", "x/[!a-", `\`, "**/", "?"} {
		f.Add(s, "a/b/c")
	}
	f.Fuzz(func(_ *testing.T, scope, p string) {
		if m, err := CompileScope(scope); err == nil {
			_ = m(p)
		}
	})
}

func TestMCPServer(t *testing.T) {
	tl, _ := NewTools(worktree(t), publicOnly, judgev1.ToolLimits{MaxSteps: 10})
	defer func() { _ = tl.Close() }()
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`garbage`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"main.go","from_line":4,"to_line":4}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"../etc/passwd"}}}`,
		`{"jsonrpc":"2.0","id":"x","method":"resources/list"}`,
	}, "\n")
	var out strings.Builder
	if err := ServeMCP(context.Background(), strings.NewReader(in), &out, tl); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d replies:\n%s", len(lines), out.String())
	}
	checks := []string{`"protocolVersion":"2025-06-18"`, `"name":"check_quote"`, `const port = 9090`, `"isError":true`, `"code":-32601`}
	for i, c := range checks {
		if !strings.Contains(lines[i], c) {
			t.Errorf("reply %d lacks %s: %s", i, c, lines[i])
		}
	}
}

func FuzzMCPServer(f *testing.F) {
	f.Add(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"pattern":"x"}}}`)
	f.Add(`{"id":null,"method":"tools/list"}`)
	f.Add(`{"id":[1],"method":"tools/call","params":7}`)
	dir := f.TempDir()
	_ = os.WriteFile(dir+"/a", []byte("alpha\n"), 0o600)
	tl, err := NewTools(dir, nil, judgev1.ToolLimits{MaxSteps: 1 << 30, MaxTotalBytes: 1 << 40})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var out strings.Builder
		if err := ServeMCP(context.Background(), strings.NewReader(s), &out, tl); err != nil {
			return
		}
		for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if l != "" && !json.Valid([]byte(l)) {
				t.Fatalf("invalid reply %q", l)
			}
		}
	})
}

func TestGuardCountsParseFailures(t *testing.T) {
	p := &structuredFake{}
	g, _ := NewGuard("judge", p, testInstance(t, ""))
	tl, _ := NewTools(worktree(t), nil, judgev1.ToolLimits{})
	defer func() { _ = tl.Close() }()
	ctx := WithTools(context.Background(), tl)
	for i, text := range []string{`{"facts":[]}`, "```json\n{\"facts\":[]}\n```", "no object", `{"facts":[{"e":{"c":1}}}, {"x":1}]}`} {
		p.text = text
		o, err := g.Judge(ctx, agenticRequest(fmt.Sprint("g", i), judgev1.TaskClaimFacts))
		if err != nil || o.Verdict != judgev1.Unknown {
			t.Fatalf("%d: %+v %v", i, o, err)
		}
	}
	st := g.Stats()
	if st.ParseFailures != 1 || st.ParseFallbacks != 3 || st.Errors != 1 || st.Unknown != 3 { // free text: fallback, fallback, failed, repaired
		t.Fatalf("stats %+v", st)
	}
}

// structuredFake answers structured tasks from free text through the tolerant parser.
type structuredFake struct{ text string }

func (*structuredFake) Name() string    { return "fake" }
func (*structuredFake) Model() string   { return "m" }
func (*structuredFake) Cloud() bool     { return false }
func (*structuredFake) Billing() string { return judgev1.BillingNone }
func (*structuredFake) Close() error    { return nil }
func (*structuredFake) Modes() []string { return []string{judgev1.ModeAgentic} }
func (s *structuredFake) Judge(_ context.Context, r *judgev1.Request) (*judgev1.Response, error) {
	obj, parse := judgev1.ParseObject(nil, s.text)
	return judgev1.NewStructuredResponse(r, obj, parse, judgev1.Model{Provider: "fake", ID: "m"}, judgev1.Usage{}), nil
}
