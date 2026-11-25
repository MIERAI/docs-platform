package judge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/judgev1"
)

// TestMain lets the test binary act as an external provider: DOCSP_FAKE=exec speaks judge/v1
// JSON Lines, DOCSP_FAKE=claude imitates `claude -p --output-format json`.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "judge-tools-mcp" { // the tool server a fake CLI starts
		serveMCPSpec(os.Args[3])
		os.Exit(0)
	}
	switch os.Getenv("DOCSP_FAKE") {
	case "claude-agentic":
		fakeClaudeAgentic()
		os.Exit(0)
	case "exec":
		fakeExec()
		os.Exit(0)
	case "claude":
		fakeClaude()
		os.Exit(0)
	case "agent":
		fakeAgent()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeExec() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		var req judgev1.Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			fmt.Println("garbage")
			continue
		}
		if strings.Contains(req.Claim.Text, "hang") {
			time.Sleep(time.Minute)
		}
		a := judgev1.Answer{Verdict: judgev1.No}
		if strings.Contains(req.Claim.Text, "port") {
			a = judgev1.Answer{Verdict: judgev1.Yes, Quote: "const port = 9090", QuoteEvidenceID: "E1"}
		}
		resp := judgev1.NewResponse(&req, a, judgev1.Model{Provider: "fake", ID: req.Model}, judgev1.Usage{InputTokens: 100, OutputTokens: 10, CostUSD: 0.01})
		b, _ := json.Marshal(resp)
		fmt.Println(string(b))
	}
}

func fakeClaude() {
	b := make([]byte, 1<<16)
	n, _ := os.Stdin.Read(b)
	verdict := `{"verdict":"no","quote":"","quote_evidence_id":"","confidence":0.9,"rationale":"ok"}`
	if strings.Contains(string(b[:n]), "port") {
		verdict = `{"verdict":"yes","quote":"const port = 9090","quote_evidence_id":"E1","confidence":0.9,"rationale":"differs"}`
	}
	fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":%s,"total_cost_usd":0.002,`+
		`"usage":{"input_tokens":5,"output_tokens":40,"cache_read_input_tokens":1000,"cache_creation_input_tokens":0},"modelUsage":{"claude-x":{}},"session_id":"s"}`, verdict)
}

func claim(text string) (judgev1.Claim, []judgev1.Evidence) {
	return judgev1.Claim{Repo: "pub", Path: "README.md", Line: 3, Text: text},
		[]judgev1.Evidence{{Path: "main.go", From: 10, To: 11, Text: "package main\nconst port = 9090"}}
}

func testInstance(t *testing.T, doc string) *instance.Instance {
	t.Helper()
	in, err := instance.Parse([]byte("version: 1\nclassification: {repos: {pub: public}}\n" + doc))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func self(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestExecProviderThroughGuard(t *testing.T) {
	t.Setenv("DOCSP_FAKE", "exec")
	p, err := NewExec(ExecConfig{Name: "exec", Model: "m1", Command: []string{self(t)}, PassEnv: []string{"DOCSP_FAKE"}, Cloud: true, Billing: judgev1.BillingAPI, Timeout: 2 * time.Second, MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGuard("judge", p, testInstance(t, "ai:\n  judge: {budget: {max_requests: 3}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	ctx := context.Background()
	tests := []struct {
		name, text, verdict string
		rejected            bool
	}{
		{"contradiction with verified quote", "The port is 8080 by default", judgev1.Yes, false},
		{"no", "Something unrelated entirely", judgev1.No, false},
	}
	for i, tt := range tests {
		cl, ev := claim(tt.text)
		o, err := g.Judge(ctx, NewRequest(fmt.Sprint("q", i), judgev1.TaskClaimContradiction, cl, ev, ""))
		if err != nil || o.Verdict != tt.verdict || o.QuoteRejected != tt.rejected {
			t.Fatalf("%s: %+v %v", tt.name, o, err)
		}
		if tt.verdict == judgev1.Yes && o.Quote.Line != 11 {
			t.Errorf("quote line %d", o.Quote.Line)
		}
	}
	cl, ev := claim("hang please")
	if _, err := g.Judge(ctx, NewRequest("q-hang", judgev1.TaskClaimContradiction, cl, ev, "")); err == nil {
		t.Fatal("timeout not reported")
	}
	if _, err := g.Judge(ctx, NewRequest("q-over", judgev1.TaskClaimContradiction, cl, ev, "")); !errors.Is(err, ErrBudget) {
		t.Fatalf("budget not enforced: %v", err)
	}
	st := g.Stats()
	if st.Requests != 3 || st.Yes != 1 || st.No != 1 || st.Errors != 1 || st.BudgetStops != 1 || st.CostUSD < 0.019 {
		t.Fatalf("stats %+v", st)
	}
}

func TestClassificationRefusesPrivateForCloud(t *testing.T) {
	t.Setenv("DOCSP_FAKE", "exec")
	p, err := NewExec(ExecConfig{Name: "exec", Model: "m", Command: []string{self(t)}, PassEnv: []string{"DOCSP_FAKE"}, Cloud: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	in := testInstance(t, "")
	g, _ := NewGuard("judge", p, in)
	defer func() { _ = g.Close() }()
	tests := []struct {
		name string
		cl   judgev1.Claim
		ev   []judgev1.Evidence
	}{
		{"private repo", judgev1.Claim{Repo: "secret", Path: "README.md", Text: "x"}, nil},
		{"private evidence repo", judgev1.Claim{Repo: "pub", Path: "README.md", Text: "x"}, []judgev1.Evidence{{Repo: "secret", Path: "a.go", Text: "y"}}},
		{"unknown repo defaults private", judgev1.Claim{Path: "README.md", Text: "x"}, nil},
	}
	for _, tt := range tests {
		_, err := g.Judge(context.Background(), NewRequest("c", judgev1.TaskClaimContradiction, tt.cl, tt.ev, ""))
		if !errors.Is(err, ErrClassified) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if st := g.Stats(); st.Requests != 0 || st.Refused != 3 || len(g.Refusals()) != 3 {
		t.Fatalf("refused requests reached the provider: %+v", st)
	}
	// a local provider may receive private content
	local, _ := NewExec(ExecConfig{Name: "exec", Model: "m", Command: []string{self(t)}, PassEnv: []string{"DOCSP_FAKE"}, Cloud: false, Timeout: time.Second})
	lg, _ := NewGuard("judge", local, in)
	defer func() { _ = lg.Close() }()
	if _, err := lg.Judge(context.Background(), NewRequest("l", judgev1.TaskClaimContradiction, tests[0].cl, nil, "")); err != nil {
		t.Fatalf("local provider refused: %v", err)
	}
}

func TestClaudeCodeProvider(t *testing.T) {
	t.Setenv("DOCSP_FAKE", "claude")
	p, err := NewClaudeCode(ClaudeCodeConfig{Program: self(t), Model: "claude-opus-5-5", Effort: "low", PassEnv: []string{"DOCSP_FAKE"}, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := NewGuard("judge", p, testInstance(t, ""))
	cl, ev := claim("The port is 8080")
	req := NewRequest("cc", judgev1.TaskClaimContradiction, cl, ev, "")
	args := strings.Join(p.Args(req), " ")
	for _, want := range []string{"-p", "--tools  ", "--no-session-persistence", "--strict-mcp-config", "--model claude-opus-5-5", "--effort low", "--output-format json"} {
		if !strings.Contains(args, want) {
			t.Errorf("argv lacks %q", want)
		}
	}
	o, err := g.Judge(context.Background(), req)
	if err != nil || o.Verdict != judgev1.Yes || o.Response.Model.ID != "claude-x" || o.Response.Usage.Billing != judgev1.BillingSubscription {
		t.Fatalf("%+v %v", o, err)
	}
	if st := g.Stats(); st.CacheRead != 1000 || st.CostUSD != 0.002 {
		t.Fatalf("usage %+v", st)
	}
}

func TestParseClaudeOutput(t *testing.T) {
	cl, ev := claim("x")
	req := NewRequest("p", judgev1.TaskClaimContradiction, cl, ev, "")
	m := judgev1.Model{Provider: "claude-code", ID: "m"}
	tests := []struct{ name, out, code, verdict string }{
		{"not json", "Credit balance too low", judgev1.ErrCodeBadReply, judgev1.Unknown},
		{"error", `{"type":"result","subtype":"error_max_budget_usd","is_error":true}`, judgev1.ErrCodeBudget, judgev1.Unknown},
		{"result text", `{"type":"result","subtype":"success","result":"{\"verdict\":\"no\",\"quote\":\"\"}"}`, "", judgev1.No},
		{"no answer", `{"type":"result","subtype":"success","result":"hmm"}`, judgev1.ErrCodeBadReply, judgev1.Unknown},
	}
	for _, tt := range tests {
		r := ParseClaudeOutput(req, []byte(tt.out), m)
		code := ""
		if r.Error != nil {
			code = r.Error.Code
			if strings.Contains(r.Error.Message, "Credit") {
				t.Errorf("%s: CLI output leaked into the error", tt.name)
			}
		}
		if code != tt.code || r.Verdict != tt.verdict {
			t.Errorf("%s: %+v", tt.name, r)
		}
	}
}

func TestVerifyQuote(t *testing.T) {
	cl, ev := claim("The default port is 8080")
	req := NewRequest("v", judgev1.TaskClaimContradiction, cl, ev, "")
	tests := []struct {
		quote string
		ok    bool
	}{
		{"const port = 9090", true},
		{"port", false},                     // too short
		{"The default port is 8080", false}, // copied from the claim
		{"const port = 9999", false},        // not in evidence
		{"package main  ", false},           // unrelated to the claim
	}
	for _, tt := range tests {
		if got := VerifyQuote(req, tt.quote); got.OK != tt.ok {
			t.Errorf("VerifyQuote(%q) = %+v", tt.quote, got)
		}
	}
	inc := NewRequest("v", judgev1.TaskClaimIncomplete, cl, ev, "")
	if !VerifyQuote(inc, "package main").OK {
		t.Error("incompleteness quotes need not share words with the claim")
	}
}

func TestPromptHashStable(t *testing.T) {
	cl, ev := claim("x")
	a := NewRequest("a", judgev1.TaskDraftEdit, cl, ev, "q")
	b := NewRequest("b", judgev1.TaskDraftEdit, cl, ev, "q")
	if a.Prompt.Hash != b.Prompt.Hash || a.RequireQuote || !strings.Contains(a.Prompt.User, "JUDGE'S QUOTE: q") {
		t.Fatalf("draft prompt %+v", a.Prompt)
	}
	if a.Prompt.Hash == NewRequest("a", judgev1.TaskClaimContradiction, cl, ev, "").Prompt.Hash {
		t.Fatal("tasks share a hash")
	}
	if len(TemplateHash()) != 16 {
		t.Fatal("template hash")
	}
}

func TestDryRunNeverCallsProvider(t *testing.T) {
	p, err := NewExec(ExecConfig{Name: "exec", Model: "claude-sonnet-5-5", Command: []string{"/nonexistent/provider"}, Cloud: true, Billing: judgev1.BillingAPI})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := NewGuard("judge", p, testInstance(t, ""))
	cl, ev := claim("The port is 8080")
	req := NewRequest("d", judgev1.TaskClaimContradiction, cl, ev, "")
	req.DryRun = true
	o, err := g.Judge(context.Background(), req)
	if err != nil || !o.Response.Usage.Estimated || o.Response.Usage.CostUSD <= 0 {
		t.Fatalf("%+v %v", o, err)
	}
}

func FuzzParseClaudeOutput(f *testing.F) {
	f.Add([]byte(`{"type":"result","subtype":"success","structured_output":{"verdict":"yes","quote":"abc"}}`))
	f.Add([]byte(`{"type":"result","modelUsage":{"a":{},"b":{}}}`))
	f.Add([]byte(`null`))
	cl, ev := claim("x")
	req := NewRequest("f", judgev1.TaskClaimContradiction, cl, ev, "")
	f.Fuzz(func(t *testing.T, b []byte) {
		r := ParseClaudeOutput(req, b, judgev1.Model{Provider: "p", ID: "m"})
		if r.ID != "f" || (r.Verdict != judgev1.Yes && r.Verdict != judgev1.No && r.Verdict != judgev1.Unknown) {
			t.Fatalf("bad response %+v", r)
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	f.Add([]byte(`{"schema_version":"1.0","id":"a","verdict":"yes","quote":"q","model":{"provider":"p","id":"m"},"prompt_hash":"0123456789abcdef","usage":{"input_tokens":1,"output_tokens":1,"cost_usd":0}}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := DecodeResponse(b)
		if err == nil && r == nil {
			t.Fatal("nil response without error")
		}
	})
}
