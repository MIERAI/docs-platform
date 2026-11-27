package rulejudge

import (
	"fmt"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/extract/golang"
)

// Fact is the decision as one fact of the line ("flag -workers defaults to 4").
func (d Decision) Fact() string {
	switch d.Kind {
	case golang.FactRoute:
		return "route " + d.Subject + " is registered"
	case golang.FactConst:
		return fmt.Sprintf("constant %s is %s", d.Subject, d.Doc)
	case golang.FactFlag, golang.FactEnv, golang.FactField:
	}
	return fmt.Sprintf("%s %s defaults to %s", kindLabel[d.Kind], d.Subject, d.Doc)
}

// Answer is the decision in the agentic answer form: one fact, its status, and the quoted
// source lines as evidence (validated by the runner like a model's).
func (d Decision) Answer() agentic.RulesAnswer {
	ev := make([]agentic.Evidence, 0, len(d.Quotes))
	for _, q := range d.Quotes {
		ev = append(ev, agentic.Evidence{Kind: agentic.KindQuote, Path: q.Path, Line: q.Line, Quote: q.Quote})
	}
	return agentic.RulesAnswer{
		Answer:   agentic.LineAnswer{Facts: []agentic.Fact{{Fact: d.Fact(), Status: d.Category, Evidence: ev}}, Reason: d.Reason},
		Category: d.Category, Model: Model, PromptHash: PromptHash(),
	}
}

// Result is the decision as a line result without re-validation (for callers without a
// runner; the quotes were read from the very files the facts came from).
func (d Decision) Result(path string, line int) agentic.LineResult {
	a := d.Answer()
	fc := agentic.FactCheck{Fact: d.Fact(), Status: d.Category, Valid: true}
	for _, e := range a.Answer.Facts[0].Evidence {
		fc.Evidence = append(fc.Evidence, agentic.EvidenceCheck{Evidence: e, OK: true})
	}
	n := len(fc.Evidence)
	return agentic.LineResult{
		Path: path, Line: line, Category: d.Category, Verdict: agentic.Verdict(d.Category),
		Derivation: agentic.Derivation{
			Category: d.Category, Claimed: d.Category, Facts: []agentic.FactCheck{fc}, Evidence: n, EvidenceOK: n,
			Kinds: map[string]int{agentic.KindQuote + "_ok": n},
		},
		Reason: d.Reason, Parse: agentic.ParseRules, Model: Model, PromptHash: PromptHash(), GroupSize: 1,
	}
}

// Decider is the stage as a runner hook.
func (s *Stage) Decider() agentic.Decider {
	return func(path string, _ int, text string) (agentic.RulesAnswer, bool) {
		d, ok := s.Decide(path, text)
		if !ok {
			return agentic.RulesAnswer{}, false
		}
		return d.Answer(), true
	}
}
