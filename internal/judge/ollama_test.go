package judge

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/judgev1"
)

func TestIsLocalIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "192.168.8.1": true, "172.16.0.1": true,
		"100.106.140.35": true, "169.254.1.1": true, "::1": true, "fd00::1": true,
		"8.8.8.8": false, "100.128.0.1": false, "2001:4860::8888": false, "1.1.1.1": false,
	}
	for ip, want := range cases {
		if got := IsLocalIP(net.ParseIP(ip)); got != want {
			t.Errorf("IsLocalIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestNewOllamaValidation(t *testing.T) {
	bad := []OllamaConfig{
		{Endpoint: "ftp://127.0.0.1", Model: "m"},
		{Endpoint: "http://user:pw@127.0.0.1:1", Model: "m"},
		{Endpoint: "127.0.0.1:11434", Model: "m"},
		{Endpoint: "http://127.0.0.1:11434"},
	}
	for _, c := range bad {
		if _, err := NewOllama(c); err == nil {
			t.Errorf("NewOllama(%+v) accepted", c)
		}
	}
	if p, err := New(instance.Task{Provider: "none"}); p != nil || err != nil {
		t.Errorf("none: %v %v", p, err)
	}
	if _, err := New(instance.Task{Provider: "cloud"}); err == nil {
		t.Error("unknown providers must be rejected")
	}
}

func ollamaReq() *judgev1.Request {
	return NewRequest("o", judgev1.TaskClaimContradiction, judgev1.Claim{Path: "a.md", Text: "c"}, nil, "")
}

func TestJudgeProtocol(t *testing.T) {
	var got genRequest
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/generate" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"model":"m","response":"{\"verdict\":\"no\",\"quote\":\"\"}","prompt_eval_count":12,"eval_count":3}`))
	}))
	defer srv.Close()
	o, err := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.Judge(context.Background(), ollamaReq())
	if err != nil || r.Verdict != "no" || r.Usage.InputTokens != 12 || r.Usage.OutputTokens != 3 {
		t.Fatalf("Judge: %v %+v", err, r)
	}
	if got.Think || got.Stream || got.Options.Temperature != 0 || got.Options.Seed != DefaultSeed || got.Model != "m" || len(got.Format) == 0 {
		t.Errorf("request not deterministic/structured: %+v", got)
	}
	if o.Cloud() || calls.Load() != 1 {
		t.Errorf("cloud %v calls %d", o.Cloud(), calls.Load())
	}
}

func TestJudgeRetriesAndErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()
	o, err := NewOllama(OllamaConfig{Endpoint: srv.URL, Model: "m", Retries: 2, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Judge(context.Background(), ollamaReq()); err == nil || calls.Load() != 2 {
		t.Errorf("want a retry after 503 then a non-retryable error, got %v after %d calls", err, calls.Load())
	}
}

func TestRefusesPublicAddress(t *testing.T) {
	o, err := NewOllama(OllamaConfig{Endpoint: "http://192.0.2.1:11434", Model: "m", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := localOnly("tcp", "8.8.8.8:443", nil); err == nil {
		t.Error("public address accepted")
	}
	if err := localOnly("tcp", "127.0.0.1:11434", nil); err != nil {
		t.Error(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := o.Judge(ctx, ollamaReq()); err == nil {
		t.Error("TEST-NET address must be refused")
	}
}
