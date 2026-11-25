package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/semantic"
)

// SemanticReport scores the semantic audit alone (layer ④c findings only).
type SemanticReport struct {
	Repos     map[string]RepoAudit `json:"repos"`
	Rows      []SemanticRow        `json:"rows"`
	Matched   []string             `json:"matched"`
	Unmatched []string             `json:"unmatched"`
}

// RepoAudit is the run cost of one repository.
type RepoAudit struct {
	Judge string         `json:"judge"`
	Stats semantic.Stats `json:"stats"`
}

// SemanticRow is precision and recall of one group of semantic findings.
type SemanticRow struct {
	Group     string          `json:"group"`
	MinConf   string          `json:"min_confidence"`
	Findings  int             `json:"findings"`
	Precision Cell            `json:"precision_lower_bound"`
	Recall    Cell            `json:"recall"`
	ByType    map[string]Cell `json:"recall_by_gt_type"`
}

func semanticReport(rs []repoResult) *SemanticReport {
	audited := false
	sr := &SemanticReport{Repos: map[string]RepoAudit{}}
	for _, r := range rs {
		if r.Audit != nil {
			audited = true
			sr.Repos[r.Repo] = RepoAudit{Judge: r.Judge, Stats: *r.Audit}
		}
	}
	if !audited {
		return nil
	}
	groups := []struct{ name, minConf string }{
		{"all semantic", "low"},
		{"all semantic", "medium"},
		{"sub-layer " + semantic.SubValue, "low"},
		{"sub-layer " + semantic.SubState, "low"},
		{"sub-layer " + semantic.SubStaleness, "low"},
		{"sub-layer " + semantic.SubModel, "low"},
	}
	for _, g := range groups {
		sr.Rows = append(sr.Rows, semanticRow(rs, g.name, g.minConf, groupFilter(g.name)))
	}
	for _, rule := range rulesSeen(rs) {
		sr.Rows = append(sr.Rows, semanticRow(rs, "rule "+rule, "low", func(f fview) bool { return f.Rule == rule }))
	}
	sr.Matched, sr.Unmatched = semanticDetails(rs)
	return sr
}

func groupFilter(name string) func(fview) bool {
	if sub, ok := strings.CutPrefix(name, "sub-layer "); ok {
		return func(f fview) bool { return f.SubLayer == sub }
	}
	return func(fview) bool { return true }
}

func rulesSeen(rs []repoResult) []string {
	m := map[string]bool{}
	for _, r := range rs {
		for _, f := range r.Findings {
			if f.Type == "semantic" {
				m[f.Rule] = true
			}
		}
	}
	return sortedKeys(m)
}

func semanticRow(rs []repoResult, name, minConf string, keep func(fview) bool) SemanticRow {
	row := SemanticRow{Group: name, MinConf: minConf, ByType: map[string]Cell{}}
	for _, r := range rs {
		var fs []fview
		for _, f := range filter(r.Findings, minConf) {
			if f.Type == "semantic" && keep(f) {
				fs = append(fs, f)
			}
		}
		row.Findings += len(fs)
		for _, f := range fs {
			row.Precision.Total++
			if anyGT(f, r.GT) {
				row.Precision.Hit++
			}
		}
		for _, g := range r.GT {
			hit := anyFinding(fs, g)
			inc(row.ByType, g.Type, hit)
			row.Recall.Total++
			if hit {
				row.Recall.Hit++
			}
		}
	}
	return row
}

func semanticDetails(rs []repoResult) (matched, unmatched []string) {
	for _, r := range rs {
		for _, f := range r.Findings {
			if f.Type != "semantic" {
				continue
			}
			line := fmt.Sprintf("%s [%s/%s] %s — %s", r.Repo, f.Rule, f.Confidence, locs(f), cut(f.Detail, 220))
			if f.Rule == semantic.RuleModelContradiction {
				line += " QUOTE: " + cut(f.Quote, 160)
			}
			if g, ok := firstGT(f, r.GT); ok {
				matched = append(matched, line+" ⇐ GT "+g)
			} else {
				unmatched = append(unmatched, line)
			}
		}
	}
	sort.Strings(matched)
	sort.Strings(unmatched)
	return matched, unmatched
}

// Markdown renders the semantic section.
func (s *SemanticReport) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Semantic audit (layer ④c)\n\n| repo | judge | claims (checkable) | candidates | questions | model requests | answers | quotes rejected | model errors | model s | wall s | git calls |\n|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, k := range sortedKeys(s.Repos) {
		a := s.Repos[k]
		st := a.Stats
		fmt.Fprintf(&b, "| %s | %s | %d (%d) | %d | %d | %d | %d | %d | %d | %.1f | %.1f | %d |\n", k, a.Judge, st.Claims, st.CheckableClaims,
			st.Candidates, st.Questions, st.ModelRequests, st.Answers, st.QuotesRejected, st.ModelErrors, st.ModelSeconds, st.Seconds, st.GitCalls)
	}
	b.WriteString("\n| group | min conf | findings | precision (lower bound) | recall | wrong | obsolete | missing |\n|---|---|---|---|---|---|---|---|\n")
	for _, r := range s.Rows {
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s | %s | %s |\n", r.Group, r.MinConf, r.Findings, r.Precision, r.Recall,
			r.ByType["wrong"], r.ByType["obsolete"], r.ByType["missing"])
	}
	b.WriteString("\n### Semantic findings matching ground truth\n\n")
	for _, m := range s.Matched {
		b.WriteString("- " + m + "\n")
	}
	b.WriteString("\n### Semantic findings not in ground truth (triage)\n\n")
	for _, m := range s.Unmatched {
		b.WriteString("- " + m + "\n")
	}
	return b.String()
}
