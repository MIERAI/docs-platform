package judgev1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAnswer(t *testing.T) {
	tests := []struct {
		name, task, text string
		verdict, quote   string
		wantErr          bool
	}{
		{"object", TaskClaimContradiction, `{"verdict":"yes","quote":"x := 2","quote_evidence_id":"E1"}`, Yes, "x := 2", false},
		{"fenced", TaskClaimContradiction, "```json\n{\"verdict\":\"No\",\"quote\":\"\"}\n```", No, "", false},
		{"legacy answer key", TaskClaimContradiction, `answer: yes`, Yes, "", false},
		{"bad verdict", TaskClaimIncomplete, `{"verdict":"maybe","quote":""}`, Unknown, "", false},
		{"draft", TaskDraftEdit, `{"draft":"new text","rationale":"r"}`, Yes, "", false},
		{"empty draft", TaskDraftEdit, `{"draft":"  "}`, Unknown, "", false},
		{"garbage", TaskClaimContradiction, `I think so`, "", "", true},
		{"draft garbage", TaskDraftEdit, `verdict: yes`, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := ParseAnswer(tt.task, tt.text)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if a.Verdict != tt.verdict || a.Quote != tt.quote {
				t.Fatalf("got %+v", a)
			}
		})
	}
}

func TestNewResponseDropsQuoteUnlessYes(t *testing.T) {
	req := &Request{ID: "a", RequireQuote: true, Prompt: Prompt{Hash: "0123456789abcdef"}, Task: Task{Type: TaskClaimContradiction}}
	r := NewResponse(req, Answer{Verdict: No, Quote: "q", QuoteEvidenceID: "E1"}, Model{Provider: "p", ID: "m"}, Usage{})
	if r.Quote != "" || r.QuoteEvidenceID != "" {
		t.Fatalf("quote kept on no: %+v", r)
	}
	r = NewResponse(req, Answer{Verdict: Yes, Quote: "q"}, Model{}, Usage{})
	if r.Quote != "q" {
		t.Fatalf("quote dropped on yes")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{{"", 0}, {"abc", 1}, {"abcd", 2}, {"状态", 2}}
	for _, tt := range tests {
		if got := EstimateTokens(tt.in); got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Request{ID: "a", Task: Task{Type: TaskDraftEdit}, Prompt: Prompt{User: "u", Hash: "h"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.Task.Type = "other"
	if bad.Validate() == nil {
		t.Fatal("unknown task accepted")
	}
}

func FuzzParseAnswer(f *testing.F) {
	for _, s := range []string{`{"verdict":"yes","quote":"a"}`, "```\n{}\n```", "answer: no", "{", "}{"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, task := range []string{TaskClaimContradiction, TaskDraftEdit} {
			a, err := ParseAnswer(task, s)
			if err != nil {
				continue
			}
			switch a.Verdict {
			case Yes, No, Unknown:
			default:
				t.Fatalf("verdict %q", a.Verdict)
			}
		}
	})
}

func TestParseObject(t *testing.T) {
	tests := []struct {
		name, structured, text, want, parse string
	}{
		{"structured output", `{"a":1}`, "ignored", `{"a":1}`, ParseSchema},
		{"structured null falls back", `null`, `{"a":2}`, `{"a":2}`, ParseFallback},
		{"fenced", ``, "Answer:\n```json\n{\"a\": [1, 2]}\n```\nbye", `{"a":[1,2]}`, ParseFallback},
		{"prose around", ``, `I checked. {"a":"}"} Hope that helps}`, `{"a":"}"}`, ParseFallback},
		{"think block", ``, `<think>{"no":1}</think>{"a":3}`, `{"a":3}`, ParseFallback},
		{"stray brace repaired", ``, `{"lines":[{"f":{"c":1}}}, {"x":1}]}`, `{"lines":[{"f":{"c":1}},{"x":1}]}`, ParseRepaired},
		{"array is not an object", ``, `[{"a":1}]`, `{"a":1}`, ParseFallback},
		{"nothing", ``, `no json here`, ``, ParseFailed},
		{"broken", ``, `{"a":`, ``, ParseFailed},
	}
	for _, tt := range tests {
		obj, parse := ParseObject(json.RawMessage(tt.structured), tt.text)
		if string(obj) != tt.want || parse != tt.parse {
			t.Errorf("%s: %s %s, want %s %s", tt.name, obj, parse, tt.want, tt.parse)
		}
	}
}

func FuzzParseObject(f *testing.F) {
	for _, s := range []string{`{"a":1}`, "```json\n{}\n```", "{", "}{", `{"a":[{"b":{}}}, {}]}`, "<think>", strings.Repeat("}", 100)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		obj, parse := ParseObject(nil, s)
		switch parse {
		case ParseFailed:
			if obj != nil {
				t.Fatal("failed parse returned an object")
			}
		case ParseFallback, ParseRepaired:
			var m map[string]any
			if err := json.Unmarshal(obj, &m); err != nil {
				t.Fatalf("%s returned a non-object %q", parse, obj)
			}
		default:
			t.Fatalf("parse %q", parse)
		}
	})
}
