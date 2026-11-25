package findings

import (
	"fmt"
	"strings"

	"github.com/MIERAI/docs-platform/internal/match"
	"github.com/MIERAI/docs-platform/internal/model"
)

// Params are the run-level inputs of a document.
type Params struct {
	Repo      string // logical repository name used in asset ids (no host, no owner required)
	Version   string
	Tools     map[string]string
	Started   string
	Finished  string
	Instance  string
	Owner     string
	Gaps      []Gap
	CodeAsset bool // whether the reverse check ran (code:<repo> is covered)
}

// Gap is a file that could not be inspected.
type Gap struct{ Path, Reason string }

// Build turns a matcher report into a findings/v1 document.
func Build(p Params, rep *match.Report) *Document {
	d := &Document{
		SchemaVersion: SchemaVersion,
		Collector:     Collector{Name: "docs-check", Version: p.Version, Tools: p.Tools},
		Run:           Run{StartedAt: p.Started, FinishedAt: p.Finished, Instance: p.Instance},
		Coverage:      coverage(p),
		Findings:      []Finding{},
	}
	for _, is := range rep.Issues {
		d.Findings = append(d.Findings, finding(p, is))
	}
	d.Metrics = metrics(p.Repo, rep)
	return d
}

func coverage(p Params) []Coverage {
	cov := []Coverage{{Asset: "doc:" + p.Repo, Status: "ok"}}
	if p.CodeAsset {
		cov = append(cov, Coverage{Asset: "code:" + p.Repo, Status: "ok"})
	}
	for _, g := range p.Gaps {
		cov = append(cov, Coverage{Asset: "doc:" + p.Repo + "/" + g.Path, Status: "gap", Reason: truncate(g.Reason, 300)})
	}
	return cov
}

// Asset gives the stable asset id of an issue. Line numbers are deliberately not part of it so
// that editing unrelated lines does not reopen findings.
func Asset(repo string, is match.Issue) string {
	if is.Type == match.TypeUndocumented {
		return fmt.Sprintf("code:%s/%s:%s%s", repo, is.Kind, progPrefix(is.Program), is.Subject)
	}
	return fmt.Sprintf("doc:%s/%s#%s:%s%s", repo, is.Anchor, is.Kind, progPrefix(is.Program), is.Subject)
}

func progPrefix(p string) string {
	if p == "" {
		return ""
	}
	return p + " "
}

func finding(p Params, is match.Issue) Finding {
	asset := Asset(p.Repo, is)
	check := match.CheckName(is)
	title, remediation := describe(is)
	return Finding{
		ID: ID(Layer, asset, check), Check: check, Layer: Layer, Asset: asset,
		Title: truncate(title, 300), Severity: severity(is), Evidence: evidence(is),
		Remediation: remediation, NeedsHuman: is.Confidence < model.ConfHigh,
		Owner: p.Owner, ObservedAt: p.Finished, Kind: "compliance",
		Attributes: map[string]any{"docs": attributes(is)},
	}
}

func attributes(is match.Issue) DocsAttributes {
	a := DocsAttributes{
		FindingType: is.Type, RefKind: string(is.Kind), Subject: is.Subject, Program: is.Program,
		Confidence: is.Confidence.String(), DetectorLayer: "reference", Suggestion: is.Suggestion,
	}
	for _, l := range is.Locations {
		a.Locations = append(a.Locations, Location(l))
	}
	return a
}

// severity: documentation drift is never critical/high by itself; confidence drives the rest.
func severity(is match.Issue) string {
	switch {
	case is.Type == match.TypeResidue:
		return "medium"
	case is.Type == match.TypeUndocumented:
		if is.Confidence == model.ConfHigh {
			return "low"
		}
		return "info"
	case is.Confidence == model.ConfHigh:
		return "medium"
	case is.Confidence == model.ConfMedium:
		return "low"
	}
	return "info"
}

func evidence(is match.Issue) string {
	parts := make([]string, 0, len(is.Locations))
	for _, l := range is.Locations {
		parts = append(parts, fmt.Sprintf("%s:%d", l.Path, l.Line))
	}
	return truncate(strings.Join(parts, ", "), 2000)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
