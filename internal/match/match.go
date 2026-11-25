package match

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
)

// Issue types (attributes.docs.finding_type).
const (
	TypeWrong        = "wrong_or_obsolete"
	TypeUndocumented = "undocumented"
	TypeOutside      = "outside_repo"
	TypeResidue      = "residue"
)

// Location is a doc or code position.
type Location struct {
	Path string
	Line int
	Role string // "doc" or "code"
}

// Issue is one aggregated problem: the same subject in the same document (or the same symbol
// across code) is reported once, with every location.
type Issue struct {
	Type       string
	Kind       model.Kind
	Subject    string
	Program    string
	Confidence model.Confidence
	// Anchor is the document (wrong/outside/residue) or the first defining file (undocumented).
	Anchor     string
	Locations  []Location
	Suggestion string
}

// RefStat counts references by kind and status (for SLIs).
type RefStat struct {
	Kind   model.Kind
	Status Status
	Count  int
}

// SymStat counts public symbols by kind and whether they are documented.
type SymStat struct {
	Kind       model.Kind
	Documented bool
	Count      int
}

// Report is the matcher output.
type Report struct {
	Issues   []Issue
	RefStats []RefStat
	SymStats []SymStat
}

// Options tunes the matcher.
type Options struct {
	MinConfidence model.Confidence
	Ignore        []config.IgnoreRule
	Reverse       config.Reverse
	GitIgnore     []string
	RelatedRepos  []string
}

// Run matches an inventory.
func Run(inv *scan.Inventory, r *repo.Repo, o Options) *Report {
	ix := newIndex(inv, r, o.GitIgnore)
	ix.related = o.RelatedRepos
	sup := suppressed(inv.Suppressions)
	rep := &Report{}
	refCounts := map[[2]string]int{}
	agg := newAggregator()
	for _, ref := range dedupRefs(inv.References) {
		st, conf := ix.resolve(ref)
		if (st == Unresolved || st == Outside) && (sup[locKey(ref.Path, ref.Line)] || ignored(o.Ignore, ref.Kind, ref.Text, ref.Path, checkFor(st, ref.Kind))) {
			st = Ignored
		}
		refCounts[[2]string{string(ref.Kind), string(st)}]++
		if (st != Unresolved && st != Outside) || conf < o.MinConfidence {
			continue
		}
		agg.addRef(ref, st, conf, ix)
	}
	rep.Issues = agg.issues()
	rep.RefStats = refStats(refCounts)
	if !o.Reverse.Disabled {
		und, stats := reverse(inv, o)
		rep.Issues = append(rep.Issues, und...)
		rep.SymStats = stats
	}
	sortIssues(rep.Issues)
	return rep
}

// CheckName is the stable findings/v1 check name for an issue.
func CheckName(i Issue) string {
	switch i.Type {
	case TypeUndocumented:
		return "docs.symbol.undocumented." + string(i.Kind)
	case TypeOutside:
		return "docs.reference.outside_repo"
	case TypeResidue:
		return "docs.residue.term"
	}
	return "docs.reference.unresolved." + string(i.Kind)
}

func checkFor(st Status, k model.Kind) string {
	if st == Outside {
		return CheckName(Issue{Type: TypeOutside, Kind: k})
	}
	return CheckName(Issue{Type: TypeWrong, Kind: k})
}

func locKey(p string, line int) string { return fmt.Sprintf("%s:%d", p, line) }

func suppressed(s []model.Suppression) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, x := range s {
		m[locKey(x.Path, x.Line)] = true
	}
	return m
}

// ignored applies .docsp.yaml ignore rules (every given field must match).
func ignored(rules []config.IgnoreRule, k model.Kind, text, p, check string) bool {
	for _, r := range rules {
		if r.Kind != "" && r.Kind != string(k) {
			continue
		}
		if r.Text != "" && !globText(r.Text, text) {
			continue
		}
		if r.Path != "" && !repo.Match(r.Path, p) {
			continue
		}
		if r.Check != "" && !globText(r.Check, check) {
			continue
		}
		return true
	}
	return false
}

func globText(pattern, s string) bool {
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

func dedupRefs(refs []model.Reference) []model.Reference {
	seen := map[string]bool{}
	out := make([]model.Reference, 0, len(refs))
	for _, r := range refs {
		k := strings.Join([]string{r.Path, strconv.Itoa(r.Line), string(r.Kind), r.Text, r.Program}, "\x00")
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

func refStats(m map[[2]string]int) []RefStat {
	out := make([]RefStat, 0, len(m))
	for k, n := range m {
		out = append(out, RefStat{Kind: model.Kind(k[0]), Status: Status(k[1]), Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Status < out[j].Status
	})
	return out
}

func sortIssues(is []Issue) {
	sort.SliceStable(is, func(i, j int) bool {
		a, b := is[i], is[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Anchor != b.Anchor {
			return a.Anchor < b.Anchor
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Subject < b.Subject
	})
}
