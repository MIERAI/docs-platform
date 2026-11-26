// Package rulecover measures which business rules of an API factory's escape layer are
// executable - pinned down by an example the factory runs - and proposes examples for the
// rest (docs-platform/rulecover/v1).
//
// For each rule of an API with hand-written code the model proposes a mutant: the smallest
// edit of that code that breaks this rule alone. The factory then runs the project with the
// mutant (`factory try`). A rule is covered when a case written in the design (an example or
// a logic example) fails; otherwise nothing in the design would notice the rule being broken,
// and the model proposes a new example. The proposal is kept only when the factory proves
// it: the example passes on the current code and fails on the mutant. Nothing is applied to
// the repository; the model never decides an outcome, the factory's run does.
package rulecover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/factoryai"
	"github.com/MIERAI/docs-platform/internal/factorymanifest"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// SchemaVersion of docs-platform/rulecover/v1.
const SchemaVersion = "1.0"

// Statuses of a rule.
const (
	Covered    = "covered"    // a design case caught the rule's mutant
	Uncovered  = "uncovered"  // the mutant survived every design case
	Unmeasured = "unmeasured" // no usable mutant (none proposed, did not apply or build)
	// BaselineFailing: the API's cases fail without any change; nothing can be measured.
	BaselineFailing = "baseline_failing"
)

// Rule is the outcome for one rule.
type Rule struct {
	API  string `json:"api"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// Key identifies what was measured (rule text, design and code); a later run reuses
	// the outcome while the key is unchanged.
	Key    string  `json:"key"`
	Status string  `json:"status"`
	Mutant *Mutant `json:"mutant,omitempty"`
	// CaughtBy are the design cases that failed on the mutant; CaughtElsewhere the
	// factory-derived or hand-written cases that did (they guard the code, not the design).
	CaughtBy        []string  `json:"caught_by,omitempty"`
	CaughtElsewhere []string  `json:"caught_elsewhere,omitempty"`
	Proposal        *Proposal `json:"proposal,omitempty"`
	Problem         string    `json:"problem,omitempty"`
}

// Mutant is the model's edit that breaks the rule in the hand-written code.
type Mutant struct {
	factoryai.Edit
	Rationale string `json:"rationale"`
}

// Proposal is a drafted example.
type Proposal struct {
	Inserts   []factoryai.Insert `json:"inserts"`
	Rationale string             `json:"rationale"`
	// Verified: the factory ran the example - it passes on the current code and fails on
	// the mutant. Only verified proposals are shown to people.
	Verified bool     `json:"verified"`
	Catches  []string `json:"catches,omitempty"`
	Problem  string   `json:"problem,omitempty"`
}

// Report is docs-platform/rulecover/v1.
type Report struct {
	SchemaVersion string `json:"schema_version"`
	Repo          string `json:"repo"`
	Ref           string `json:"ref"`
	Rules         []Rule `json:"rules"`
	Summary       struct {
		Rules     int `json:"rules"`
		Measured  int `json:"measured"`
		Covered   int `json:"covered"`
		Uncovered int `json:"uncovered"`
		Proposals int `json:"verified_proposals"`
		// Coverage is covered / measured: the share of rules with hand-written code that an
		// example of the design pins down (nil when nothing was measured).
		Coverage *float64 `json:"coverage"`
		Reused   int      `json:"reused"`
	} `json:"summary"`
	USD float64 `json:"usd"`
}

// Options of a measurement.
type Options struct {
	Repo, Dir, Ref string
	Manifest       *factorymanifest.Manifest
	Guard          *judge.Guard
	Try            factorytry.Runner
	// Previous is the last report: rules whose key is unchanged are reused.
	Previous *Report
	// Propose drafts examples for uncovered rules.
	Propose bool
	Workers int
	// Only limits the run to these API ids (none = all).
	Only []string
}

// job is one rule to measure, with the files it is measured on.
type job struct {
	er    factorymanifest.EscapedRule
	rule  Rule
	files map[string]string
	doc   string
}

// Measure measures the manifest's escaped rules.
func Measure(ctx context.Context, o Options) (*Report, error) {
	rep := &Report{SchemaVersion: SchemaVersion, Repo: o.Repo, Ref: o.Ref, Rules: []Rule{}}
	prev := map[string]Rule{}
	if o.Previous != nil {
		for _, r := range o.Previous.Rules {
			if r.Status != Unmeasured {
				prev[r.Key] = r
			}
		}
	}
	jobs, err := collect(o)
	if err != nil {
		return nil, err
	}
	baseline := baselines(ctx, o, jobs, prev)
	parallel(len(jobs), o.Workers, func(k int) {
		j := jobs[k]
		if r, ok := prev[j.rule.Key]; ok {
			j.rule = r
			return
		}
		if p := baseline[j.er.API]; p != "" {
			j.rule.Status, j.rule.Problem = BaselineFailing, p
			return
		}
		measure(ctx, o, j)
	})
	for _, j := range jobs {
		rep.Rules = append(rep.Rules, j.rule)
		if _, ok := prev[j.rule.Key]; ok {
			rep.Summary.Reused++
		}
	}
	rep.summarise()
	if o.Guard != nil {
		rep.USD = o.Guard.Stats().CostUSD
	}
	return rep, nil
}

// collect reads each escaped rule's document and hand-written files.
func collect(o Options) ([]*job, error) {
	var jobs []*job
	for _, er := range o.Manifest.EscapedRules() {
		if len(er.Files) == 0 || (len(o.Only) > 0 && !contains(o.Only, er.API)) {
			continue
		}
		doc, err := read(o.Dir, er.Path)
		if err != nil {
			return nil, err
		}
		files := map[string]string{}
		for _, f := range er.Files {
			if files[f], err = read(o.Dir, f); err != nil {
				return nil, err
			}
		}
		text := factoryai.RuleBlock(doc, er.Line)
		jobs = append(jobs, &job{er: er, files: files, doc: doc, rule: Rule{API: er.API, Path: er.Path, Line: er.Line, Text: text, Key: key(text, doc, files)}})
	}
	return jobs, nil
}

// baselines runs each API with something new to measure unchanged: its cases must pass as
// they are, or no mutant can be judged. The result maps an API to its problem ("" = fine).
func baselines(ctx context.Context, o Options, jobs []*job, prev map[string]Rule) map[string]string {
	var need []string
	for _, j := range jobs {
		if _, ok := prev[j.rule.Key]; !ok && !contains(need, j.er.API) {
			need = append(need, j.er.API)
		}
	}
	sort.Strings(need)
	out := map[string]string{}
	var mu sync.Mutex
	parallel(len(need), o.Workers, func(k int) {
		p := baselineProblem(o.Try.Try(ctx, nil, []string{need[k]}))
		mu.Lock()
		out[need[k]] = p
		mu.Unlock()
	})
	return out
}

func baselineProblem(res *factorytry.Result, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case res.Status != factorytry.StatusOK:
		return fmt.Sprintf("%s at %s: %s", res.Status, res.Stage, firstLine(res.Message))
	case len(res.Failed()) > 0:
		return fmt.Sprintf("%d cases fail without any change (first: %s)", len(res.Failed()), res.Failed()[0].Name)
	case res.Ran(factorytry.Design...) == 0:
		return "no design case ran"
	}
	return ""
}

// parallel runs fn(0..n-1) on at most workers goroutines.
func parallel(n, workers int, fn func(int)) {
	sem := make(chan struct{}, max(1, workers))
	var wg sync.WaitGroup
	for k := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func(k int) {
			defer func() { <-sem; wg.Done() }()
			fn(k)
		}(k)
	}
	wg.Wait()
}

func (rep *Report) summarise() {
	s := &rep.Summary
	s.Rules = len(rep.Rules)
	for _, r := range rep.Rules {
		switch r.Status {
		case Covered:
			s.Measured++
			s.Covered++
		case Uncovered:
			s.Measured++
			s.Uncovered++
			if r.Proposal != nil && r.Proposal.Verified {
				s.Proposals++
			}
		}
	}
	if s.Measured > 0 {
		c := float64(s.Covered) / float64(s.Measured)
		s.Coverage = &c
	}
}

const mutantSchema = `{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},"rationale":{"type":"string"}},"required":["path","old","new","rationale"],"additionalProperties":false}`

const mutantTask = `RULE (%s line %d):
<<<
%s
>>>
The rule is implemented by the hand-written code below. Propose the smallest edit to ONE of these files that makes the code violate THIS rule - and only this rule - for some inputs, while it still compiles. Typical edits: a threshold, a comparison operator, a rounding, a branch condition, a filter. Avoid edits every input notices (always returning an error, an empty result): the point is to learn whether the design's examples pin this rule down.
"path" is the file; "old" is text copied exactly from it (whole lines, enough to occur exactly once; never the line numbers); "new" replaces it. Never edit tests. If the rule has no behaviour in this code that an edit could break, reply with "path" empty.%s`

func sources(j *job) []factoryai.Source {
	srcs := []factoryai.Source{{Path: j.er.Path, Text: j.doc}}
	for _, f := range j.er.Files {
		srcs = append(srcs, factoryai.Source{Path: f, Text: j.files[f]})
	}
	return srcs
}

func measure(ctx context.Context, o Options, j *job) {
	r := &j.rule
	var lastErr string
	for range 2 {
		done, problem := tryMutant(ctx, o, j, lastErr)
		if done {
			return
		}
		lastErr = problem
	}
	r.Status, r.Problem = Unmeasured, lastErr
}

// tryMutant asks for one mutant and runs it. done = the rule has its outcome; otherwise
// problem says why this mutant was not usable (the next attempt is told).
func tryMutant(ctx context.Context, o Options, j *job, lastErr string) (bool, string) {
	r, er := &j.rule, j.er
	retry := ""
	if lastErr != "" {
		retry = "\nA previous attempt failed: " + lastErr + ". Propose a different edit."
	}
	var m Mutant
	if err := factoryai.Ask(ctx, o.Guard, judgev1.TaskRuleMutant, o.Repo, er.Path, er.Line, r.Text,
		fmt.Sprintf(mutantTask, er.Path, er.Line, r.Text, retry), sources(j), mutantSchema, &m); err != nil {
		r.Status, r.Problem = Unmeasured, err.Error()
		return true, ""
	}
	if m.Path == "" {
		r.Status, r.Problem = Unmeasured, "the model found no edit that breaks the rule alone: "+m.Rationale
		return true, ""
	}
	orig, ok := j.files[m.Path]
	if !ok {
		return false, m.Path + " is not one of the rule's hand-written files"
	}
	mutated, err := m.Apply(orig)
	if err != nil {
		return false, err.Error()
	}
	res, err := o.Try.Try(ctx, []factorytry.File{{Path: m.Path, Content: mutated}}, []string{er.API})
	if err != nil {
		r.Status, r.Problem = Unmeasured, err.Error()
		return true, ""
	}
	if res.Status != factorytry.StatusOK {
		return false, fmt.Sprintf("the mutant did not get through the factory (%s at %s: %s)", res.Status, res.Stage, firstLine(res.Message))
	}
	r.Mutant = &m
	r.CaughtBy = names(res.Failed(factorytry.Design...))
	r.CaughtElsewhere = names(res.Failed(factorytry.SourceAuto, factorytry.SourceDifferential, factorytry.SourceHandTest))
	r.Status = Uncovered
	if len(r.CaughtBy) > 0 {
		r.Status = Covered
	} else if o.Propose {
		r.Proposal = propose(ctx, o, j, factorytry.File{Path: m.Path, Content: mutated})
	}
	return true, ""
}

const exampleSchema = `{"type":"object","properties":{"inserts":{"type":"array","maxItems":3,"items":{"type":"object","properties":{"anchor":{"type":"string"},"text":{"type":"string"}},"required":["anchor","text"],"additionalProperties":false}},"rationale":{"type":"string"}},"required":["inserts","rationale"],"additionalProperties":false}`

const exampleTask = `RULE (%s line %d):
<<<
%s
>>>
No example of the design notices when the code breaks this rule: the MUTANT below changed the code and every example still passed.
MUTANT (%s):
<<<
%s
>>>
=== replaced by ===
<<<
%s
>>>
Propose ONE new example for %s that holds for the current code and for the rule, and that the mutant would fail. Write it exactly in the format, keys, indentation and language of the neighbouring examples. If it needs its own data rows, add them too (prefer values no other example uses, so existing examples keep their results).
Each insert goes right after the line "anchor", copied exactly from %s (a whole line, occurring once; never the line numbers); "text" is the lines to insert. At most 3 inserts. If the rule cannot be pinned down by an example in this design, reply with no inserts.%s`

func propose(ctx context.Context, o Options, j *job, mutant factorytry.File) *Proposal {
	p := &Proposal{}
	var lastErr string
	for range 2 {
		done, problem := proposeOnce(ctx, o, j, mutant, p, lastErr)
		if done {
			return p
		}
		lastErr = problem
	}
	p.Problem = lastErr
	return p
}

// proposeOnce asks for one example and has the factory check it. done = p has its outcome.
func proposeOnce(ctx context.Context, o Options, j *job, mutant factorytry.File, p *Proposal, lastErr string) (bool, string) {
	r, er := &j.rule, j.er
	retry := ""
	if lastErr != "" {
		retry = "\nA previous proposal failed: " + lastErr + ". Fix it."
	}
	var a struct {
		Inserts []struct {
			Anchor string `json:"anchor"`
			Text   string `json:"text"`
		} `json:"inserts"`
		Rationale string `json:"rationale"`
	}
	if err := factoryai.Ask(ctx, o.Guard, judgev1.TaskDraftExample, o.Repo, er.Path, er.Line, r.Text,
		fmt.Sprintf(exampleTask, er.Path, er.Line, r.Text, r.Mutant.Path, r.Mutant.Old, r.Mutant.New, er.Path, er.Path, retry),
		sources(j), exampleSchema, &a); err != nil {
		p.Problem = err.Error()
		return true, ""
	}
	p.Rationale, p.Inserts = a.Rationale, nil
	if len(a.Inserts) == 0 {
		p.Problem = "the model proposed no example: " + a.Rationale
		return true, ""
	}
	edited := j.doc
	for _, in := range a.Inserts {
		ins := factoryai.Insert{Path: er.Path, Anchor: in.Anchor, Text: in.Text}
		var err error
		if edited, err = ins.Apply(edited); err != nil {
			return false, err.Error()
		}
		p.Inserts = append(p.Inserts, ins)
	}
	ok, msg := verify(ctx, o, er.API, factorytry.File{Path: er.Path, Content: edited}, mutant, p)
	if !ok {
		return false, msg
	}
	p.Verified, p.Problem = true, ""
	return true, ""
}

// verify: the drafted example passes on the current code and a design case fails on the mutant.
func verify(ctx context.Context, o Options, api string, design, mutant factorytry.File, p *Proposal) (bool, string) {
	res, err := o.Try.Try(ctx, []factorytry.File{design}, []string{api})
	if err != nil {
		return false, err.Error()
	}
	if res.Status != factorytry.StatusOK {
		return false, fmt.Sprintf("the design with the example does not get through the factory (%s at %s: %s)", res.Status, res.Stage, firstLine(res.Message))
	}
	if f := res.Failed(); len(f) > 0 {
		return false, fmt.Sprintf("the example fails on the current code (%s: %s)", f[0].Name, firstLine(f[0].Detail))
	}
	res, err = o.Try.Try(ctx, []factorytry.File{design, mutant}, []string{api})
	if err != nil {
		return false, err.Error()
	}
	caught := names(res.Failed(factorytry.Design...))
	if res.Status != factorytry.StatusOK || len(caught) == 0 {
		return false, "the example passes on the mutant too: it does not pin the rule down"
	}
	p.Catches = caught
	return true, ""
}

func key(text, doc string, files map[string]string) string {
	h := sha256.New()
	ks := make([]string, 0, len(files))
	for k := range files {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s", factoryai.PromptVersion, text, doc)
	for _, k := range ks {
		_, _ = fmt.Fprintf(h, "\x00%s\x00%s", k, files[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func read(dir, p string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	if err != nil {
		return "", fmt.Errorf("rulecover: %w", err)
	}
	return string(b), nil
}

func names(cs []factorytry.Case) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Load reads a previous report ("" or a missing file = none).
func Load(p string) (*Report, error) {
	if p == "" {
		return nil, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rulecover: %w", err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("rulecover: %w", err)
	}
	return &r, nil
}
