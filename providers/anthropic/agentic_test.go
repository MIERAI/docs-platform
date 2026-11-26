package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MIERAI/docs-platform/judgev1"
)

// scripted is a fake Messages API answering call n with replies[n] and recording every body.
type scripted struct {
	mu      sync.Mutex
	bodies  []map[string]any
	replies []string
}

func (s *scripted) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		s.mu.Lock()
		n := len(s.bodies)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()
		if r.URL.Path != "/v1/messages" || n >= len(s.replies) {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"unexpected"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.replies[n]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func reply(stop string, content ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_x", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5", "stop_reason": stop,
		"content": content,
		"usage":   map[string]any{"input_tokens": 1000, "output_tokens": 100, "cache_read_input_tokens": 500, "cache_creation_input_tokens": 0},
	})
	return string(b)
}

func toolUse(id, name, input string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": json.RawMessage(input)}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

const factsAnswer = `{"facts":[{"fact":"port","status":"confirmed","evidence":[{"kind":"quote","path":"main.go","line":4,"quote":"const port = 9090"}]}],"reason":"ok"}`

func agenticRequest(id string) *judgev1.Request {
	return &judgev1.Request{
		SchemaVersion: judgev1.SchemaVersion, ID: id, Mode: judgev1.ModeAgentic,
		Task:     judgev1.Task{Type: judgev1.TaskClaimFacts},
		Claim:    judgev1.Claim{Repo: "pub", Path: "README.md", Line: 3, Text: "The port is 9090"},
		Evidence: []judgev1.Evidence{},
		Prompt: judgev1.Prompt{
			Version: "docsp-agentic/3", Hash: "0123456789abcdef", System: "agentic system", User: "check line 3",
			ReplySchema: json.RawMessage(`{"type":"object","properties":{"facts":{"type":"array"},"reason":{"type":"string"}},"required":["facts"],"additionalProperties":false}`),
		},
		Limits: judgev1.Limits{MaxTurns: 16},
		Tools: &judgev1.ToolSpec{
			Contract: "judge-tools/v1", Allowed: []string{judgev1.ToolReadFile, judgev1.ToolSearch},
			Limits: judgev1.ToolLimits{MaxSteps: 16, MaxResultBytes: 32768, MaxTotalBytes: 1 << 20},
			Definitions: []judgev1.ToolDef{
				{Name: judgev1.ToolReadFile, Description: "Read a file.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"from_line":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)},
				{Name: judgev1.ToolSearch, Description: "Search.", InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"],"additionalProperties":false}`)},
			},
		},
	}
}

// fakeCore answers tool calls the way docs-platform's exec transport does and records them.
type fakeCore struct {
	calls []judgev1.ToolCall
}

func (c *fakeCore) caller(_ context.Context, tc judgev1.ToolCall) (judgev1.ToolResult, error) {
	c.calls = append(c.calls, tc)
	r := judgev1.ToolResult{
		Type: "tool_result", ID: tc.ID, Call: tc.Call, OK: true,
		Content: json.RawMessage(`{"lines":["const port = 9090"]}`), Text: "4: const port = 9090 && <ok>",
	}
	if strings.Contains(string(tc.Args), "private") {
		r = judgev1.ToolResult{Type: "tool_result", ID: tc.ID, Call: tc.Call, Error: "path is classified private"}
	}
	return r, nil
}

func TestAgenticToolLoop(t *testing.T) {
	api := &scripted{replies: []string{
		reply("tool_use", text("Let me read it."), toolUse("tu_1", "read_file", `{"path":"main.go"}`), toolUse("tu_2", "read_file", `{"path":"private/notes.md"}`)),
		reply("end_turn", text(factsAnswer)),
	}}
	a := testAdapter(t, api.server(t), Config{Model: "claude-sonnet-5-5", Fallbacks: "default"})
	core := &fakeCore{}
	r := a.Answer(context.Background(), agenticRequest("a1"), core.caller)
	if r.Error != nil || r.AnswerParse != judgev1.ParseSchema || string(r.Answer) != factsAnswer || r.Verdict != judgev1.Unknown {
		t.Fatalf("response %+v error %+v", r, r.Error)
	}
	if r.Usage.InputTokens != 2000 || r.Usage.OutputTokens != 200 || r.Usage.CacheReadInputTokens != 1000 || r.Usage.Billing != judgev1.BillingAPI {
		t.Errorf("usage %+v", r.Usage)
	}
	if want := (2000*2.0 + 200*10.0 + 1000*0.10) / 1e6; r.Usage.CostUSD < want-1e-12 || r.Usage.CostUSD > want+1e-12 {
		t.Errorf("cost %v, want %v", r.Usage.CostUSD, want)
	}
	if len(core.calls) != 2 || core.calls[0].Tool != "read_file" || core.calls[0].ID != "a1" || core.calls[0].Call == core.calls[1].Call {
		t.Fatalf("tool calls %+v", core.calls)
	}
	first, second := api.bodies[0], api.bodies[1]
	tools, _ := first["tools"].([]any)
	t0, _ := tools[0].(map[string]any)
	schema, _ := t0["input_schema"].(map[string]any)
	oc, _ := first["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	switch {
	case len(tools) != 2 || t0["name"] != "read_file" || schema["additionalProperties"] != false || schema["type"] != "object":
		t.Errorf("tools %v", tools)
	case format["type"] != "json_schema" || oc["effort"] != nil:
		t.Errorf("output_config %v (agentic requests must not inherit the snippet effort)", oc)
	case first["temperature"] != nil || first["thinking"] != nil || first["tool_choice"] != nil:
		t.Errorf("unexpected parameters %v", first)
	case first["cache_control"] == nil || first["fallbacks"] != "default":
		t.Errorf("caching or fallbacks missing %v", first)
	}
	// the second turn carries the assistant turn and both tool results in one user message;
	// the model sees the core's text rendering (line numbers, no escaping), errors flagged
	msgs, _ := second["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages %v", msgs)
	}
	last, _ := msgs[2].(map[string]any)
	blocks, _ := last["content"].([]any)
	b0, _ := blocks[0].(map[string]any)
	b1, _ := blocks[1].(map[string]any)
	if len(blocks) != 2 || b0["type"] != "tool_result" || b0["tool_use_id"] != "tu_1" || !strings.Contains(string(mustJSON(b0["content"])), "4: const port = 9090 && <ok>") ||
		b1["is_error"] != true || !strings.Contains(string(mustJSON(b1["content"])), "classified private") {
		t.Errorf("tool results %v", blocks)
	}
}

func mustJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.Bytes()
}

func TestAgenticEffortAndStepBudget(t *testing.T) {
	api := &scripted{replies: []string{
		reply("tool_use", toolUse("tu_1", "search", `{"pattern":"port"}`), toolUse("tu_2", "search", `{"pattern":"x"}`)),
		reply("end_turn", text(factsAnswer)),
	}}
	a := testAdapter(t, api.server(t), Config{Model: "claude-sonnet-5-5", AgenticEffort: "high", Fallbacks: "none"})
	req := agenticRequest("a2")
	req.Limits.MaxTurns = 1
	core := &fakeCore{}
	r := a.Answer(context.Background(), req, core.caller)
	if r.Error != nil || len(core.calls) != 1 {
		t.Fatalf("response %+v calls %d", r.Error, len(core.calls))
	}
	oc, _ := api.bodies[0]["output_config"].(map[string]any)
	tc, _ := api.bodies[1]["tool_choice"].(map[string]any)
	if oc["effort"] != "high" || api.bodies[0]["fallbacks"] != nil || tc["type"] != "none" {
		t.Errorf("effort %v fallbacks %v final tool_choice %v", oc["effort"], api.bodies[0]["fallbacks"], tc)
	}
	msgs, _ := api.bodies[1]["messages"].([]any)
	last, _ := json.Marshal(msgs[len(msgs)-1])
	if !strings.Contains(string(last), "budget exhausted") || !strings.Contains(string(last), "final JSON object") {
		t.Errorf("over-budget call not refused or no final nudge: %s", last)
	}
}

func TestAgenticCapsAndFailures(t *testing.T) {
	tiny := 0.0001
	tests := []struct {
		name    string
		replies []string
		edit    func(*judgev1.Request)
		code    string
		parse   string
		calls   int
	}{
		{"request cap ends the request only", nil, func(r *judgev1.Request) { r.Limits.MaxRequestUSD = &tiny }, judgev1.ErrCodeBadReply, judgev1.ParseFailed, 0},
		{"run budget is a budget error", nil, func(r *judgev1.Request) { r.Limits.BudgetRemainingUSD = &tiny }, judgev1.ErrCodeBudget, "", 0},
		{"no tool definitions", nil, func(r *judgev1.Request) { r.Tools.Definitions = nil }, judgev1.ErrCodeConfig, "", 0},
		{"refusal", []string{reply("refusal")}, nil, judgev1.ErrCodeRefused, "", 1},
		{"truncated", []string{reply("max_tokens", text(`{"facts":[`))}, nil, judgev1.ErrCodeBadReply, judgev1.ParseFailed, 1},
		{
			"tool use after the budget",
			[]string{reply("tool_use", toolUse("a", "search", `{}`)), reply("tool_use", toolUse("b", "search", `{}`))},
			func(r *judgev1.Request) { r.Limits.MaxTurns = 1 }, judgev1.ErrCodeBadReply, judgev1.ParseFailed, 2,
		},
		{"tolerant parser", []string{reply("end_turn", text("Here:\n```json\n"+factsAnswer+"\n```"))}, nil, "", judgev1.ParseFallback, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &scripted{replies: tt.replies}
			a := testAdapter(t, api.server(t), Config{Model: "claude-sonnet-5-5"})
			req := agenticRequest("c")
			if tt.edit != nil {
				tt.edit(req)
			}
			r := a.Answer(context.Background(), req, (&fakeCore{}).caller)
			code := ""
			if r.Error != nil {
				code = r.Error.Code
			}
			if code != tt.code || r.AnswerParse != tt.parse || len(api.bodies) != tt.calls {
				t.Errorf("code %q parse %q calls %d; want %q %q %d", code, r.AnswerParse, len(api.bodies), tt.code, tt.parse, tt.calls)
			}
		})
	}
}

// TestServeAgenticOverStdio runs the stdin/stdout transport the core speaks: request line in,
// tool_call line out, tool_result line in, response line out. Nothing in the output carries
// the API key.
func TestServeAgenticOverStdio(t *testing.T) {
	api := &scripted{replies: []string{
		reply("tool_use", toolUse("tu_1", "read_file", `{"path":"main.go"}`)),
		reply("end_turn", text(factsAnswer)),
	}}
	a := testAdapter(t, api.server(t), Config{Model: "claude-sonnet-5-5"})
	reqLine, _ := json.Marshal(agenticRequest("s1"))
	res, _ := json.Marshal(judgev1.ToolResult{Type: "tool_result", ID: "s1", Call: "c1", OK: true, Text: "1: package main"})
	in := strings.NewReader(string(reqLine) + "\n" + string(res) + "\n")
	var out bytes.Buffer
	if err := serve(context.Background(), a, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output %q", out.String())
	}
	var tc judgev1.ToolCall
	var resp judgev1.Response
	_ = json.Unmarshal([]byte(lines[0]), &tc)
	_ = json.Unmarshal([]byte(lines[1]), &resp)
	if tc.Type != "tool_call" || tc.ID != "s1" || tc.Call != "c1" || tc.Tool != "read_file" || string(tc.Args) != `{"path":"main.go"}` {
		t.Errorf("tool call %+v", tc)
	}
	if resp.ID != "s1" || resp.Error != nil || resp.AnswerParse != judgev1.ParseSchema {
		t.Errorf("response %+v", resp)
	}
	if strings.Contains(out.String(), testKey) {
		t.Error("the API key reached the output")
	}

	// a broken tool channel (wrong call id) ends the request with a transport error
	api2 := &scripted{replies: []string{reply("tool_use", toolUse("tu_1", "read_file", `{"path":"main.go"}`))}}
	a2 := testAdapter(t, api2.server(t), Config{Model: "claude-sonnet-5-5"})
	bad, _ := json.Marshal(judgev1.ToolResult{Type: "tool_result", ID: "s1", Call: "other", OK: true})
	out.Reset()
	if err := serve(context.Background(), a2, strings.NewReader(string(reqLine)+"\n"+string(bad)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	_ = json.Unmarshal([]byte(lines[len(lines)-1]), &resp)
	if resp.Error == nil || resp.Error.Code != judgev1.ErrCodeTransport {
		t.Errorf("broken channel: %+v", resp)
	}
}

func TestAgenticEffortFlag(t *testing.T) {
	if _, err := newAdapter(Config{Model: "m", Effort: "medium", AgenticEffort: "extreme", KeyEnv: "X_KEY"}, nil, ""); err == nil {
		t.Error("bad -agentic-effort accepted")
	}
}
