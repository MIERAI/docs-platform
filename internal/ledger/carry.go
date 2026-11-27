package ledger

import (
	"context"
	"path"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Kinds of a quote carried forward: its file changed, its scope did not (the verdict holds).
const (
	CarryReformatted  = "evidence_reformatted"   // only whitespace changed in the file
	CarryMoved        = "evidence_moved"         // the scope's text is unchanged at another line
	CarryOutsideScope = "evidence_outside_scope" // the file changed outside the scope, the quote is where it was
)

// minSurvival is the share of a file's old lines that must still exist for a change to be
// classified at all: a file largely rewritten always re-opens its claims.
const minSurvival = 0.5

// Carry is a quote whose file changed without touching the claim's scope: the verdict is
// carried forward and the quote re-anchored to Blob, NewLine and Scope at the checked
// revision.
type Carry struct {
	Kind    string      `json:"kind"`
	Path    string      `json:"path"`
	Line    int         `json:"line"`
	NewLine int         `json:"new_line"`
	Blob    string      `json:"blob"`
	Scope   *QuoteScope `json:"scope,omitempty"`
	// Ref is the revision the quote is re-anchored at.
	Ref string `json:"ref,omitempty"`
}

// quoteReasons decides one quote. A quote without a scope (ledgers written before scopes)
// is stale whenever its file changed, as before; with a scope, a changed file only re-opens
// the claim when the change touches the scope (or the scope can no longer be found). An
// enumerating claim (a list of components, settings, cases ...) ignores its scopes: its
// completeness depends on the rest of the file (a new case added next to the quoted ones).
func quoteReasons(ch *Changes, q Quote, enumerating bool) ([]Reason, *Carry) {
	st, changed := ch.Paths[q.Path]
	switch {
	case !changed:
		return nil, nil
	case st == 'D':
		return []Reason{{Kind: ReasonEvidenceRemoved, Path: q.Path}}, nil
	case enumerating || q.Scope == nil || st != 'M' || ch.dir == "" || q.Blob == "":
		return []Reason{{Kind: ReasonEvidenceChanged, Path: q.Path}}, nil
	}
	newBlob, ok := ch.blobAt(q.Path)
	if ok && newBlob == q.Blob {
		return nil, nil // already re-anchored at this content
	}
	oldSrc, ok1 := ch.content(q.Blob)
	newSrc, ok2 := ch.fileAt(q.Path)
	if !ok || !ok1 || !ok2 {
		return []Reason{{Kind: ReasonEvidenceChanged, Path: q.Path}}, nil
	}
	c, why := CarryQuote(q, oldSrc, newSrc)
	if why != "" {
		return []Reason{{Kind: ReasonEvidenceChanged, Path: q.Path, Detail: why}}, nil
	}
	c.Blob, c.Ref = newBlob, ch.To
	return nil, &c
}

// CarryQuote classifies the change of a quote's file from oldSrc (the quote's blob) to newSrc
// against the quote's scope. It returns the carry (kind, new line and scope; no blob) or, when
// the claim must be judged again, why.
func CarryQuote(q Quote, oldSrc, newSrc string) (Carry, string) {
	sc := q.Scope
	oldL, newL := splitLines(oldSrc), splitLines(newSrc)
	if sc == nil || sc.Start < 1 || sc.Start > q.Line || q.Line > sc.End || sc.End > len(oldL) {
		return Carry{}, "no usable scope"
	}
	key := lineKey(q.Path)
	oldK, newK := keys(oldL, key), keys(newL, key)
	if equal(oldK, newK) {
		ns := ComputeScope(q.Path, newSrc, q.Line)
		return Carry{Kind: CarryReformatted, Path: q.Path, Line: q.Line, NewLine: q.Line, Scope: ns}, ""
	}
	if survival(oldK, newK) < minSurvival {
		return Carry{}, "file largely rewritten"
	}
	block := oldK[sc.Start-1 : sc.End]
	if sc.Kind == ScopeLines && len(occurrences(oldK, block)) != 1 {
		return Carry{}, "scope " + sc.Kind + " is not unique in the old file"
	}
	found := relocate(q, newSrc, newK, block)
	if len(found) != 1 {
		return Carry{}, "change touches scope " + scopeLabel(sc)
	}
	c := found[0]
	c.Kind = CarryOutsideScope
	if c.NewLine != q.Line {
		c.Kind = CarryMoved
	}
	return c, ""
}

// relocate finds the scope's unchanged text (block) in the new file: every place where it
// still is the same declaration or key with the same bounds (any place, for a line scope).
func relocate(q Quote, newSrc string, newK, block []string) []Carry {
	sc := q.Scope
	var found []Carry
	for _, j := range occurrences(newK, block) {
		nl := j + 1 + q.Line - sc.Start
		ns := ComputeScope(q.Path, newSrc, nl)
		if sc.Kind != ScopeLines && (ns == nil || ns.Kind != sc.Kind || ns.Name != sc.Name || ns.Start != j+1 || ns.End != j+len(block)) {
			continue
		}
		found = append(found, Carry{Path: q.Path, Line: q.Line, NewLine: nl, Scope: ns})
	}
	return found
}

func scopeLabel(s *QuoteScope) string {
	if s.Name != "" {
		return s.Kind + " " + s.Name
	}
	return s.Kind
}

// lineKey is how lines compare: Go ignores whitespace inside a line (gofmt), other files
// (YAML indentation is meaningful) only trailing whitespace.
func lineKey(p string) func(string) string {
	if strings.EqualFold(path.Ext(p), ".go") {
		return func(l string) string { return strings.Join(strings.Fields(l), " ") }
	}
	return func(l string) string { return strings.TrimRight(l, " \t\r") }
}

func keys(lines []string, key func(string) string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = key(l)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// survival is the share of the old non-blank lines still present in the new file.
func survival(oldK, newK []string) float64 {
	have := map[string]int{}
	for _, k := range newK {
		have[k]++
	}
	n, kept := 0, 0
	for _, k := range oldK {
		if k == "" {
			continue
		}
		n++
		if have[k] > 0 {
			have[k]--
			kept++
		}
	}
	if n == 0 {
		return 1
	}
	return float64(kept) / float64(n)
}

// occurrences are the 0-based starts of block in ks.
func occurrences(ks, block []string) []int {
	var out []int
	for j := 0; j+len(block) <= len(ks); j++ {
		if equal(ks[j:j+len(block)], block) {
			out = append(out, j)
		}
	}
	return out
}

// blobAt is the blob of p at the change set's target revision.
func (c *Changes) blobAt(p string) (string, bool) {
	cp, err := judge.CleanPath(p)
	if err != nil || !repo.ValidRef(c.To) {
		return "", false
	}
	return c.cached("blob\x00"+cp, func() (string, error) {
		out, err := repo.Git(c.context(), c.dir, "rev-parse", "--verify", "--quiet", c.To+":"+cp)
		return strings.TrimSpace(out), err
	})
}

// fileAt is the content of p at the change set's target revision.
func (c *Changes) fileAt(p string) (string, bool) {
	cp, err := judge.CleanPath(p)
	if err != nil || !repo.ValidRef(c.To) {
		return "", false
	}
	return c.cached("file\x00"+cp, func() (string, error) {
		return repo.Git(c.context(), c.dir, "show", c.To+":"+cp)
	})
}

// content is the content of a blob.
func (c *Changes) content(blob string) (string, bool) {
	if !isHex(blob) {
		return "", false
	}
	return c.cached("obj\x00"+blob, func() (string, error) {
		return repo.Git(c.context(), c.dir, "show", blob)
	})
}

func (c *Changes) cached(k string, read func() (string, error)) (string, bool) {
	if v, ok := c.files[k]; ok {
		return v.text, v.ok
	}
	if c.files == nil {
		c.files = map[string]docAt{}
	}
	text, err := read()
	d := docAt{text: text, ok: err == nil && text != ""}
	if !d.ok {
		d.text = ""
	}
	c.files[k] = d
	return d.text, d.ok
}

func (c *Changes) context() context.Context {
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func isHex(s string) bool {
	if len(s) < 7 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// BlobAt is the blob of path p at ref in dir ("" and false when it does not exist).
func BlobAt(ctx context.Context, dir, ref, p string) (string, bool) {
	ch := &Changes{To: ref, ctx: ctx, dir: dir}
	return ch.blobAt(p)
}

// Reanchor applies a report's carried-forward quotes to the ledger: each fresh claim with
// carries gets the quotes' new line, blob and scope, marked with the carry's kind and the
// revision (the verdict and verified_ref are unchanged). It returns how many quotes moved.
func (l *Ledger) Reanchor(r *Report) int {
	n := 0
	for _, s := range r.Claims {
		if s.Status != Fresh || len(s.Carried) == 0 {
			continue
		}
		c := l.Find(s.ID)
		if c == nil {
			continue
		}
		done := make([]bool, len(c.Evidence.Quotes))
		for _, cr := range s.Carried {
			for i := range c.Evidence.Quotes {
				q := &c.Evidence.Quotes[i]
				if done[i] || q.Path != cr.Path || q.Line != cr.Line || cr.Blob == "" {
					continue
				}
				q.Line, q.Blob, q.Scope, q.Carried, q.CarriedRef = cr.NewLine, cr.Blob, cr.Scope, cr.Kind, cr.Ref
				done[i] = true
				n++
			}
		}
	}
	return n
}
