// Package related picks the documentation sections a pull request is about when there is no
// claim ledger yet (docs-platform/related/v1): the zero-config PR mode. Documents are split
// into sections at their headings; a section is selected when the change edits it, when it
// mentions a changed file or identifier (layer ③'s reference graph), or when its words and
// identifiers overlap the changed code's lines enough. Everything here is deterministic and
// free; only the selected sections are put to the judge afterwards.
package related

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// SchemaVersion of docs-platform/related/v1.
const SchemaVersion = "1.0"

// Reasons a section is selected, in priority order.
const (
	ReasonChanged   = "changed"   // the change edits the section itself
	ReasonReference = "reference" // the section mentions a changed file or identifier
	ReasonOverlap   = "overlap"   // its words and identifiers overlap the changed code
)

// DefaultMaxSections bounds the sections one change selects (each costs judge requests).
const DefaultMaxSections = 8

// MinScore is the overlap score a section needs: one identifier or path term (weight 3), or
// three distinct plain words (weight 1 each).
const MinScore = 3

// Term weights.
const (
	weightWord  = 1
	weightIdent = 3
	weightPath  = 5
)

// Section is one selected documentation section.
type Section struct {
	Path    string   `json:"path"`
	Heading string   `json:"heading"`
	From    int      `json:"from"`
	To      int      `json:"to"`
	Reason  string   `json:"reason"`
	Score   int      `json:"score,omitzero"`  // overlap: term weights; reference: mentions
	Terms   []string `json:"terms,omitempty"` // what matched (at most maxTerms)
}

// Report is docs-platform/related/v1.
type Report struct {
	SchemaVersion string    `json:"schema_version"`
	Base          string    `json:"base,omitempty"`
	Head          string    `json:"head,omitempty"`
	Docs          int       `json:"docs"`       // documentation files considered
	Considered    int       `json:"considered"` // their sections
	Dropped       int       `json:"dropped"`    // related sections left out by the cap
	Sections      []Section `json:"sections"`
}

// Contains reports whether line n of file p is inside a selected section.
func (r *Report) Contains(p string, n int) bool {
	for _, s := range r.Sections {
		if s.Path == p && n >= s.From && n <= s.To {
			return true
		}
	}
	return false
}

// Input of one selection.
type Input struct {
	// Docs maps every documentation file to its lines (at the head).
	Docs map[string][]string
	// Diffs is the change (repo.DiffHunks, merge-base semantics).
	Diffs []repo.FileDiff
	// References are the sections layer ③'s reference graph relates to the change
	// (impact.Derive); optional.
	References []impact.SectionResult
	// Max bounds the selected sections (0 = DefaultMaxSections).
	Max int
}

// Select returns the sections the change is about, deterministic for the same input.
func Select(in Input) *Report {
	maxN := in.Max
	if maxN <= 0 {
		maxN = DefaultMaxSections
	}
	var all []impact.Section
	paths := sortedKeys(in.Docs)
	for _, p := range paths {
		all = append(all, impact.MarkdownSections(p, in.Docs[p])...)
	}
	rep := &Report{SchemaVersion: SchemaVersion, Docs: len(paths), Considered: len(all), Sections: []Section{}}
	picked := map[string]bool{}
	var cands []Section
	add := func(s Section) {
		k := fmt.Sprintf("%s:%d", s.Path, s.From)
		if !picked[k] {
			picked[k] = true
			cands = append(cands, s)
		}
	}
	for _, s := range changedSections(all, in.Diffs) {
		add(s)
	}
	for _, s := range referenceSections(all, in.References) {
		add(s)
	}
	for _, s := range overlapSections(all, in.Docs, in.Diffs) {
		add(s)
	}
	if len(cands) > maxN {
		rep.Dropped = len(cands) - maxN
		cands = cands[:maxN]
	}
	rep.Sections = append(rep.Sections, cands...)
	return rep
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// changedSections are the sections with a line the change added (in file order).
func changedSections(all []impact.Section, diffs []repo.FileDiff) []Section {
	added := map[string][]int{}
	for _, d := range diffs {
		for _, l := range d.Added {
			added[d.Path] = append(added[d.Path], l.N)
		}
	}
	var out []Section
	for _, s := range all {
		for _, n := range added[s.Path] {
			if s.Contains(n) {
				out = append(out, Section{Path: s.Path, Heading: s.Heading, From: s.From, To: s.To, Reason: ReasonChanged})
				break
			}
		}
	}
	return out
}

// referenceSections keeps the reference graph's sections that are documentation sections.
func referenceSections(all []impact.Section, refs []impact.SectionResult) []Section {
	known := map[string]bool{}
	for _, s := range all {
		known[fmt.Sprintf("%s:%d:%d", s.Path, s.From, s.To)] = true
	}
	var out []Section
	for _, r := range refs {
		if !known[fmt.Sprintf("%s:%d:%d", r.Path, r.From, r.To)] {
			continue
		}
		var terms []string
		for _, why := range r.Reasons {
			terms = append(terms, why.Subject)
		}
		terms = dedup(terms)
		out = append(out, Section{
			Path: r.Path, Heading: r.Heading, From: r.From, To: r.To, Reason: ReasonReference, Score: len(terms), Terms: capTerms(terms),
		})
	}
	// the sections mentioning the most changed things first
	sortByScore(out)
	return out
}

// overlapSections scores every section by the changed code's terms it contains; a term that
// appears in many sections says nothing about any of them and is ignored.
func overlapSections(all []impact.Section, docs map[string][]string, diffs []repo.FileDiff) []Section {
	terms := diffTerms(docs, diffs)
	if len(terms) == 0 || len(all) == 0 {
		return nil
	}
	texts := make([]string, len(all))
	toks := make([]map[string]bool, len(all))
	df := map[string]int{}
	for i, s := range all {
		texts[i] = strings.ToLower(strings.Join(docs[s.Path][s.From-1:min(s.To, len(docs[s.Path]))], "\n"))
		toks[i] = tokenSet(texts[i])
		for t := range terms {
			if hasTerm(t, texts[i], toks[i]) {
				df[t]++
			}
		}
	}
	common := max(2, len(all)/5)
	var out []Section
	for i, s := range all {
		score, matched := scoreSection(terms, df, common, texts[i], toks[i])
		if score < MinScore {
			continue
		}
		out = append(out, Section{
			Path: s.Path, Heading: s.Heading, From: s.From, To: s.To, Reason: ReasonOverlap, Score: score, Terms: capTerms(matched),
		})
	}
	sortByScore(out)
	return out
}

// scoreSection sums the weights of the uncommon terms found in a section; the matched terms
// come heaviest first.
func scoreSection(terms, df map[string]int, common int, text string, toks map[string]bool) (int, []string) {
	score, matched := 0, []string{}
	for t, w := range terms {
		if df[t] > common || !hasTerm(t, text, toks) {
			continue
		}
		score += w
		matched = append(matched, t)
	}
	sort.Slice(matched, func(a, b int) bool {
		if terms[matched[a]] != terms[matched[b]] {
			return terms[matched[a]] > terms[matched[b]]
		}
		return matched[a] < matched[b]
	})
	return score, matched
}

func sortByScore(out []Section) {
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		if out[a].Path != out[b].Path {
			return out[a].Path < out[b].Path
		}
		return out[a].From < out[b].From
	})
}

// hasTerm: path terms (with '/') are substrings of the text, the others whole tokens.
func hasTerm(t, text string, toks map[string]bool) bool {
	if strings.Contains(t, "/") {
		return strings.Contains(text, t)
	}
	return toks[t]
}

const maxTerms = 8

func capTerms(t []string) []string {
	if len(t) > maxTerms {
		return t[:maxTerms]
	}
	return t
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// reToken: identifiers, with inner '-' kept (CLI flags such as --max-age → max-age).
var reToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*[A-Za-z0-9_]`)

func tokenSet(lower string) map[string]bool {
	out := map[string]bool{}
	for _, t := range reToken.FindAllString(lower, -1) {
		out[t] = true
	}
	return out
}

// diffTerms are the lower-cased terms of the changed code (documentation files and lock or
// generated files excluded) with their weights: changed paths, identifier-shaped tokens and
// plain words that are not stop words.
func diffTerms(docs map[string][]string, diffs []repo.FileDiff) map[string]int {
	out := weights{}
	for _, d := range diffs {
		name := d.Name()
		if _, isDoc := docs[name]; isDoc || d.Binary || noise(name) {
			continue
		}
		for _, p := range []string{d.Path, d.OldPath} {
			out.addPath(p)
		}
		for _, l := range append(append([]repo.DiffLine{}, d.Added...), d.Removed...) {
			for _, t := range reToken.FindAllString(l.Text, -1) {
				out.put(strings.ToLower(t), tokenWeight(t))
			}
		}
	}
	return out
}

// weights keeps the highest weight seen per term.
type weights map[string]int

func (ws weights) put(t string, w int) {
	if w > 0 && w > ws[t] {
		ws[t] = w
	}
}

// addPath adds a changed file: its path (when in a directory) and its name without extension.
func (ws weights) addPath(p string) {
	if p == "" {
		return
	}
	if strings.Contains(p, "/") {
		ws.put(strings.ToLower(p), weightPath)
	}
	if b := strings.ToLower(strings.TrimSuffix(path.Base(p), path.Ext(p))); len(b) >= 4 && !genericName[b] {
		ws.put(b, weightIdent)
	}
}

// tokenWeight: identifier-shaped tokens (snake, kebab, camel, ALL_CAPS, with digits) weigh
// more than plain words; short tokens and stop words weigh nothing.
func tokenWeight(t string) int {
	l := strings.ToLower(t)
	switch {
	case len(t) < 3 || stop[l]:
		return 0
	case identShaped(t):
		return weightIdent
	case len(t) >= 5 && t == l:
		return weightWord
	}
	return 0
}

func identShaped(t string) bool {
	hasLower, innerUpper, allUpper := false, false, true
	for i, r := range t {
		switch {
		case r == '_' || r == '-' || r >= '0' && r <= '9':
			return true
		case r >= 'a' && r <= 'z':
			hasLower, allUpper = true, false
		case r >= 'A' && r <= 'Z' && i > 0:
			innerUpper = true
		}
	}
	return hasLower && innerUpper || allUpper && len(t) >= 4
}

// noise: lock files, checksums, minified and vector files say nothing about the docs.
func noise(p string) bool {
	b := path.Base(p)
	switch b {
	case "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock", "poetry.lock", "uv.lock", "Gemfile.lock", "composer.lock":
		return true
	}
	for _, s := range []string{".lock", ".min.js", ".min.css", ".svg", ".snap", ".pb.go"} {
		if strings.HasSuffix(b, s) {
			return true
		}
	}
	return false
}

var genericName = setOf("main", "index", "util", "utils", "types", "common", "helpers", "helper", "init", "test", "tests",
	"readme", "setup", "base", "core", "internal", "lib", "mod")

// stop are words that carry no topic: English function words and programming keywords.
var stop = setOf(
	"the", "and", "for", "not", "are", "was", "but", "you", "all", "can", "has", "had", "its", "our", "out", "new", "use",
	"with", "this", "that", "from", "into", "then", "than", "when", "will", "each", "also", "only", "more", "most", "some",
	"such", "must", "none", "true", "false", "null", "nil", "self", "func", "else", "case", "type", "var", "let", "def",
	"about", "above", "after", "again", "being", "below", "between", "both", "could", "doing", "during", "every", "first",
	"further", "having", "other", "same", "should", "their", "there", "these", "those", "through", "under", "until",
	"where", "which", "while", "would", "without", "within", "because", "before", "string", "strings", "return", "returns",
	"const", "import", "package", "class", "public", "private", "static", "final", "struct", "interface", "function",
	"error", "errors", "value", "values", "param", "params", "print", "println", "printf", "sprintf", "errorf", "format",
	"context", "default", "switch", "break", "continue", "range", "export", "async", "await", "yield", "lambda", "raise",
	"except", "finally", "assert", "number", "boolean", "object", "array", "string", "length", "append", "make",
	"todo", "fixme", "http", "https", "www", "com", "org", "github", "license", "copyright", "test", "tests", "testing",
	"expected", "actual", "result", "results", "input", "output", "data", "file", "files", "line", "lines", "name", "names",
)

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// maxReportBytes bounds a related/v1 file read back (audit -sections).
const maxReportBytes = 4 << 20

// Load reads a related/v1 report.
func Load(p string) (*Report, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("related sections: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxReportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("related sections: %w", err)
	}
	if len(b) > maxReportBytes {
		return nil, fmt.Errorf("related sections %s exceeds %d bytes", p, maxReportBytes)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("related sections %s: %w", p, err)
	}
	if !strings.HasPrefix(r.SchemaVersion, "1.") {
		return nil, fmt.Errorf("related sections %s: schema_version %q, want 1.x", p, r.SchemaVersion)
	}
	return &r, nil
}
