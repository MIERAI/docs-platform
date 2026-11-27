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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factoryai"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/prtext"
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
	Block    string         `json:"block,omitempty"` // the whole rule (condition and result) at the head
	Touched  []string       `json:"touched"`         // the changed files that make it relevant
	Category string         `json:"category"`
	Facts    []ledger.Fact  `json:"facts,omitempty"`
	Quotes   []ledger.Quote `json:"quotes,omitempty"`
	Error    string         `json:"error,omitempty"`
	Draft    *Draft         `json:"draft,omitempty"`
	// Code is the code lines the verdict cites, as they read at the head.
	Code []Line `json:"code,omitempty"`
	// Evidence of a problem: executed (the design's examples of the API fail at the head)
	// or read (only the judge's reading of the code).
	Evidence string `json:"evidence,omitempty"`
}

// Evidence levels of a problem.
const (
	EvidenceExecuted = "executed"
	EvidenceRead     = "read"
)

// Line is one line of a file at the head.
type Line struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Draft is a proposed design edit.
type Draft struct {
	Edits     []factoryai.Edit `json:"edits"`
	Rationale string           `json:"rationale"`
	Verified  bool             `json:"verified"`
	Problem   string           `json:"problem,omitempty"`
	// For the person who decides, in the design's language: what differs, the one yes/no
	// question that decides it, and a concrete input on which the two disagree.
	Summary        string `json:"summary,omitempty"`
	Question       string `json:"question,omitempty"`
	Counterexample string `json:"counterexample,omitempty"`
	// Patch is the draft as a unified diff (`git apply` in the change's checkout).
	Patch string `json:"patch,omitempty"`
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
	// Failing is the design's cases (examples, logic examples, auto, differential) that fail
	// at the head as it is, per touched API: executed counterexamples.
	Failing map[string][]factorytry.Case `json:"failing,omitempty"`
	// Diffs is the change to each hand-written file behind a problem (truncated).
	Diffs map[string]string `json:"diffs,omitempty"`
	// GitHub is owner/name of the repository: the comment links to lines at the head.
	GitHub string `json:"github,omitempty"`
	// Feedback is what people ticked in the posted comment before this run.
	Feedback []Feedback `json:"feedback,omitempty"`
	USD      float64    `json:"usd"`
	// Incomplete lists why the run did not finish (prtext.Reason*), as the caller states it;
	// judge failures on touched rules are added when rendering.
	Incomplete []string `json:"incomplete,omitempty"`

	// Rendering options (not part of the report).
	Lang        prtext.Lang `json:"-"` // "" = zh
	MaxProblems int         `json:"-"` // problems one comment shows (0 = prtext.DefaultMaxProblems)
	SummaryURL  string      `json:"-"` // where the full list is (the job summary)
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

// Changed is the head commit of dir and the paths changed since base.
func Changed(ctx context.Context, dir, base string) (string, map[string]bool, error) {
	head, err := ledger.Resolve(ctx, dir, "HEAD")
	if err != nil {
		return "", nil, err //nolint:wrapcheck // already descriptive
	}
	ch, err := ledger.Diff(ctx, dir, base, head)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // already descriptive
	}
	changed := map[string]bool{}
	for p := range ch.Paths {
		changed[p] = true
	}
	return head, changed, nil
}

// Review judges the touched rules at the head and drafts design edits for the contradicted ones.
func Review(ctx context.Context, o Options) (*Report, error) {
	head, changed, err := Changed(ctx, o.Dir, o.Base)
	if err != nil {
		return nil, err
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
	runHead(ctx, o, rep, apis)
	markEvidence(ctx, o, rep)
	return rep, nil
}

// runHead has the factory accept the head as it is, per touched API.
func runHead(ctx context.Context, o Options, rep *Report, apis map[string]bool) {
	if o.Try == nil || len(apis) == 0 {
		return
	}
	rep.Acceptance, rep.Failing = map[string]string{}, map[string][]factorytry.Case{}
	for a := range apis {
		res, err := o.Try.Try(ctx, nil, []string{a})
		rep.Acceptance[a], _ = verdict(res, err)
		if _, f := verdict(res, err, designSources...); len(f) > 0 {
			rep.Failing[a] = f
		}
	}
}

// markEvidence grades each problem and keeps the change to the hand-written files behind it.
// A problem is executed only when the design's examples of its API fail at the head and the
// factory-verified draft that makes them pass rewrites this very rule: failing examples prove
// the API's design and code disagree, not which of its rules is the stale one.
func markEvidence(ctx context.Context, o Options, rep *Report) {
	rep.Diffs = map[string]string{}
	for i := range rep.Rules {
		r := &rep.Rules[i]
		if !problem[r.Category] {
			continue
		}
		r.Evidence = EvidenceRead
		if len(rep.Failing[r.API]) > 0 && draftRewrites(r.Draft, r.Block) {
			r.Evidence = EvidenceExecuted
		}
		for _, f := range r.Touched {
			if _, ok := rep.Diffs[f]; !ok && f != r.Path {
				rep.Diffs[f] = fileDiff(ctx, o.Dir, o.Base, rep.Head, f)
			}
		}
	}
}

// draftRewrites says whether a verified draft edits a line of the rule's block.
func draftRewrites(d *Draft, block string) bool {
	if d == nil || !d.Verified || block == "" {
		return false
	}
	for _, e := range d.Edits {
		for _, l := range strings.Split(e.Old, "\n") {
			if t := strings.TrimSpace(l); t != "" && strings.Contains(block, t) {
				return true
			}
		}
	}
	return false
}

// maxDiffLines bounds a file's diff in the comment.
const maxDiffLines = 40

// fileDiff is the change to one file between two revisions (truncated; "" on error).
func fileDiff(ctx context.Context, dir, base, head, path string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--no-color", "-U2", base, head, "--", path).Output() //nolint:gosec // fixed program, argv vector
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for len(lines) > 0 && !strings.HasPrefix(lines[0], "@@") { // headers: the file is named beside it
		lines = lines[1:]
	}
	if len(lines) > maxDiffLines {
		lines = append(lines[:maxDiffLines], fmt.Sprintf("… (%d more lines)", len(lines)-maxDiffLines))
	}
	return strings.Join(lines, "\n")
}

// unifiedDiff is a design edit as a patch that `git apply` takes in the checkout.
func unifiedDiff(ctx context.Context, path, old, edited string) string {
	dir, err := os.MkdirTemp("", "docsp-patch-")
	if err != nil {
		return ""
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if os.WriteFile(filepath.Join(dir, "a"), []byte(old), 0o600) != nil || os.WriteFile(filepath.Join(dir, "b"), []byte(edited), 0o600) != nil { //nolint:gosec // our own temporary directory
		return ""
	}
	out, _ := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--no-index", "--no-color", "-U2", "a", "b").Output() //nolint:gosec // fixed program, argv vector
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for len(lines) > 0 && !strings.HasPrefix(lines[0], "@@") {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return ""
	}
	return fmt.Sprintf("--- a/%s\n+++ b/%s\n%s\n", path, path, strings.Join(lines, "\n"))
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
	if doc, err := os.ReadFile(filepath.Join(o.Dir, filepath.FromSlash(lr.Path))); err == nil {
		r.Block = factoryai.RuleBlock(string(doc), lr.Line)
	}
	if c, ok := o.Builder.Claim(ctx, lr); ok {
		r.Category, r.Facts, r.Quotes = c.Category, c.Facts, c.Evidence.Quotes
	}
	for _, q := range r.Quotes[:min(3, len(r.Quotes))] {
		if t, ok := o.Builder.LineText(ctx, q.Path, q.Line); ok {
			r.Code = append(r.Code, Line{Path: q.Path, Line: q.Line, Text: t})
		}
	}
	if problem[r.Category] && o.Draft != nil {
		r.Draft = draft(ctx, o, er, &r)
	}
	return r
}

// designSources are the cases a design draft answers for: hand-written tests belong to the
// code (a change that breaks them is the code's business, whatever the design says).
var designSources = []string{factorytry.SourceExample, factorytry.SourceLogicExample, factorytry.SourceAuto, factorytry.SourceDifferential}

// acceptance runs the factory and says what is wrong ("" = everything passes); with sources,
// only failures of those sources count.
func acceptance(ctx context.Context, t factorytry.Runner, files []factorytry.File, api string, sources ...string) string {
	res, err := t.Try(ctx, files, []string{api})
	p, _ := verdict(res, err, sources...)
	return p
}

// verdict says what is wrong with a factory run ("" = everything passes) and which cases of
// the sources (all when none) fail.
func verdict(res *factorytry.Result, err error, sources ...string) (string, []factorytry.Case) {
	switch {
	case err != nil:
		return err.Error(), nil
	case res.Status != factorytry.StatusOK:
		return fmt.Sprintf("%s at %s: %s", res.Status, res.Stage, firstLine(res.Message)), nil
	case res.Problem() != "":
		return "acceptance did not run: " + firstLine(res.Problem()), nil
	}
	f := res.Failed(sources...)
	if len(f) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(f))
	for _, c := range f[:min(3, len(f))] {
		names = append(names, fmt.Sprintf("%s (%s)", c.Name, c.Source))
	}
	return fmt.Sprintf("%d cases fail: %s", len(f), strings.Join(names, "; ")), f
}

const draftSchema = `{"type":"object","properties":{"edits":{"type":"array","maxItems":12,"items":{"type":"object","properties":{"old":{"type":"string"},"new":{"type":"string"}},"required":["old","new"],"additionalProperties":false}},"rationale":{"type":"string"},"summary":{"type":"string"},"question":{"type":"string"},"counterexample":{"type":"string"}},"required":["edits","rationale","summary","question","counterexample"],"additionalProperties":false}`

const draftTask = `RULE (%s line %d):
<<<
%s
>>>
At the head of a change, the hand-written code no longer does what this rule says. Facts checked against the code:
%s
Propose the smallest edits to %s that make the design state what the code does now: the rule's wording, and the expected results of any example that encodes the old behaviour. Keep every other word, the language, keys and indentation exactly. Do not change inputs or data rows unless an example cannot be fixed otherwise. Whether the design is stale or the code is wrong is decided by a person; you only draft the design side.
Each edit replaces "old" - text copied exactly from %s (whole lines, occurring exactly once; never the line numbers) - with "new". At most 12 edits: one edit may cover several adjacent lines (an example's description and its result together). Quote each old text from the file without the "  42| " line-number prefix.
Also write, for the person who decides, in the language of %s (short, concrete, no jargon):
- summary: one sentence - what the design says and what the code does now (e.g. "设计书写超过 300 秒算违约,代码现在是超过 600 秒").
- question: the one yes/no question whose answer decides it (e.g. "SLA 阈值改成 600 秒是有意的吗?").
- counterexample: one concrete input on which they disagree, and what each gives (e.g. "处理时间 400 秒: 设计书说违约,代码说不违约").%s`

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
		Rationale      string `json:"rationale"`
		Summary        string `json:"summary"`
		Question       string `json:"question"`
		Counterexample string `json:"counterexample"`
	}
	if err := factoryai.Ask(ctx, o.Draft, judgev1.TaskDraftRule, o.Repo, er.Path, er.Line, text,
		fmt.Sprintf(draftTask, er.Path, er.Line, text, facts, er.Path, er.Path, er.Path, retry), srcs, draftSchema, &a); err != nil {
		d.Problem = err.Error()
		return true, ""
	}
	d.Rationale, d.Edits, d.Patch = a.Rationale, nil, ""
	d.Summary, d.Question, d.Counterexample = a.Summary, a.Question, a.Counterexample
	if len(a.Edits) == 0 {
		d.Problem = "no edit proposed: " + a.Rationale
		return true, ""
	}
	edited := doc
	for _, e := range a.Edits {
		ed := factoryai.Edit{Path: er.Path, Old: e.Old, New: e.New}
		var err error
		if edited, err = ed.Apply(edited); err != nil {
			d.Edits = nil // never show half a draft
			return false, err.Error()
		}
		d.Edits = append(d.Edits, ed)
	}
	d.Patch = unifiedDiff(ctx, er.Path, doc, edited)
	if o.Try == nil {
		d.Problem = "not verified: no factory configured"
		return true, ""
	}
	if p := acceptance(ctx, o.Try, []factorytry.File{{Path: er.Path, Content: edited}}, er.API, designSources...); p != "" {
		return false, "the factory does not accept the design with it: " + p
	}
	d.Verified, d.Problem = true, ""
	return true, ""
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
