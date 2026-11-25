package findings

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/MIERAI/docs-platform/internal/match"
)

var kindNames = map[string]string{
	"cli_command": "command", "cli_flag": "flag", "env_var": "environment variable",
	"config_key": "config key", "metric": "metric", "file_path": "path", "link": "link target",
	"make_target": "make target", "workflow_input": "workflow input", "anchor": "anchor",
}

func kindName(k string) string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return k
}

func describe(is match.Issue) (string, string) {
	k := kindName(string(is.Kind))
	subj := is.Subject
	if is.Program != "" {
		subj = is.Program + " " + is.Subject
	}
	switch is.Type {
	case match.TypeUndocumented:
		return fmt.Sprintf("Undocumented %s %s (defined in %s)", k, subj, is.Anchor),
			fmt.Sprintf("Document the %s %s (defined at %s) in the user-facing docs, or add an ignore rule with a reason if it is internal.", k, subj, firstLoc(is))
	case match.TypeOutside:
		return fmt.Sprintf("%s refers to %s outside the repository", is.Anchor, subj),
			"Readers of this repository cannot follow it: inline the needed information, link a public source, or remove the reference."
	case match.TypeResidue:
		return is.Anchor + " mentions a denylisted term",
			"Remove or neutralise the term (instance denylist: names of private or unrelated projects, internal hosts, personal paths)."
	}
	rem := fmt.Sprintf("%s at %s mentions the %s %s, which this repository does not define. Fix the docs or restore the %s.",
		is.Anchor, firstLoc(is), k, subj, k)
	if is.Suggestion != "" {
		rem += fmt.Sprintf(" Did you mean %s?", is.Suggestion)
	}
	return fmt.Sprintf("Docs mention %s %s that does not exist", k, subj), rem
}

func firstLoc(is match.Issue) string {
	if len(is.Locations) == 0 {
		return is.Anchor
	}
	l := is.Locations[0]
	return fmt.Sprintf("%s:%d", l.Path, l.Line)
}

func metrics(repo string, rep *match.Report) []Metric {
	var out []Metric
	var resolved, total float64
	for _, s := range rep.RefStats {
		out = append(out, Metric{
			Name: "docs_references", Value: float64(s.Count),
			Labels: map[string]string{"repo": repo, "kind": string(s.Kind), "status": string(s.Status)},
			Help:   "Documentation references by kind and resolution status",
		})
		switch s.Status {
		case match.Resolved:
			resolved += float64(s.Count)
			total += float64(s.Count)
		case match.Unresolved:
			total += float64(s.Count)
		default: // external, outside and ignored references are not checkable here
		}
	}
	var documented, public float64
	for _, s := range rep.SymStats {
		out = append(out, Metric{
			Name: "docs_public_symbols", Value: float64(s.Count),
			Labels: map[string]string{"repo": repo, "kind": string(s.Kind), "documented": strconv.FormatBool(s.Documented)},
			Help:   "Public code symbols by kind and whether documentation mentions them",
		})
		public += float64(s.Count)
		if s.Documented {
			documented += float64(s.Count)
		}
	}
	out = append(out, ratio("docs_reference_resolution_ratio", repo, resolved, total, "Resolved / checkable documentation references"))
	out = append(out, ratio("docs_public_symbol_coverage_ratio", repo, documented, public, "Documented / public code symbols"))
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func ratio(name, repo string, num, den float64, help string) Metric {
	v := 1.0
	if den > 0 {
		v = num / den
	}
	return Metric{Name: name, Value: v, Labels: map[string]string{"repo": repo}, Help: help}
}
