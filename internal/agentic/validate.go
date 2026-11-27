package agentic

import (
	"context"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// minAbsencePattern is the shortest search an absence may rest on.
const minAbsencePattern = 3

// Validator re-checks the evidence of one line's facts against the worktree.
type Validator struct {
	Tools *judge.Tools
	// Drift holds the ids of the validated drift entries shared with the judgment.
	Drift map[string]bool
	// Doc and Line are the documentation line under test.
	Doc  string
	Line int
	// External are path globs whose content is not evidence (vendored, mirrored, submodules).
	External []string
}

// EvidenceCheck is the outcome for one piece of evidence.
type EvidenceCheck struct {
	Evidence Evidence `json:"evidence"`
	OK       bool     `json:"ok"`
	Reason   string   `json:"reason,omitempty"`
}

// Check validates one piece of evidence:
//   - quote: verbatim (whitespace-normalised) at path:line±2, at least 6 characters, not the
//     line under test itself (±3), in a path the provider may see;
//   - absence: the exact search (search tool semantics, every tracked file in scope, private
//     ones included, the document under test excluded) is re-run; accepted only when nothing
//     matches and the scope selects at least one file (or the scope is itself the file the
//     pattern names, which then does not exist);
//   - drift: the id names a validated drift entry;
//   - path_missing: the path exists neither from the root, nor next to the document, nor
//     (a bare name) as any file or directory name in the repository.
func (v *Validator) Check(ctx context.Context, e Evidence) EvidenceCheck {
	c := EvidenceCheck{Evidence: e}
	if e.Kind == KindQuote && v.external(e.Path) {
		c.Reason = "external source: not this repository's to verify"
		return c
	}
	switch e.Kind {
	case KindQuote:
		v.Tools.SetDoc(judge.Doc{Path: v.Doc, Lines: []int{v.Line}})
		q := v.Tools.VerifyNear(e.Path, e.Line, e.Quote)
		c.OK, c.Reason = q.OK, q.Reason
	case KindAbsence:
		c.OK, c.Reason = v.absence(ctx, e)
	case KindDrift:
		c.OK = v.Drift[strings.TrimSpace(e.DriftID)]
		if !c.OK {
			c.Reason = "no validated drift entry " + e.DriftID
		}
	case KindPathMissing:
		c.OK, c.Reason = v.pathMissing(e.Path)
	default:
		c.Reason = "unknown evidence kind " + e.Kind
	}
	return c
}

func (v *Validator) external(p string) bool {
	c, err := judge.CleanPath(p)
	return err == nil && repo.MatchAny(v.External, c)
}

func (v *Validator) absence(ctx context.Context, e Evidence) (bool, string) {
	pat := strings.TrimSpace(e.SearchPattern)
	if len(pat) < minAbsencePattern {
		return false, "search pattern too short"
	}
	scope := e.SearchScope
	if strings.TrimSpace(scope) == "" {
		scope = "*"
	}
	res, err := v.Tools.Search(ctx, judge.SearchQuery{Pattern: pat, Scope: scope, Regex: e.SearchRegex, Max: 1, SkipFile: v.Doc, All: true})
	switch {
	case err != nil:
		return false, err.Error()
	case len(res.Matches) > 0:
		m := res.Matches[0]
		return false, "the search finds " + m.Path + ":" + strconv.Itoa(m.Line)
	case res.FilesSearched == 0 && !strings.Contains(strings.ToLower(scope), strings.ToLower(pat)):
		// an empty scope proves nothing about the text, unless the scope is the very file
		// the pattern looks for ("ENVIRONMENTS.md" in "**/ENVIRONMENTS.md": it is missing)
		return false, "the scope selects no file"
	}
	return true, ""
}

func (v *Validator) pathMissing(p string) (bool, string) {
	p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), "`"))
	c, err := judge.CleanPath(p)
	switch {
	case err != nil:
		return false, err.Error()
	case c == "." || strings.ContainsAny(c, "*?[") || strings.Contains(p, "://"):
		return false, "not a concrete repository path"
	case v.Tools.Exists(c):
		return false, c + " exists"
	}
	if rel := path.Join(path.Dir(v.Doc), c); v.Tools.Exists(rel) {
		return false, rel + " exists (relative to the document)"
	}
	if !strings.Contains(c, "/") {
		for _, f := range v.Tools.TrackedFiles(context.Background()) {
			if slices.Contains(strings.Split(f, "/"), c) {
				return false, "a file or directory named " + c + " exists: " + f
			}
		}
	}
	return true, ""
}

// FactCheck is one fact with its validated evidence.
type FactCheck struct {
	Fact     string          `json:"fact"`
	Status   string          `json:"status"`
	Valid    bool            `json:"valid"`
	Evidence []EvidenceCheck `json:"evidence,omitempty"`
}

// Derivation is the verdict for one line derived from validated evidence.
type Derivation struct {
	// Category is the harness's verdict: one of Problems, confirmed, unverifiable_in_repo or
	// unknown.
	Category string `json:"category"`
	// Claimed is what the model's own statuses say, before validation (diagnostics only).
	Claimed    string         `json:"claimed"`
	Facts      []FactCheck    `json:"facts"`
	Evidence   int            `json:"evidence"`
	EvidenceOK int            `json:"evidence_ok"`
	Kinds      map[string]int `json:"kinds,omitempty"`
}

// Derive validates every piece of evidence of a line's facts and derives the category:
//  1. a fact is valid when at least one of its pieces of evidence is (unverifiable_in_repo
//     needs none);
//  2. the first problem status, in priority order (contradicted > dangling_reference >
//     incomplete_list > outdated_wording), held by a valid fact decides;
//     A valid sources_conflict fact (quotes from two places) decides next.
//  3. otherwise confirmed when every fact is a valid confirmed fact; unverifiable_in_repo when
//     every fact is valid and confirmed or unverifiable; unknown in all other cases (no facts,
//     invalid evidence, unknown statuses).
func Derive(ctx context.Context, v *Validator, facts []Fact) Derivation {
	d := Derivation{Category: Unknown, Claimed: claimed(facts), Kinds: map[string]int{}}
	for _, f := range facts {
		fc := FactCheck{Fact: f.Fact, Status: f.Status}
		for _, e := range f.Evidence {
			c := v.Check(ctx, e)
			d.Evidence++
			k := e.Kind
			if !c.OK {
				k += "_bad"
			} else {
				k += "_ok"
				d.EvidenceOK++
				fc.Valid = true
			}
			d.Kinds[k]++
			fc.Evidence = append(fc.Evidence, c)
		}
		switch f.Status {
		case UnverifiableInRepo:
			fc.Valid = true
		case SourcesConflict:
			fc.Valid = conflictShown(fc.Evidence)
		}
		d.Facts = append(d.Facts, fc)
	}
	d.Category = category(d.Facts)
	return d
}

// conflictShown: a sources_conflict fact needs validated quotes from at least two places.
func conflictShown(es []EvidenceCheck) bool {
	places := map[string]bool{}
	for _, e := range es {
		if e.OK && e.Evidence.Kind == KindQuote {
			places[e.Evidence.Path+":"+strconv.Itoa(e.Evidence.Line)] = true
		}
	}
	return len(places) >= 2
}

func category(fs []FactCheck) string {
	if len(fs) == 0 {
		return Unknown
	}
	for _, p := range append(slices.Clone(Problems), SourcesConflict) {
		for _, f := range fs {
			if f.Status == p && f.Valid {
				return p
			}
		}
	}
	allConfirmed, allSettled := true, true
	for _, f := range fs {
		allConfirmed = allConfirmed && f.Status == Confirmed && f.Valid
		allSettled = allSettled && f.Valid && (f.Status == Confirmed || f.Status == UnverifiableInRepo)
	}
	switch {
	case allConfirmed:
		return Confirmed
	case allSettled:
		return UnverifiableInRepo
	}
	return Unknown
}

func claimed(fs []Fact) string {
	for _, p := range Problems {
		for _, f := range fs {
			if f.Status == p {
				return p
			}
		}
	}
	if len(fs) == 0 {
		return Unknown
	}
	for _, f := range fs {
		if f.Status != Confirmed {
			return "other"
		}
	}
	return Confirmed
}

// IsProblem reports whether a category says the line is wrong.
func IsProblem(category string) bool { return slices.Contains(Problems, category) }
