package judgev1

import (
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
