package main

import (
	"context"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/rulejudge"
)

// makeDrafts drafts each claim: deterministically for rules verdicts, through the draft
// provider g otherwise.
func makeDrafts(ctx context.Context, g *judge.Guard, rules *rulejudge.Stage, root string, claims []*ledger.Claim) []draft.Entry {
	now := time.Now()
	made := make([]draft.Entry, 0, len(claims))
	for _, cl := range claims {
		if agentic.IsRules(cl.Model) {
			made = append(made, rulesDraft(ctx, rules, root, cl, now))
		} else {
			made = append(made, proposeDraft(ctx, g, root, cl, now))
		}
	}
	return made
}

// needsModel reports whether any claim needs a model-written draft (rules verdicts get a
// deterministic one).
func needsModel(claims []*ledger.Claim) bool {
	for _, c := range claims {
		if !agentic.IsRules(c.Model) {
			return true
		}
	}
	return false
}

// rulesDraft is the deterministic draft of a rules verdict: the line with only the wrong
// token replaced by the code's value, checked against the same minimal-diff rules as a
// model's draft. Never model-written.
func rulesDraft(ctx context.Context, st *rulejudge.Stage, root string, c *ledger.Claim, now time.Time) draft.Entry {
	e := draft.Entry{
		ClaimID: c.ID, Repo: c.Repo, Path: c.Path, Line: c.Line, Category: c.Category, Original: c.Text,
		ProposedAt: now.UTC().Format(time.RFC3339), ProposedRef: c.VerifiedRef, Model: rulejudge.Model, Status: draft.RejectedByHarness,
	}
	d, ok := st.Decide(c.Path, c.Text)
	switch {
	case !ok || d.Category != agentic.Contradicted:
		e.Problems = []string{"no draft: the rules no longer contradict this line (recheck it)"}
		return e
	case d.Draft == "":
		e.Problems = []string{"no draft: the code value " + d.Code + " cannot be written in the line's style"}
		return e
	}
	ev := draft.Evidence(ctx, root, c)
	texts := make([]string, 0, len(ev)+len(d.Quotes))
	for _, x := range ev {
		texts = append(texts, x.Text)
	}
	for _, q := range d.Quotes {
		texts = append(texts, q.Quote)
	}
	texts = append(texts, d.Code) // the value the rules evaluated (30 * time.Second is "30s")
	e.Draft = d.Draft
	chk := draft.Constrain(c.Text, e.Draft, texts)
	e.Action, e.DiffTokens, e.Problems = chk.Action, chk.DiffTokens, chk.Problems
	if chk.OK {
		e.Status = draft.Proposed
	}
	return e
}
