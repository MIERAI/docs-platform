package semantic

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/rulejudge"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
)

// RuleGoFactMismatch is the rule of a typed fact of the Go code (a flag's default, an exported constant, an
// environment variable's default, a config field's constructor default, a route's method)
// contradicts the line - decided by rules (rulejudge), never by a model.
const RuleGoFactMismatch = "go_fact_mismatch"

func init() { subLayerOf[RuleGoFactMismatch] = SubValue }

var errNotIndexed = errors.New("not in the evidence index")

// decideRules runs the deterministic stage over every prose line of the Markdown documents
// before anything is put to the judge: decided lines are never questions. In agentic mode a
// decision is validated by the runner exactly like a model's evidence and recorded for the
// ledger (model "rules/go-ast").
func (c *checker) decideRules(r *repo.Repo, inv *scan.Inventory) {
	c.decided = map[string]bool{}
	read := func(p string) ([]byte, error) {
		ls := c.ix.Lines(p)
		if ls == nil {
			return nil, errNotIndexed
		}
		return []byte(strings.Join(ls, "\n")), nil
	}
	st := rulejudge.Load(read, inv.CodeFiles)
	if st.Facts() == 0 {
		return
	}
	for _, d := range inv.DocFiles {
		if ext := strings.ToLower(path.Ext(d)); ext != ".md" && ext != ".markdown" {
			continue
		}
		b, err := r.Read(d)
		if err != nil {
			continue
		}
		for _, l := range claim.ProseLines(b) {
			if c.opt.Suppressed[fmt.Sprintf("%s:%d", d, l.N)] {
				continue
			}
			if dec, ok := st.Decide(d, l.Text); ok {
				c.recordRule(d, l.N, l.Text, dec)
			}
		}
	}
}

func (c *checker) recordRule(p string, n int, text string, d rulejudge.Decision) {
	if c.opt.Agentic != nil {
		lr, ok := c.opt.Agentic.DecideRules(c.ctx, p, n, d.Answer())
		if !ok {
			return // the evidence did not validate: the line stays with the model
		}
		c.ruled = append(c.ruled, lr)
	}
	c.decided[fmt.Sprintf("%s:%d", p, n)] = true
	c.stats.RulesDecided++
	if d.Category == agentic.Confirmed {
		c.stats.RulesConfirmed++
		return
	}
	c.stats.RulesContradicted++
	is := Issue{
		Rule: RuleGoFactMismatch, Subject: d.Subject + "=" + d.Doc, Confidence: model.ConfHigh,
		Detail:     d.Reason + " (decided by rules, no model)",
		Suggestion: fmt.Sprintf("Update the docs to %s (or the code, if the docs are right).", d.Code),
		Quote:      d.Quotes[0].Quote,
	}
	if d.Draft != "" {
		is.Suggestion = "Replace the line with: " + strings.TrimSpace(d.Draft) + " (or change the code, if the docs are right)."
	}
	for _, q := range d.Quotes {
		is.Locations = append(is.Locations, Location{Path: q.Path, Line: q.Line, Role: "code"})
	}
	c.add(&claim.Claim{Path: p, Line: n, Text: text}, is)
}

// dropDecided removes the questions on lines the rules decided and counts them.
func (c *checker) dropDecided() {
	kept := c.pending[:0]
	for _, q := range c.pending {
		if c.decided[fmt.Sprintf("%s:%d", q.cl.Path, q.cl.Line)] {
			c.stats.RulesSaved++
			continue
		}
		kept = append(kept, q)
	}
	c.pending = kept
}
