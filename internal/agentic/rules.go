package agentic

import (
	"context"
	"strings"

	"github.com/MIERAI/docs-platform/judgev1"
)

// RulesModelPrefix marks a verdict decided by deterministic rules, not by a model: the
// ledger's model field is e.g. "rules/go-ast" and its prompt_hash the rules' version hash.
const RulesModelPrefix = "rules/"

// ParseRules is the parse status of a rules verdict (an answer that needed no parsing).
const ParseRules = "rules"

// IsRules reports whether a model name marks a rules verdict.
func IsRules(model string) bool { return strings.HasPrefix(model, RulesModelPrefix) }

// RulesAnswer is a deterministic answer for one line: facts with quote evidence, the
// category the rules derived, and the rules' name and version hash.
type RulesAnswer struct {
	Answer     LineAnswer
	Category   string
	Model      string
	PromptHash string
}

// Decider decides a documentation line by rules; false leaves the line to the model.
type Decider func(path string, line int, text string) (RulesAnswer, bool)

// SetRules sets the deterministic stage that decides lines before any request (nil = none).
func (r *Runner) SetRules(d Decider) { r.o.Rules = d }

// DecideRules validates a rules answer exactly like a model's (every quote re-checked
// against the worktree by the same validator) and returns its result. It returns false - the
// line goes to the model - when the evidence does not validate to the rules' own category.
func (r *Runner) DecideRules(ctx context.Context, docPath string, line int, a RulesAnswer) (LineResult, bool) {
	res := LineResult{
		Path: docPath, Line: line, Category: Unknown, Verdict: judgev1.Unknown, Parse: ParseRules,
		Model: a.Model, PromptHash: a.PromptHash, GroupSize: 1,
	}
	t, err := r.tools(DefaultLineSteps, maxLineTotalBytes)
	if err != nil {
		return res, false
	}
	defer func() { _ = t.Close() }()
	r.setDoc(t, docPath, []int{line})
	r.derive(ctx, t, nil, a.Answer, &res)
	return res, res.Category == a.Category
}

// rulesFirst decides the lines the rules settle and returns the rest (in order).
func (r *Runner) rulesFirst(ctx context.Context, docPath string, src []string, lines []int) ([]LineResult, []int) {
	if r.o.Rules == nil {
		return nil, lines
	}
	var out []LineResult
	var rest []int
	for _, n := range lines {
		if n >= 1 && n <= len(src) {
			if a, ok := r.o.Rules(docPath, n, src[n-1]); ok {
				if lr, ok := r.DecideRules(ctx, docPath, n, a); ok {
					out = append(out, lr)
					continue
				}
			}
		}
		rest = append(rest, n)
	}
	return out, rest
}
