package findings

import (
	"fmt"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic"
)

// SemanticCheckPrefix prefixes the check names of the semantic audit.
const SemanticCheckPrefix = "docs.semantic."

// BuildSemantic turns semantic audit issues into a findings/v1 document (collector
// "docs-audit"). Every finding is a proposal: needs_human is always true and the severity is
// at most low.
func BuildSemantic(p Params, issues []semantic.Issue) *Document {
	d := &Document{
		SchemaVersion: SchemaVersion,
		Collector:     Collector{Name: "docs-audit", Version: p.Version, Tools: p.Tools},
		Run:           Run{StartedAt: p.Started, FinishedAt: p.Finished, Instance: p.Instance},
		Coverage:      coverage(Params{Repo: p.Repo, Gaps: p.Gaps}),
		Findings:      []Finding{},
	}
	counts := map[string]int{}
	for _, is := range issues {
		d.Findings = append(d.Findings, semanticFinding(p, is))
		counts[is.Rule]++
	}
	for _, r := range sortedRules(counts) {
		d.Metrics = append(d.Metrics, Metric{
			Name: "docs_semantic_findings", Value: float64(counts[r]),
			Labels: map[string]string{"repo": p.Repo, "rule": r, "sub_layer": semantic.SubLayer(r)},
			Help:   "Semantic audit findings by rule",
		})
	}
	return d
}

func sortedRules(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// SemanticAsset is the stable asset id of a semantic issue (no line number).
func SemanticAsset(repo string, is semantic.Issue) string {
	return fmt.Sprintf("doc:%s/%s#semantic:%s:%s", repo, is.Path, is.Rule, is.Subject)
}

func semanticFinding(p Params, is semantic.Issue) Finding {
	asset := SemanticAsset(p.Repo, is)
	check := SemanticCheckPrefix + is.Rule
	sev := "info"
	if is.Confidence >= model.ConfMedium {
		sev = "low"
	}
	a := DocsAttributes{
		FindingType: "semantic", RefKind: "claim", Subject: truncate(is.Subject, 500),
		Confidence: is.Confidence.String(), DetectorLayer: "semantic", Suggestion: is.Suggestion,
		Rule: is.Rule, SubLayer: is.SubLayer(), Claim: truncate(is.Claim, 500), Quote: truncate(is.Quote, 500),
		Model: is.Model, PromptHash: is.PromptHash, Votes: is.Votes,
	}
	var ev []string
	for _, l := range is.Locations {
		a.Locations = append(a.Locations, Location(l))
		ev = append(ev, fmt.Sprintf("%s:%d", l.Path, l.Line))
	}
	return Finding{
		ID: ID(Layer, asset, check), Check: check, Layer: Layer, Asset: asset,
		Title:    truncate(fmt.Sprintf("%s:%d: %s", is.Path, is.Line, is.Detail), 300),
		Severity: sev, Evidence: truncate(strings.Join(ev, ", "), 2000), Remediation: is.Suggestion,
		NeedsHuman: true, Owner: p.Owner, ObservedAt: p.Finished, Kind: "compliance",
		Attributes: map[string]any{"docs": a},
	}
}
