// Package impact is layer ④(a): PR impact analysis. A declarative map "code paths → docs" in
// .docsp.yaml says which documentation describes which code. A change that touches mapped code
// without touching any of its docs fails, unless the PR carries an explicit, reasoned waiver
// (a "Docs-Not-Needed: <reason>" trailer in a commit message or the PR description).
package impact

import (
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// SchemaVersion of docs-platform/impact/v1 emitted.
const SchemaVersion = "1.0"

// Rule statuses.
const (
	NotTriggered = "not_triggered"
	DocsUpdated  = "docs_updated"
	MissingDocs  = "missing_docs"
	Waived       = "waived"
)

// Verdicts.
const (
	VerdictPass          = "pass"
	VerdictFail          = "fail"
	VerdictWaived        = "waived"
	VerdictNotApplicable = "not_applicable"
)

// Report is docs-platform/impact/v1.
type Report struct {
	SchemaVersion string       `json:"schema_version"`
	Tool          Tool         `json:"tool"`
	Base          string       `json:"base"`
	Head          string       `json:"head"`
	ChangedFiles  int          `json:"changed_files"`
	Rules         []RuleResult `json:"rules"`
	WaiverReason  string       `json:"waiver_reason,omitempty"`
	Verdict       string       `json:"verdict"`
	// Sections are the doc sections the change affects, derived from the reference graph
	// (advisory: they never change the verdict, which only the explicit rules decide).
	Sections []SectionResult `json:"sections,omitempty"`
	Metrics  []Metric        `json:"metrics,omitempty"`
}

// Tool identifies the producer.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RuleResult is the outcome of one mapping rule.
type RuleResult struct {
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	CodeChanged  []string `json:"code_changed,omitempty"`
	DocsExpected []string `json:"docs_expected,omitempty"`
	DocsChanged  []string `json:"docs_changed,omitempty"`
}

// Metric is an SLI sample (e.g. docs_impacting_change{with_docs="true"}).
type Metric struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// Input is everything Evaluate needs; it does no I/O itself.
type Input struct {
	Base, Head string
	Changed    []string
	// Texts are commit messages and the PR description, searched for the waiver trailer.
	Texts   []string
	Cfg     config.Impact
	Version string
}

// Evaluate applies the impact rules to a change set.
func Evaluate(in Input) *Report {
	rep := &Report{
		SchemaVersion: SchemaVersion, Tool: Tool{Name: "docsp", Version: in.Version},
		Base: in.Base, Head: in.Head, ChangedFiles: len(in.Changed), Rules: []RuleResult{},
	}
	missing, triggered := 0, 0
	for _, r := range in.Cfg.Rules {
		res := evalRule(r, in.Changed)
		if res.Status != NotTriggered {
			triggered++
		}
		if res.Status == MissingDocs {
			missing++
		}
		rep.Rules = append(rep.Rules, res)
	}
	rep.Verdict = verdict(triggered, missing)
	if missing > 0 {
		if reason := Waiver(trailer(in.Cfg), in.Texts); reason != "" {
			rep.WaiverReason, rep.Verdict = reason, VerdictWaived
			for i := range rep.Rules {
				if rep.Rules[i].Status == MissingDocs {
					rep.Rules[i].Status = Waived
				}
			}
		}
	}
	rep.Metrics = metrics(rep)
	return rep
}

func verdict(triggered, missing int) string {
	switch {
	case triggered == 0:
		return VerdictNotApplicable
	case missing > 0:
		return VerdictFail
	}
	return VerdictPass
}

func trailer(c config.Impact) string {
	if c.Trailer == "" {
		return "Docs-Not-Needed"
	}
	return c.Trailer
}

func evalRule(r config.ImpactRule, changed []string) RuleResult {
	res := RuleResult{Name: r.Name, Status: NotTriggered, DocsExpected: r.Docs}
	for _, f := range changed {
		switch {
		case repo.MatchAny(r.Docs, f):
			res.DocsChanged = append(res.DocsChanged, f)
		case repo.MatchAny(r.Code, f):
			res.CodeChanged = append(res.CodeChanged, f)
		}
	}
	sort.Strings(res.CodeChanged)
	sort.Strings(res.DocsChanged)
	switch {
	case len(res.CodeChanged) == 0:
	case len(res.DocsChanged) > 0:
		res.Status = DocsUpdated
	default:
		res.Status = MissingDocs
	}
	return res
}

// Waiver returns the reason given with the trailer key ("Key: reason") in any text, or "".
// A trailer without a reason is not a waiver.
func Waiver(key string, texts []string) string {
	re := regexp.MustCompile(`(?mi)^\s*` + regexp.QuoteMeta(key) + `\s*:\s*(\S.*?)\s*$`)
	for _, t := range texts {
		if m := re.FindStringSubmatch(t); m != nil {
			return truncate(m[1], 1000)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// AddSections attaches derived sections and their metric to a report.
func (rep *Report) AddSections(s []SectionResult) {
	rep.Sections = s
	n := map[string]int{SectionReread: 0, SectionUpdated: 0}
	for _, x := range s {
		n[x.Status]++
	}
	for _, st := range []string{SectionReread, SectionUpdated} {
		rep.Metrics = append(rep.Metrics, Metric{Name: "docs_impact_sections", Value: float64(n[st]), Labels: map[string]string{"status": st}})
	}
}

func metrics(rep *Report) []Metric {
	if rep.Verdict == VerdictNotApplicable {
		return []Metric{{Name: "docs_impact_changes", Value: 1, Labels: map[string]string{"impacting": "false"}}}
	}
	with := "false"
	switch rep.Verdict {
	case VerdictPass:
		with = "true"
	case VerdictWaived:
		with = "waived"
	}
	return []Metric{{Name: "docs_impact_changes", Value: 1, Labels: map[string]string{"impacting": "true", "with_docs": with}}}
}
