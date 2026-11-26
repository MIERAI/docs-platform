// Package factorypr reviews a change to an API factory's project from the design's side
// (docs-platform/factory-pr/v1): which business rules with hand-written code the change
// touches - their code or their own text - whether each still holds at the change's head
// (the agentic judge, pointed at the code by the factory's manifest), and, for a rule the
// code now contradicts, the smallest design edit that states what the code does. A draft
// is kept as verified only when the factory accepts the project with it (`factory try`:
// regeneration, build, tests and acceptance all pass at the head). Whether the design is
// stale or the code is wrong stays a human decision; nothing is applied.
package factorypr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factoryai"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

// SchemaVersion of docs-platform/factory-pr/v1.
const SchemaVersion = "1.0"

// Marker identifies the review's PR comment (it is updated in place, never duplicated).
const Marker = "<!-- docsp:factory-pr -->"

// Rule is one touched rule at the head.
type Rule struct {
	API      string         `json:"api"`
	Path     string         `json:"path"`
	Line     int            `json:"line"`
	Text     string         `json:"text"`
	Touched  []string       `json:"touched"` // the changed files that make it relevant
	Category string         `json:"category"`
	Facts    []ledger.Fact  `json:"facts,omitempty"`
	Quotes   []ledger.Quote `json:"quotes,omitempty"`
	Error    string         `json:"error,omitempty"`
	Draft    *Draft         `json:"draft,omitempty"`
}

// Draft is a proposed design edit.
type Draft struct {
	Edits     []factoryai.Edit `json:"edits"`
	Rationale string           `json:"rationale"`
	Verified  bool             `json:"verified"`
	Problem   string           `json:"problem,omitempty"`
}

// Report is docs-platform/factory-pr/v1.
type Report struct {
	SchemaVersion string `json:"schema_version"`
	Repo          string `json:"repo"`
	Base          string `json:"base"`
	Head          string `json:"head"`
	Rules         []Rule `json:"rules"`
	// Acceptance is the factory's verdict on the head as it is, per touched API ("" = passes).
	Acceptance map[string]string `json:"acceptance,omitempty"`
	USD        float64           `json:"usd"`
}

// Problem categories: the rule no longer says what the code does.
var problem = map[string]bool{agentic.Contradicted: true, agentic.OutdatedWording: true, agentic.IncompleteList: true, agentic.DanglingReference: true}

// Problems counts the rules the code contradicts.
func (r *Report) Problems() int {
	n := 0
	for _, x := range r.Rules {
		if problem[x.Category] {
			n++
		}
	}
	return n
}

// Options of a review. Dir is the checkout at the head (HEAD); Base the change's base.
type Options struct {
	Repo, Dir, Base string
	Manifest        *factorymanifest.Manifest // exported at the head
	Run             *agentic.Runner           // the judge (agentic), over Dir
	Builder         *ledger.Builder           // Dir at HEAD
	Draft           *judge.Guard              // proposes design edits (nil = no drafts)
	Try             factorytry.Runner         // the factory (nil = drafts stay unverified)
}

// Touched lists the escaped rules a set of changed paths affects: a change to one of the
// rule's hand-written files or to its own document.
func Touched(m *factorymanifest.Manifest, changed map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, r := range m.EscapedRules() {
		k := fmt.Sprintf("%s:%d", r.Path, r.Line)
		for _, f := range append([]string{r.Path}, r.Files...) {
			if changed[f] {
				out[k] = append(out[k], f)
			}
		}
	}
	return out
}

// Review judges the touched rules at the head and drafts design edits for the contradicted ones.
func Review(ctx context.Context, o Options) (*Report, error) {
	head, err := ledger.Resolve(ctx, o.Dir, "HEAD")
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	ch, err := ledger.Diff(ctx, o.Dir, o.Base, head)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	changed := map[string]bool{}
	for p := range ch.Paths {
		changed[p] = true
	}
	rep := &Report{SchemaVersion: SchemaVersion, Repo: o.Repo, Base: o.Base, Head: head, Rules: []Rule{}}
	touched := Touched(o.Manifest, changed)
	rules, byDoc := touchedByDoc(o.Manifest, touched)
	docs := make([]string, 0, len(byDoc))
	for d := range byDoc {
		docs = append(docs, d)
	}
	sort.Strings(docs)
	apis := map[string]bool{}
	for _, d := range docs {
		for _, lr := range o.Run.Judge(ctx, d, byDoc[d]) {
			k := fmt.Sprintf("%s:%d", d, lr.Line)
			apis[rules[k].API] = true
			rep.Rules = append(rep.Rules, judged(ctx, o, rules[k], touched[k], &lr))
		}
	}
	if o.Try != nil && len(apis) > 0 {
		rep.Acceptance = map[string]string{}
		for a := range apis {
			rep.Acceptance[a] = acceptance(ctx, o.Try, nil, a)
		}
	}
	return rep, nil
}

// touchedByDoc indexes the touched rules ("path:line") and groups their lines by document.
func touchedByDoc(m *factorymanifest.Manifest, touched map[string][]string) (map[string]factorymanifest.EscapedRule, map[string][]int) {
	rules := map[string]factorymanifest.EscapedRule{}
	byDoc := map[string][]int{}
	for _, r := range m.EscapedRules() {
		k := fmt.Sprintf("%s:%d", r.Path, r.Line)
		if _, ok := touched[k]; ok {
			byDoc[r.Path] = append(byDoc[r.Path], r.Line)
			rules[k] = r
		}
	}
	return rules, byDoc
}

// judged turns one judgment into the rule's outcome (with a draft when the code contradicts it).
func judged(ctx context.Context, o Options, er factorymanifest.EscapedRule, touched []string, lr *agentic.LineResult) Rule {
	r := Rule{API: er.API, Path: lr.Path, Line: lr.Line, Touched: touched, Category: lr.Category, Error: lr.Error}
	r.Text, _ = o.Builder.LineText(ctx, lr.Path, lr.Line)
	if c, ok := o.Builder.Claim(ctx, lr); ok {
		r.Category, r.Facts, r.Quotes = c.Category, c.Facts, c.Evidence.Quotes
	}
	if problem[r.Category] && o.Draft != nil {
		r.Draft = draft(ctx, o, er, &r)
	}
	return r
}

// acceptance runs the factory and says what is wrong ("" = everything passes).
func acceptance(ctx context.Context, t factorytry.Runner, files []factorytry.File, api string) string {
	res, err := t.Try(ctx, files, []string{api})
	switch {
	case err != nil:
		return err.Error()
	case res.Status != factorytry.StatusOK:
		return fmt.Sprintf("%s at %s: %s", res.Status, res.Stage, firstLine(res.Message))
	}
	if f := res.Failed(); len(f) > 0 {
		names := make([]string, 0, len(f))
		for _, c := range f[:min(3, len(f))] {
			names = append(names, fmt.Sprintf("%s (%s)", c.Name, c.Source))
		}
		return fmt.Sprintf("%d cases fail: %s", len(f), strings.Join(names, "; "))
	}
	return ""
}

const draftSchema = `{"type":"object","properties":{"edits":{"type":"array","maxItems":4,"items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["old","new"],"additionalProperties":false}},"rationale":{"type":"string"}},"required":["edits","rationale"],"additionalProperties":false}`

const draftTask = `RULE (%s line %d):
<<<
%s
>>>
At the head of a change, the hand-written code no longer does what this rule says. Facts checked against the code:
%s
Propose the smallest edits to %s that make the design state what the code does now: the rule's wording, and the expected results of any example that encodes the old behaviour. Keep every other word, the language, keys and indentation exactly. Do not change inputs or data rows unless an example cannot be fixed otherwise. Whether the design is stale or the code is wrong is decided by a person; you only draft the design side.
Each edit replaces "old" - text copied exactly from %s (whole lines, occurring exactly once; never the line numbers) - with "new". At most 4 edits.%s`

func draft(ctx context.Context, o Options, er factorymanifest.EscapedRule, r *Rule) *Draft {
	doc, err := os.ReadFile(filepath.Join(o.Dir, filepath.FromSlash(er.Path)))
	if err != nil {
		return &Draft{Problem: err.Error()}
	}
	srcs := []factoryai.Source{{Path: er.Path, Text: string(doc)}}
	for _, f := range er.Files {
		if b, err := os.ReadFile(filepath.Join(o.Dir, filepath.FromSlash(f))); err == nil {
			srcs = append(srcs, factoryai.Source{Path: f, Text: string(b)})
		}
	}
	var facts strings.Builder
	for _, f := range r.Facts {
		fmt.Fprintf(&facts, "- %s (%s)\n", f.Fact, f.Status)
	}
	d := &Draft{}
	var lastErr string
	for range 2 {
		done, problem := draftOnce(ctx, o, er, string(doc), facts.String(), srcs, d, lastErr)
		if done {
			return d
		}
		lastErr = problem
	}
	d.Problem = lastErr
	return d
}

// draftOnce asks for one draft and has the factory check it. done = d has its outcome.
func draftOnce(ctx context.Context, o Options, er factorymanifest.EscapedRule, doc, facts string, srcs []factoryai.Source, d *Draft, lastErr string) (bool, string) {
	text := factoryai.RuleBlock(doc, er.Line)
	retry := ""
	if lastErr != "" {
		retry = "\nA previous draft failed: " + lastErr + ". Fix it."
	}
	var a struct {
		Edits []struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"edits"`
		Rationale string `json:"rationale"`
	}
	if err := factoryai.Ask(ctx, o.Draft, judgev1.TaskDraftRule, o.Repo, er.Path, er.Line, text,
		fmt.Sprintf(draftTask, er.Path, er.Line, text, facts, er.Path, er.Path, retry), srcs, draftSchema, &a); err != nil {
		d.Problem = err.Error()
		return true, ""
	}
	d.Rationale, d.Edits = a.Rationale, nil
	if len(a.Edits) == 0 {
		d.Problem = "no edit proposed: " + a.Rationale
		return true, ""
	}
	edited := doc
	for _, e := range a.Edits {
		ed := factoryai.Edit{Path: er.Path, Old: e.Old, New: e.New}
		var err error
		if edited, err = ed.Apply(edited); err != nil {
			return false, err.Error()
		}
		d.Edits = append(d.Edits, ed)
	}
	if o.Try == nil {
		d.Problem = "not verified: no factory configured"
		return true, ""
	}
	if p := acceptance(ctx, o.Try, []factorytry.File{{Path: er.Path, Content: edited}}, er.API); p != "" {
		return false, "the factory does not accept the design with it: " + p
	}
	d.Verified, d.Problem = true, ""
	return true, ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
