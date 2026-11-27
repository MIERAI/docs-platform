package ledger

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/judge"
)

// StaleSchemaVersion of docs-platform/stale/v1 (1.1: carried_forward).
const StaleSchemaVersion = "1.1"

// Statuses of a claim against a newer revision.
const (
	Fresh = "fresh" // every piece of evidence is unchanged: the verdict still holds
	Stale = "stale" // evidence or the line changed: judge it again
	// Recheck: the claim rests on no evidence (unknown, unverifiable_in_repo); only the
	// periodic recheck of the freshness policy can renew it.
	Recheck = "recheck"
)

// Reason kinds.
const (
	ReasonDocRemoved        = "doc_removed"
	ReasonDocLineChanged    = "doc_line_changed"
	ReasonEvidenceChanged   = "evidence_changed"
	ReasonEvidenceRemoved   = "evidence_removed"
	ReasonDriftChanged      = "drift_evidence_changed"
	ReasonRemovedReappeared = "removed_path_reappeared"
	ReasonAbsenceScope      = "absence_scope_changed"
	ReasonPathAppeared      = "path_appeared"
	ReasonNoEvidence        = "no_evidence"
	ReasonPinnedChanged     = "pinned_changed"
)

// Reason is why a claim is stale (or to be rechecked).
type Reason struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ClaimStatus is one claim's outcome.
type ClaimStatus struct {
	ID       string   `json:"id"`
	Repo     string   `json:"repo"`
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	Category string   `json:"category"`
	Status   string   `json:"status"`
	Reasons  []Reason `json:"reasons,omitempty"`
	// NewLine is the line's number at the new revision when the document changed around it.
	NewLine     int     `json:"new_line,omitempty"`
	VerifiedRef string  `json:"verified_ref"`
	Target      *Target `json:"target,omitempty"`
	// Carried (1.1) are the quotes of a fresh claim whose files changed outside the claim's
	// scope: the verdict is carried forward, the quotes re-anchored (Ledger.Reanchor).
	Carried []Carry `json:"carried_forward,omitempty"`
}

// Invalidate decides one claim from the paths changed since its verified revision (one
// repository: the same changes for the document and its evidence); doc returns a
// document's content at the new revision. No model is involved.
func Invalidate(c *Claim, ch *Changes, doc func(string) (string, bool)) ClaimStatus {
	return InvalidateAcross(c, ch, ch, doc)
}

// InvalidateAcross decides a claim whose document and evidence may live in different
// repositories: docCh are the document repository's changes, ch the evidence repository's.
func InvalidateAcross(c *Claim, docCh, ch *Changes, doc func(string) (string, bool)) ClaimStatus {
	return InvalidateWith(c, docCh, ch, doc, Rules{})
}

// InvalidateWith is InvalidateAcross with footprint rules: when a rule is on, a change in
// the claim's footprint also makes it stale (with that rule's reason kind).
func InvalidateWith(c *Claim, docCh, ch *Changes, doc func(string) (string, bool), r Rules) ClaimStatus {
	s := ClaimStatus{ID: c.ID, Repo: c.Repo, Path: c.Path, Line: c.Line, Category: c.Category, VerifiedRef: c.VerifiedRef, Target: c.Target}
	var rs []Reason
	rs = append(rs, docReasons(c, docCh, doc, &s)...)
	var carried []Carry
	enum := Enumerating(c)
	for _, q := range c.Evidence.Quotes {
		qr, cr := quoteReasons(ch, q, enum)
		rs = append(rs, qr...)
		if cr != nil {
			carried = append(carried, *cr)
		}
	}
	for _, d := range c.Evidence.Drift {
		for _, f := range d.Files {
			rs = append(rs, fileReason(ch, f.Path, ReasonDriftChanged)...)
		}
		if d.RemovedPath != "" {
			if p, ok := addedUnder(ch, d.RemovedPath); ok {
				rs = append(rs, Reason{Kind: ReasonRemovedReappeared, Path: p, Detail: "drift " + d.ID})
			}
		}
	}
	for _, a := range c.Evidence.Absence {
		rs = append(rs, absenceReasons(ch, a)...)
	}
	for _, pm := range c.Evidence.PathMissing {
		rs = append(rs, appearedReasons(ch, pm)...)
	}
	for _, f := range c.Evidence.Pinned {
		rs = append(rs, fileReason(ch, f.Path, ReasonPinnedChanged)...)
	}
	if r.Any() {
		rs = append(rs, footprintReasons(c, ch, r)...)
	}
	s.Reasons = dedupReasons(rs)
	switch {
	case len(s.Reasons) > 0:
		s.Status = Stale
	case c.Evidence.Empty():
		s.Status, s.Reasons = Recheck, []Reason{{Kind: ReasonNoEvidence, Detail: c.Category}}
	default:
		s.Status, s.Carried = Fresh, carried
	}
	return s
}

func docReasons(c *Claim, ch *Changes, doc func(string) (string, bool), s *ClaimStatus) []Reason {
	st, changed := ch.Paths[c.Path]
	if !changed {
		return nil
	}
	if st == 'D' {
		return []Reason{{Kind: ReasonDocRemoved, Path: c.Path}}
	}
	text, ok := doc(c.Path)
	if !ok {
		return []Reason{{Kind: ReasonDocRemoved, Path: c.Path}}
	}
	if n := findLine(text, c.Text, c.Line); n > 0 {
		if n != c.Line {
			s.NewLine = n
		}
		return nil
	}
	return []Reason{{Kind: ReasonDocLineChanged, Path: c.Path}}
}

// findLine returns the number of the line equal (whitespace-normalised) to want, the one
// nearest hint when there are several; 0 when none.
func findLine(text, want string, hint int) int {
	w := strings.Join(strings.Fields(want), " ")
	best := 0
	for i, l := range strings.Split(text, "\n") {
		if strings.Join(strings.Fields(l), " ") != w {
			continue
		}
		if n := i + 1; best == 0 || abs(n-hint) < abs(best-hint) {
			best = n
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func fileReason(ch *Changes, p, kind string) []Reason {
	st, ok := ch.Paths[p]
	switch {
	case !ok:
		return nil
	case st == 'D':
		return []Reason{{Kind: ReasonEvidenceRemoved, Path: p}}
	}
	return []Reason{{Kind: kind, Path: p}}
}

// addedUnder finds an added path equal to p or below it.
func addedUnder(ch *Changes, p string) (string, bool) {
	p = strings.TrimSuffix(p, "/")
	for q, st := range ch.Paths {
		if st == 'A' && (q == p || strings.HasPrefix(q, p+"/")) {
			return q, true
		}
	}
	return "", false
}

func absenceReasons(ch *Changes, a Absence) []Reason {
	m, err := judge.CompileScope(a.Scope)
	if err != nil {
		return []Reason{{Kind: ReasonAbsenceScope, Detail: "scope no longer compiles: " + err.Error()}}
	}
	for _, p := range sortedPaths(ch) {
		if p != a.SkipFile && m(p) {
			return []Reason{{Kind: ReasonAbsenceScope, Path: p, Detail: fmt.Sprintf("%q in %s", a.Pattern, a.Scope)}}
		}
	}
	return nil
}

func appearedReasons(ch *Changes, pm PathMissing) []Reason {
	for _, c := range pm.Candidates {
		if p, ok := addedUnder(ch, c); ok {
			return []Reason{{Kind: ReasonPathAppeared, Path: p, Detail: pm.Path}}
		}
	}
	if pm.Name == "" {
		return nil
	}
	for _, p := range sortedPaths(ch) {
		if ch.Paths[p] != 'A' {
			continue
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == pm.Name {
				return []Reason{{Kind: ReasonPathAppeared, Path: p, Detail: pm.Name}}
			}
		}
	}
	return nil
}

func sortedPaths(ch *Changes) []string {
	ps := make([]string, 0, len(ch.Paths))
	for p := range ch.Paths {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

func dedupReasons(rs []Reason) []Reason {
	seen := map[Reason]bool{}
	var out []Reason
	for _, r := range rs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Report is docs-platform/stale/v1: the claims of one repository against one revision.
type Report struct {
	SchemaVersion string         `json:"schema_version"`
	Repo          string         `json:"repo"`
	Ref           string         `json:"ref"`
	CheckedAt     string         `json:"checked_at"`
	Counts        map[string]int `json:"counts"`
	Claims        []ClaimStatus  `json:"claims"`
}

// Check evaluates every claim of repoName in the ledger against ref of the checkout dir:
// one diff per distinct verified revision, documents read at ref only when they changed.
func Check(ctx context.Context, l *Ledger, dir, repoName, ref string, now time.Time) (*Report, error) {
	return CheckWith(ctx, l, dir, repoName, ref, now, Rules{})
}

// CheckWith is Check with footprint rules.
func CheckWith(ctx context.Context, l *Ledger, dir, repoName, ref string, now time.Time, rules Rules) (*Report, error) {
	to, err := Resolve(ctx, dir, ref)
	if err != nil {
		return nil, err
	}
	r := &Report{
		SchemaVersion: StaleSchemaVersion, Repo: repoName, Ref: to, CheckedAt: now.UTC().Format(time.RFC3339),
		Counts: map[string]int{Fresh: 0, Stale: 0, Recheck: 0}, Claims: []ClaimStatus{},
	}
	diffs := map[string]*Changes{}
	docs := map[string]docAt{}
	doc := func(p string) (string, bool) {
		d, ok := docs[p]
		if !ok {
			d.text, d.ok = FileAt(ctx, dir, to, p)
			docs[p] = d
		}
		return d.text, d.ok
	}
	for i := range l.Claims {
		c := &l.Claims[i]
		if c.Repo != repoName || c.Target != nil {
			continue // claims of other repositories, or judged against a declared target (CheckGroup)
		}
		ch, ok := diffs[c.VerifiedRef]
		if !ok {
			if ch, err = Diff(ctx, dir, c.VerifiedRef, to); err != nil {
				return nil, fmt.Errorf("claim %s: %w", c.ID, err)
			}
			diffs[c.VerifiedRef] = ch
		}
		s := InvalidateWith(c, ch, ch, doc, rules)
		r.Counts[s.Status]++
		r.Claims = append(r.Claims, s)
	}
	sort.SliceStable(r.Claims, func(i, j int) bool {
		if r.Claims[i].Path != r.Claims[j].Path {
			return r.Claims[i].Path < r.Claims[j].Path
		}
		return r.Claims[i].Line < r.Claims[j].Line
	})
	return r, nil
}

// CheckGroup evaluates the claims judged against declared targets (repository groups): the
// document's repository is diffed from the claim's DocRef to its checkout's HEAD, the
// target repository from VerifiedRef to the commit the declared revision now names. dirs
// maps "repo@ref" (preferred) or "repo" to checkouts; claims whose repositories have no
// checkout are skipped.
func CheckGroup(ctx context.Context, l *Ledger, group string, dirs map[string]string, now time.Time) (*Report, error) {
	r := &Report{
		SchemaVersion: StaleSchemaVersion, Repo: group, CheckedAt: now.UTC().Format(time.RFC3339),
		Counts: map[string]int{Fresh: 0, Stale: 0, Recheck: 0}, Claims: []ClaimStatus{},
	}
	g := &groupState{ctx: ctx, dirs: dirs, heads: map[string]string{}, diffs: map[string]*Changes{}, docs: map[string]docAt{}}
	for i := range l.Claims {
		c := &l.Claims[i]
		if c.Target == nil || g.dir(c.Repo, "HEAD") == "" || g.dir(c.Target.Repo, c.Target.Ref) == "" {
			continue
		}
		docTo, err := g.head(c.Repo, "HEAD")
		if err != nil {
			return nil, err
		}
		evTo, err := g.head(c.Target.Repo, c.Target.Ref)
		if err != nil {
			return nil, err
		}
		docCh, err := g.diff(c.Repo, c.docRef(), docTo)
		if err != nil {
			return nil, fmt.Errorf("claim %s: %w", c.ID, err)
		}
		evCh, err := g.diffIn(g.dir(c.Target.Repo, c.Target.Ref), c.VerifiedRef, evTo)
		if err != nil {
			return nil, fmt.Errorf("claim %s: %w", c.ID, err)
		}
		s := InvalidateAcross(c, docCh, evCh, func(p string) (string, bool) { return g.doc(c.Repo, docTo, p) })
		r.Counts[s.Status]++
		r.Claims = append(r.Claims, s)
	}
	sort.SliceStable(r.Claims, func(i, j int) bool {
		a, b := r.Claims[i], r.Claims[j]
		if a.Repo+a.Path != b.Repo+b.Path {
			return a.Repo+a.Path < b.Repo+b.Path
		}
		return a.Line < b.Line
	})
	return r, nil
}

type groupState struct {
	ctx   context.Context
	dirs  map[string]string
	heads map[string]string
	diffs map[string]*Changes
	docs  map[string]docAt
}

// dir is the checkout serving repo@ref: a "repo@ref" entry, else the "repo" entry.
func (g *groupState) dir(repoName, ref string) string {
	if d := g.dirs[repoName+"@"+ref]; d != "" {
		return d
	}
	return g.dirs[repoName]
}

func (g *groupState) head(repoName, ref string) (string, error) {
	k := repoName + "@" + ref
	if h, ok := g.heads[k]; ok {
		return h, nil
	}
	h, err := Resolve(g.ctx, g.dir(repoName, ref), ref)
	if err != nil {
		return "", fmt.Errorf("%s: %w", k, err)
	}
	g.heads[k] = h
	return h, nil
}

func (g *groupState) diff(repoName, from, to string) (*Changes, error) {
	return g.diffIn(g.dir(repoName, "HEAD"), from, to)
}

func (g *groupState) diffIn(dir, from, to string) (*Changes, error) {
	k := dir + "\x00" + from + "\x00" + to
	if ch, ok := g.diffs[k]; ok {
		return ch, nil
	}
	ch, err := Diff(g.ctx, dir, from, to)
	if err != nil {
		return nil, err
	}
	g.diffs[k] = ch
	return ch, nil
}

func (g *groupState) doc(repoName, ref, p string) (string, bool) {
	k := repoName + "\x00" + ref + "\x00" + p
	d, ok := g.docs[k]
	if !ok {
		d.text, d.ok = FileAt(g.ctx, g.dir(repoName, "HEAD"), ref, p)
		g.docs[k] = d
	}
	return d.text, d.ok
}

type docAt struct {
	text string
	ok   bool
}
