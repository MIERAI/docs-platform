// Package agentic is the agentic mode of layer ④(c): a model explores the inspected
// repository through docsp's read-only judge-tools and answers in a structured form that the
// harness checks piece by piece. Two passes:
//
//  1. Repository drift scan (once per repository and HEAD, cached by HEAD SHA): the model
//     reads the history (git_log, git_show, git_changes) and lists the changes that make older
//     documentation wrong. The harness validates every entry (its commits exist and are
//     ancestors of HEAD, its quotes are verbatim, a path it calls removed is really gone);
//     only validated entries are shared with the per-line judgments.
//  2. Per-line judgment (or, cheaper, per document in groups of up to six lines): the model
//     splits a documentation line into facts and gives each a status (contradicted,
//     incomplete_list, outdated_wording, dangling_reference, unverifiable_in_repo, confirmed)
//     with evidence of four kinds (quote, absence, drift, path_missing). The harness re-checks
//     every piece of evidence and derives the verdict from validated evidence only, in the
//     priority contradicted > dangling_reference > incomplete_list > outdated_wording.
//
// Everything goes through judge/v1 (task types repo_drift, claim_facts, doc_facts) and the
// Guard, so the model is swapped by instance configuration and data classification still
// keeps private content away from cloud providers.
package agentic

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Fact statuses (the model's) and categories (the harness's verdict for a line).
const (
	Contradicted       = "contradicted"
	DanglingReference  = "dangling_reference"
	IncompleteList     = "incomplete_list"
	OutdatedWording    = "outdated_wording"
	UnverifiableInRepo = "unverifiable_in_repo"
	Confirmed          = "confirmed"
	// Unknown: the line could not be decided (no facts, unvalidated evidence, no answer).
	Unknown = "unknown"
)

// Problems are the statuses that make a line wrong, in priority order.
var Problems = []string{Contradicted, DanglingReference, IncompleteList, OutdatedWording}

// Statuses are every status a fact may have.
var Statuses = append([]string{Confirmed, UnverifiableInRepo}, Problems...)

// Evidence kinds.
const (
	KindQuote       = "quote"
	KindAbsence     = "absence"
	KindDrift       = "drift"
	KindPathMissing = "path_missing"
)

// Evidence is one piece of evidence for a fact, as the model gave it.
type Evidence struct {
	Kind          string `json:"kind"`
	Path          string `json:"path,omitempty"`
	Line          int    `json:"line,omitempty"`
	Quote         string `json:"quote,omitempty"`
	SearchPattern string `json:"search_pattern,omitempty"`
	SearchScope   string `json:"search_scope,omitempty"`
	SearchRegex   bool   `json:"search_regex,omitempty"`
	DriftID       string `json:"drift_id,omitempty"`
}

// Fact is one fact of a documentation line with the model's status and evidence.
type Fact struct {
	Fact     string     `json:"fact"`
	Status   string     `json:"status"`
	Evidence []Evidence `json:"evidence"`
}

// LineAnswer is the model's answer for one line.
type LineAnswer struct {
	Line   int    `json:"line,omitempty"`
	Facts  []Fact `json:"facts"`
	Reason string `json:"reason"`
}

// DriftEntry is one entry of a drift list.
type DriftEntry struct {
	ID       string     `json:"id,omitempty"`
	Kind     string     `json:"kind"`
	Old      string     `json:"old"`
	New      string     `json:"new"`
	Summary  string     `json:"summary"`
	Commits  []string   `json:"commits"`
	Evidence []QuoteRef `json:"evidence"`
	// AbsentOldPath: the entry's old path was checked and is gone (validated entries only).
	AbsentOldPath bool `json:"absent_old_path,omitempty"`
}

// QuoteRef is a verbatim line of the current worktree.
type QuoteRef struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Quote string `json:"quote"`
}

// Bounds of a decoded answer (anything beyond is dropped).
const (
	maxFacts       = 30
	maxEvidence    = 10
	maxDriftItems  = 80
	maxDocLines    = 20
	maxStringRunes = 2000
)

// ErrNoFacts: the answer object holds no facts list.
var ErrNoFacts = errors.New("answer has no facts")

// ParseLineAnswer decodes a claim_facts answer leniently: numbers may come as strings, a
// single evidence object instead of a list, a quote without a kind is a quote.
func ParseLineAnswer(raw json.RawMessage) (LineAnswer, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return LineAnswer{}, fmt.Errorf("decode answer: %w", err)
	}
	return lineFrom(m)
}

func lineFrom(m map[string]any) (LineAnswer, error) {
	a := LineAnswer{Line: toInt(m["line"]), Reason: toStr(m["reason"])}
	fs, ok := m["facts"].([]any)
	if !ok {
		return a, ErrNoFacts
	}
	for _, f := range fs {
		if len(a.Facts) == maxFacts {
			break
		}
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		a.Facts = append(a.Facts, Fact{
			Fact: toStr(fm["fact"]), Status: strings.ToLower(strings.TrimSpace(toStr(fm["status"]))),
			Evidence: evidenceFrom(fm["evidence"]),
		})
	}
	return a, nil
}

func evidenceFrom(v any) []Evidence {
	var items []any
	switch x := v.(type) {
	case []any:
		items = x
	case map[string]any:
		items = []any{x}
	}
	var out []Evidence
	for _, it := range items {
		em, ok := it.(map[string]any)
		if !ok || len(out) == maxEvidence {
			continue
		}
		e := Evidence{
			Kind: strings.ToLower(strings.TrimSpace(toStr(em["kind"]))), Path: toStr(em["path"]), Line: toInt(em["line"]),
			Quote: toStr(em["quote"]), SearchPattern: toStr(em["search_pattern"]), SearchScope: toStr(em["search_scope"]),
			SearchRegex: toBool(em["search_regex"]), DriftID: toStr(em["drift_id"]),
		}
		if e.Kind == "" && e.Quote != "" {
			e.Kind = KindQuote
		}
		if e.Kind == "drift_id" {
			e.Kind = KindDrift
		}
		out = append(out, e)
	}
	return out
}

// ParseDocAnswer decodes a doc_facts answer: one LineAnswer per line, keyed by line number.
func ParseDocAnswer(raw json.RawMessage) (map[int]LineAnswer, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode answer: %w", err)
	}
	ls, ok := m["lines"].([]any)
	if !ok {
		return nil, errors.New("answer has no lines")
	}
	out := map[int]LineAnswer{}
	for _, l := range ls {
		lm, ok := l.(map[string]any)
		if !ok || len(out) == maxDocLines {
			continue
		}
		a, err := lineFrom(lm)
		if err != nil || a.Line <= 0 {
			continue
		}
		if _, dup := out[a.Line]; !dup {
			out[a.Line] = a
		}
	}
	return out, nil
}

// ParseDriftAnswer decodes a repo_drift answer.
func ParseDriftAnswer(raw json.RawMessage) ([]DriftEntry, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode answer: %w", err)
	}
	ds, ok := m["drift"].([]any)
	if !ok {
		return nil, errors.New("answer has no drift list")
	}
	var out []DriftEntry
	for _, d := range ds {
		dm, ok := d.(map[string]any)
		if !ok || len(out) == maxDriftItems {
			continue
		}
		e := DriftEntry{Kind: toStr(dm["kind"]), Old: toStr(dm["old"]), New: toStr(dm["new"]), Summary: toStr(dm["summary"])}
		if cs, ok := dm["commits"].([]any); ok {
			for _, c := range cs {
				if s := toStr(c); s != "" && len(e.Commits) < maxEvidence {
					e.Commits = append(e.Commits, s)
				}
			}
		}
		for _, ev := range evidenceFrom(dm["evidence"]) {
			e.Evidence = append(e.Evidence, QuoteRef{Path: ev.Path, Line: ev.Line, Quote: ev.Quote})
		}
		out = append(out, e)
	}
	return out, nil
}

func toStr(v any) string {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		s = strconv.FormatBool(x)
	}
	if r := []rune(s); len(r) > maxStringRunes {
		s = string(r[:maxStringRunes])
	}
	return s
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		if x >= 0 && x < math.MaxInt32 {
			return int(x)
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil && n >= 0 && n < math.MaxInt32 {
			return n
		}
	}
	return 0
}

func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	}
	return false
}
