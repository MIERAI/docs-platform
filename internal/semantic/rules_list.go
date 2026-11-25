package semantic

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/semantic/evidence"
)

// List matching thresholds: the list must be mostly made of the set's members, and must cover
// at least half of the set, before the rest of the set counts as "missing from the list".
const (
	minOverlap     = 2
	minListShare   = 0.6
	minSetCoverage = 0.5
	maxMissing     = 15
)

// checkLists compares an enumeration in the docs with the code set it enumerates (commands of a
// program, make targets, jobs of a workflow, children of a directory, values of a key).
func (c *checker) checkLists(cl *claim.Claim) {
	for _, l := range cl.Lists {
		if l.Open {
			continue
		}
		s, ok := c.generatedList(cl, l.Items)
		if !ok {
			s, ok = c.bestSet(l.Items)
		}
		if !ok {
			continue
		}
		missing := c.missingFrom(cl, l.Items, s)
		if len(missing) == 0 || len(missing) > maxMissing {
			continue
		}
		conf := model.ConfLow
		if l.Closed {
			conf = model.ConfMedium
		}
		c.add(cl, Issue{
			Rule: RuleListIncomplete, Subject: s.Name, Confidence: conf,
			Detail:     fmt.Sprintf("the list names %d of the %d %s; not listed: %s", len(l.Items)-len(notIn(l.Items, s)), len(s.Items), s.Name, strings.Join(missing, ", ")),
			Suggestion: "Add " + strings.Join(missing, ", ") + " to the list, or say the list is not exhaustive.",
			Quote:      c.quoteLine(s.Path, s.Line), Locations: []Location{{Path: s.Path, Line: s.Line, Role: "code"}},
		})
	}
}

// generatedList matches a list in a sentence about generated files against the generated
// entries next to the document.
func (c *checker) generatedList(cl *claim.Claim, items []string) (evidence.Set, bool) {
	if !slices.Contains(cl.Roles, claim.RoleGenerated) {
		return evidence.Set{}, false
	}
	s := c.ix.GeneratedSet(path.Dir(cl.Path), c.inv.CodeFiles)
	if len(s.Items) < 2 {
		return evidence.Set{}, false
	}
	n := 0
	for _, it := range items {
		for _, v := range variantsOf(it) {
			if s.Items[v] {
				n++
				break
			}
		}
	}
	return s, n >= minOverlap && float64(n)/float64(len(items)) >= minListShare
}

func variantsOf(item string) []string {
	out := []string{item}
	if top, _, ok := strings.Cut(item, "/"); ok {
		out = append(out, top)
	}
	return out
}

// bestSet picks the code set the list most plausibly enumerates.
func (c *checker) bestSet(items []string) (evidence.Set, bool) {
	hits := map[int]map[string]bool{}
	for _, it := range items {
		for _, v := range variantsOf(it) {
			for _, si := range c.ix.SetsWith(v) {
				if hits[si] == nil {
					hits[si] = map[string]bool{}
				}
				hits[si][it] = true
			}
		}
	}
	best, bestScore := -1, 0.0
	idx := make([]int, 0, len(hits))
	for si := range hits {
		idx = append(idx, si)
	}
	sort.Ints(idx)
	for _, si := range idx {
		n, size := float64(len(hits[si])), float64(len(c.ix.Sets[si].Items))
		if n < minOverlap || n/float64(len(items)) < minListShare || n/size < minSetCoverage {
			continue
		}
		if score := n / (float64(len(items)) + size - n); score > bestScore {
			best, bestScore = si, score
		}
	}
	if best < 0 {
		return evidence.Set{}, false
	}
	return c.ix.Sets[best], true
}

// noiseMember are set members a prose list legitimately leaves out.
func noiseMember(m string) bool {
	return m == "" || strings.HasPrefix(m, ".") || strings.HasPrefix(m, "_") || strings.HasSuffix(m, "_test") ||
		strings.HasPrefix(m, "readme") || strings.HasPrefix(m, "license") || m == "help" || m == "version" ||
		m == "testdata" || m == "notice" || m == "main" || m == "changelog" || m == "go" || m == "doc"
}

func (c *checker) missingFrom(cl *claim.Claim, items []string, s evidence.Set) []string {
	listed := map[string]bool{}
	for _, it := range items {
		for _, v := range variantsOf(it) {
			listed[v] = true
		}
	}
	text := strings.ToLower(cl.Text)
	var out []string
	for m := range s.Items {
		if listed[m] || noiseMember(m) || strings.Contains(text, m) {
			continue
		}
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func notIn(items []string, s evidence.Set) []string {
	var out []string
	for _, it := range items {
		in := false
		for _, v := range variantsOf(it) {
			in = in || s.Items[v]
		}
		if !in {
			out = append(out, it)
		}
	}
	return out
}
