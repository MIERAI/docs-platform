package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MIERAI/docs-platform/judgev1"
)

const testKey = "sk-ant-test-0123456789"

// fakeAPI imitates POST /v1/messages; reply picks the answer by the request.
func fakeAPI(t *testing.T, status int, reply func(body map[string]any) string) (*httptest.Server, *atomic.Int32, *map[string]any, *http.Header) {
	t.Helper()
	var calls atomic.Int32
	var last map[string]any
	var hdr http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		hdr = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &last)
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply(last)))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &last, &hdr
}

func message(stop, text string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "stop_reason": stop,
		"content": []map[string]any{{"type": "text", "text": text}},
		"usage":   map[string]any{"input_tokens": 1000, "output_tokens": 200, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0},
	})
	return string(b)
}

func keyFile(t *testing.T, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func testAdapter(t *testing.T, srv *httptest.Server, c Config) *Adapter {
	t.Helper()
	if c.Model == "" {
		c.Model = "claude-opus-5-5"
	}
	if c.Effort == "" {
		c.Effort = "medium"
	}
	if c.KeyFile == "" && c.KeyEnv == "" {
		c.KeyFile = keyFile(t, 0o600)
	}
	host := strings.Split(strings.TrimPrefix(srv.URL, "https://"), ":")[0]
	a, err := newAdapter(c, allowOnly(map[string]bool{host: true}, srv.Client().Transport), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func request(id string) *judgev1.Request {
	return &judgev1.Request{
		SchemaVersion: "1.0", ID: id, Task: judgev1.Task{Type: judgev1.TaskClaimContradiction}, RequireQuote: true,
		Claim:    judgev1.Claim{Path: "README.md", Text: "The port is 8080"},
		Evidence: []judgev1.Evidence{{ID: "E1", Path: "main.go", Text: "const port = 9090"}},
		Prompt: judgev1.Prompt{
			Version: "v", Hash: "0123456789abcdef", System: "sys", User: "user prompt",
			ReplySchema: json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"],"additionalProperties":false}`),
		},
	}
}

func TestJudgeRequestShapeAndResponse(t *testing.T) {
	srv, calls, last, hdr := fakeAPI(t, http.StatusOK, func(map[string]any) string {
		return message("end_turn", `{"verdict":"yes","quote":"const port = 9090","quote_evidence_id":"E1","confidence":0.9,"rationale":"r"}`)
	})
	a := testAdapter(t, srv, Config{Fallbacks: "default"})
	r := a.Judge(context.Background(), request("q1"))
	if r.Error != nil || r.Verdict != judgev1.Yes || r.Quote != "const port = 9090" || r.Model.ID != "claude-opus-5-5" {
		t.Fatalf("response %+v %+v", r, r.Error)
	}
	if r.Usage.CostUSD != (1000*4.0+200*20.0)/1e6 || r.Usage.Billing != judgev1.BillingAPI {
		t.Errorf("usage %+v", r.Usage)
	}
	body := *last
	oc, _ := body["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	sys, _ := body["system"].([]any)
	first, _ := sys[0].(map[string]any)
	switch {
	case calls.Load() != 1, body["model"] != "claude-opus-5-5", oc["effort"] != "medium", format["type"] != "json_schema":
		t.Errorf("request body %v", body)
	case body["temperature"] != nil, body["thinking"] != nil:
		t.Errorf("sampling/thinking parameters must not be sent (400 on current models): %v", body)
	case first["cache_control"] == nil, body["fallbacks"] != "default":
		t.Errorf("cache breakpoint or fallbacks missing: %v", body)
	case hdr.Get("X-Api-Key") != testKey && hdr.Get("Authorization") != "Bearer "+testKey,
		!strings.Contains(hdr.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01"):
		t.Errorf("headers %v", hdr)
	}
}

func TestJudgeFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		stop   string
		code   string
	}{
		{"rate limited", http.StatusTooManyRequests, "", judgev1.ErrCodeRateLimited},
		{"bad key", http.StatusUnauthorized, "", judgev1.ErrCodeConfig},
		{"refusal", http.StatusOK, "refusal", judgev1.ErrCodeRefused},
		{"truncated", http.StatusOK, "max_tokens", judgev1.ErrCodeBadReply},
		{"not json", http.StatusOK, "end_turn", judgev1.ErrCodeBadReply},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _, _ := fakeAPI(t, tt.status, func(map[string]any) string {
				if tt.status != http.StatusOK {
					return `{"type":"error","error":{"type":"x","message":"m"}}`
				}
				return message(tt.stop, "no json here")
			})
			r := testAdapter(t, srv, Config{Fallbacks: "none"}).Judge(context.Background(), request("q"))
			if r.Error == nil || r.Error.Code != tt.code || r.Verdict != judgev1.Unknown {
				t.Fatalf("%+v %+v", r, r.Error)
			}
			if strings.Contains(r.Error.Message, testKey) {
				t.Fatal("key leaked into an error")
			}
		})
	}
}

func TestBudgetAndDryRunCallNothing(t *testing.T) {
	srv, calls, _, _ := fakeAPI(t, http.StatusOK, func(map[string]any) string { return message("end_turn", "{}") })
	a := testAdapter(t, srv, Config{})
	req := request("b")
	tiny := 0.0001
	req.Limits.BudgetRemainingUSD = &tiny
	if r := a.Judge(context.Background(), req); r.Error == nil || r.Error.Code != judgev1.ErrCodeBudget {
		t.Fatalf("budget not enforced: %+v", r)
	}
	dry := request("d")
	dry.DryRun = true
	if r := a.Judge(context.Background(), dry); r.Error != nil || !r.Usage.Estimated || r.Usage.CostUSD <= 0 {
		t.Fatalf("dry run %+v", r)
	}
	if calls.Load() != 0 {
		t.Fatalf("%d HTTP calls", calls.Load())
	}
}

func TestKeyLoading(t *testing.T) {
	t.Setenv("DOCSP_TEST_KEY", testKey)
	t.Setenv("DOCSP_EMPTY_KEY", "")
	tests := []struct {
		name      string
		file, env string
		ok        bool
	}{
		{"0600 file", keyFile(t, 0o600), "", true},
		{"0400 file", keyFile(t, 0o400), "", true},
		{"group readable", keyFile(t, 0o640), "", false},
		{"world readable", keyFile(t, 0o644), "", false},
		{"env", "", "DOCSP_TEST_KEY", true},
		{"empty env", "", "DOCSP_EMPTY_KEY", false},
		{"both", keyFile(t, 0o600), "DOCSP_TEST_KEY", false},
		{"neither", "", "", false},
		{"directory", t.TempDir(), "", false},
	}
	for _, tt := range tests {
		k, err := loadKey(tt.file, tt.env)
		if (err == nil) != tt.ok || tt.ok && k != testKey {
			t.Errorf("%s: %v", tt.name, err)
		}
		if err != nil && strings.Contains(err.Error(), testKey) {
			t.Errorf("%s: key in error", tt.name)
		}
	}
}

func TestAllowList(t *testing.T) {
	rt := allowOnly(map[string]bool{apiHost: true}, http.DefaultTransport)
	for _, u := range []string{"http://api.anthropic.com/v1/messages", "https://127.0.0.1/v1/messages", "https://api.anthropic.com.evil.example/v1"} {
		req, _ := http.NewRequest(http.MethodPost, u, nil)
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("%s: %v", u, err)
		}
	}
	// The production constructor talks only to the official host: a test server is refused.
	srv, calls, _, _ := fakeAPI(t, http.StatusOK, func(map[string]any) string { return message("end_turn", "{}") })
	a, err := newAdapter(Config{Model: "claude-opus-5-5", Effort: "low", KeyFile: keyFile(t, 0o600), Fallbacks: "none"},
		allowOnly(map[string]bool{apiHost: true}, srv.Client().Transport), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if r := a.Judge(context.Background(), request("x")); r.Error == nil || calls.Load() != 0 {
		t.Fatalf("non-allow-listed host reached: %+v", r)
	}
}

func TestServeJSONLines(t *testing.T) {
	srv, _, _, _ := fakeAPI(t, http.StatusOK, func(map[string]any) string {
		return message("end_turn", `{"verdict":"no","quote":"","quote_evidence_id":"","confidence":0.8,"rationale":"r"}`)
	})
	a := testAdapter(t, srv, Config{})
	b, _ := json.Marshal(request("s1"))
	in := strings.NewReader("garbage\n" + string(b) + "\n")
	var out bytes.Buffer
	if err := serve(context.Background(), a, in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"bad_reply"`) || !strings.Contains(lines[1], `"verdict":"no"`) {
		t.Fatalf("%q", out.String())
	}
	if strings.Contains(out.String(), testKey) {
		t.Fatal("key in output")
	}
}

func TestRunFlags(t *testing.T) {
	if err := run(context.Background(), []string{"-model", "m", "-effort", "huge", "-api-key-env", "X"}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("bad effort accepted")
	}
	if err := run(context.Background(), []string{"-effort", "low"}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("missing model accepted")
	}
}
