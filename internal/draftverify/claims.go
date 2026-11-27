package draftverify

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// claimsGate: the changed lines are extracted again (their references must resolve, as in
// docsp check) and each changed claim of the ledger is re-verified deterministically against
// the evidence it was judged on. What only a model could judge is needs_model.
func claimsGate(ctx context.Context, in *Input, changes []Change) Gate {
	g := Gate{Name: GateClaims}
	refs, err := referenceItems(in, changes)
	if err != nil {
		g.Items = append(g.Items, Item{Kind: "reference", Status: Error, Detail: "re-extraction failed: " + err.Error()})
	}
	g.Items = append(g.Items, refs...)
	g.Items = append(g.Items, claimItems(ctx, in, changes)...)
	g.Status = worst(g.Items, NotApplicable)
	switch g.Status {
	case Pass:
		g.Detail = "every changed reference resolves and every changed claim is backed by its evidence"
	case NeedsModel:
		g.Detail = "some changes can only be judged by a model (not called): not verified"
	case Fail:
		g.Detail = "a changed line contradicts the repository or its evidence"
	case NotApplicable:
		g.Detail = "no changed claim"
	default:
		g.Detail = "a check could not run: not verified"
	}
	return g
}

// claimItems re-verifies each changed line against the ledger.
func claimItems(ctx context.Context, in *Input, changes []Change) []Item {
	if len(changes) == 0 {
		return nil
	}
	head, err := ledger.Resolve(ctx, in.Root, in.Ref)
	if err != nil {
		return []Item{{Kind: "claim", Status: Error, Detail: err.Error()}}
	}
	cv := claimVerifier{ctx: ctx, in: in, head: head}
	oldBlock, newBlock := blockLines(in.Old), blockLines(in.New)
	var out []Item
	for _, c := range changes {
		if trivial(c.OldText) && trivial(c.NewText) {
			continue
		}
		cl := findClaim(in, c)
		switch {
		case cl != nil:
			out = append(out, cv.verify(cl, c))
		case oldBlock[c.Old] || newBlock[c.New]:
			// a code line no claim covers: the format and examples gates decide
		case c.New == 0:
			out = append(out, Item{Line: c.Old, Kind: "claim", Status: Pass, Detail: "an unjudged line is removed; nothing is left to verify"})
		default:
			out = append(out, Item{Line: c.New, Kind: "claim", Subject: clip(c.NewText, 80), Status: NeedsModel, Detail: "no judged claim for this line in the ledger: judging it needs a model"})
		}
	}
	return out
}

// findClaim is the ledger claim of a changed line's old text.
func findClaim(in *Input, c Change) *ledger.Claim {
	if in.Ledger == nil || c.Old == 0 {
		return nil
	}
	if cl := in.Ledger.Find(ledger.ClaimID(in.RepoName, in.Path, c.OldText)); cl != nil {
		return cl
	}
	if in.ClaimID != "" {
		if cl := in.Ledger.Find(in.ClaimID); cl != nil && cl.Path == in.Path && norm(cl.Text) == norm(c.OldText) {
			return cl
		}
	}
	return nil
}

type claimVerifier struct {
	ctx  context.Context
	in   *Input
	head string
	tree *ledger.Tree
}

// verify decides one changed claim: deleting it passes; otherwise its evidence must be
// unchanged since it was judged, the draft may change only fact tokens (code, paths,
// identifiers, numbers, links) - a prose rewording needs a model - and every fact token it
// adds must appear verbatim in the claim's quoted evidence (or, for a link, name an existing
// evidence file).
func (v *claimVerifier) verify(cl *ledger.Claim, c Change) Item {
	it := Item{Line: c.New, Kind: "claim", Subject: cl.ID, Status: Pass}
	if c.New == 0 {
		it.Line, it.Detail = c.Old, "the draft deletes the "+cl.Category+" line; nothing is left to verify"
		return it
	}
	st, d, carried := v.fresh(cl)
	if st != Pass {
		it.Status, it.Detail = st, d
		return it
	}
	added, removed := tokenDiff(draft.Tokens(c.OldText), draft.Tokens(c.NewText))
	if p := prose(added, removed); len(p) > 0 {
		it.Status, it.Detail = NeedsModel, "the draft rewords prose ("+strings.Join(p, " ")+"): judging the new wording needs a model"
		return it
	}
	ev, paths := v.evidence(cl, carried)
	var facts []string
	for _, t := range added {
		if !isFact(t) {
			continue
		}
		st, d := v.backed(t, ev, paths)
		if rank[st] > rank[it.Status] {
			it.Status, it.Detail = st, d
		}
		facts = append(facts, t)
	}
	if it.Status == Pass {
		it.Detail = "removes detail only; the remaining facts were judged " + cl.Category
		if len(facts) > 0 {
			it.Detail = "every new fact (" + strings.Join(facts, " ") + ") is quoted verbatim in the claim's evidence"
		}
		if len(carried) > 0 {
			it.Detail += " (evidence carried forward: its files changed outside the claim's scope)"
		}
	}
	return it
}

// fresh: the claim's evidence still holds at the head - by the ledger's own invalidation,
// so a quote whose file changed only outside the claim's scope is carried forward (its new
// line returned) rather than stale. The document itself is what the draft changes, so its
// own reasons do not count.
func (v *claimVerifier) fresh(cl *ledger.Claim) (Status, string, []ledger.Carry) {
	if cl.Target != nil {
		return NeedsModel, "the claim was judged against another repository (" + cl.Target.Repo + "@" + cl.Target.Ref + "): not re-verified here", nil
	}
	ch, err := ledger.Diff(v.ctx, v.in.Root, cl.VerifiedRef, v.head)
	if err != nil {
		return Error, err.Error(), nil
	}
	st := ledger.Invalidate(cl, ch, func(p string) (string, bool) { return ledger.FileAt(v.ctx, v.in.Root, v.head, p) })
	var why []string
	for _, r := range st.Reasons {
		if r.Path != cl.Path && r.Kind != ledger.ReasonNoEvidence {
			why = append(why, r.Kind+" "+r.Path)
		}
	}
	switch {
	case len(why) > 0:
		return NeedsModel, "the claim's evidence changed since it was judged (" + strings.Join(why, ", ") + "): judging it again needs a model", nil
	case cl.Evidence.Empty():
		return NeedsModel, "the claim rests on no evidence (" + cl.Category + "): judging it needs a model", nil
	}
	return Pass, "", st.Carried
}

// evidence is the text of the claim's quoted lines (±2) at the head and the quoted files. A
// quote's line is where it is now: the carried-forward line when its file changed outside
// the claim's scope, else the ledger's (valid at the quote's blob, which the head still has).
func (v *claimVerifier) evidence(cl *ledger.Claim, carried []ledger.Carry) (string, map[string]bool) {
	paths := map[string]bool{}
	for _, q := range cl.Evidence.Quotes {
		paths[q.Path] = true
	}
	for _, d := range cl.Evidence.Drift {
		for _, f := range d.Files {
			paths[f.Path] = true
		}
	}
	var b strings.Builder
	for _, q := range cl.Evidence.Quotes {
		line := q.Line
		for _, c := range carried {
			if c.Path == q.Path && c.Line == q.Line {
				line = c.NewLine
			}
		}
		text, ok := ledger.FileAt(v.ctx, v.in.Root, v.head, q.Path)
		if !ok {
			continue
		}
		ls := strings.Split(text, "\n")
		if from, to := max(1, line-2), min(len(ls), line+2); from <= to {
			b.WriteString(strings.Join(ls[from-1:to], "\n") + "\n")
		}
	}
	return b.String(), paths
}

var reLinkDest = regexp.MustCompile(`^\[[^\]]*\]\(([^)\s]+)[^)]*\)$`)

// backed decides one added fact token.
func (v *claimVerifier) backed(t, ev string, paths map[string]bool) (Status, string) {
	if m := reLinkDest.FindStringSubmatch(t); m != nil {
		return v.link(m[1], paths)
	}
	val := strings.Trim(t, "`.,;:!?()\"'")
	if val == "" || strings.Contains(ev, val) {
		return Pass, ""
	}
	return Fail, fmt.Sprintf("adds %q, which no evidence quote of the claim shows", val)
}

func (v *claimVerifier) link(dest string, paths map[string]bool) (Status, string) {
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:") {
		return NeedsModel, "links to " + dest + " (outside the repository): needs a model"
	}
	dest, _, _ = strings.Cut(dest, "#")
	if dest == "" {
		return NeedsModel, "links to an anchor: needs a model"
	}
	target := path.Clean(path.Join(path.Dir(v.in.Path), dest))
	if strings.HasPrefix(dest, "/") {
		target = path.Clean(strings.TrimPrefix(dest, "/"))
	}
	if v.tree == nil {
		t, err := ledger.ReadTree(v.ctx, v.in.Root, v.head)
		if err != nil {
			return Error, err.Error()
		}
		v.tree = t
	}
	switch {
	case !v.tree.Exists(target):
		return Fail, "links to " + target + ", which does not exist"
	case !paths[target]:
		return NeedsModel, "links to " + target + ", which is not the claim's evidence: needs a model"
	}
	return Pass, ""
}

// tokenDiff is the multiset difference of two token lists.
func tokenDiff(a, b []string) (added, removed []string) {
	n := map[string]int{}
	for _, t := range a {
		n[t]++
	}
	for _, t := range b {
		if n[t] > 0 {
			n[t]--
			continue
		}
		added = append(added, t)
	}
	m := map[string]int{}
	for _, t := range b {
		m[t]++
	}
	for _, t := range a {
		if m[t] > 0 {
			m[t]--
			continue
		}
		removed = append(removed, t)
	}
	return added, removed
}

// prose lists the changed word tokens that are not facts.
func prose(added, removed []string) []string {
	var out []string
	for _, t := range append(append([]string{}, added...), removed...) {
		if !trivial(t) && !isFact(t) {
			out = append(out, t)
		}
	}
	return out
}

var reCamel = regexp.MustCompile(`[a-z][A-Z]`)

// isFact: a token that states a checkable fact: inline code, a link, a number or version, a
// path or identifier (/, _, ::, camelCase), a flag.
func isFact(t string) bool {
	if strings.HasPrefix(t, "`") || strings.HasPrefix(t, "[") {
		return true
	}
	w := strings.TrimRight(t, ".,;:!?")
	return strings.ContainsAny(w, "0123456789/_@") || strings.Contains(w, "::") || reCamel.MatchString(w) ||
		strings.HasPrefix(w, "-") && len(w) > 1 || strings.Contains(strings.Trim(w, "."), ".")
}

// trivial: no letter or digit (punctuation, markup, blank).
func trivial(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// blockLines are the lines of fenced code blocks (fences included) of a Markdown text.
func blockLines(text string) map[int]bool {
	m := map[int]bool{}
	for _, f := range fences(text) {
		end := f.End
		if end == 0 {
			end = f.Line + len(f.Lines)
		}
		for l := f.Line; l <= end; l++ {
			m[l] = true
		}
	}
	return m
}
