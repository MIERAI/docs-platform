package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/semantic"
)

type repoResult struct {
	Repo     string
	Revision string
	GT       []GTItem
	Findings []fview
	Audit    *semantic.Stats
	Judge    string
}

// Cell is recall or precision as found/total.
type Cell struct {
	Hit   int `json:"hit"`
	Total int `json:"total"`
}

func (c Cell) String() string {
	if c.Total == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", c.Hit, c.Total, 100*float64(c.Hit)/float64(c.Total))
}

// Threshold is the score at one minimum confidence.
type Threshold struct {
	MinConfidence string          `json:"min_confidence"`
	Findings      int             `json:"findings"`
	Precision     Cell            `json:"precision_lower_bound"`
	PrecisionBy   map[string]Cell `json:"precision_by_finding_type"`
	Recall        Cell            `json:"recall"`
	RecallByType  map[string]Cell `json:"recall_by_gt_type"`
	RecallByLayer map[string]Cell `json:"recall_by_gt_layer"`
	RecallByRepo  map[string]Cell `json:"recall_by_repo"`
}

// Report is the benchmark result.
type Report struct {
	Repos      map[string]string `json:"repos"`
	GTTotal    int               `json:"gt_total"`
	GTByLayer  map[string]int    `json:"gt_by_layer"`
	Thresholds []Threshold       `json:"thresholds"`
	Matched    []string          `json:"matched"`
	Missed     []string          `json:"missed_gt"`
	Unmatched  []string          `json:"unmatched_findings_sample"`
	Semantic   *SemanticReport   `json:"semantic,omitempty"`
}

func buildReport(rs []repoResult) *Report {
	rep := &Report{Repos: map[string]string{}, GTByLayer: map[string]int{}}
	for _, r := range rs {
		rep.Repos[r.Repo] = r.Revision
		rep.GTTotal += len(r.GT)
		for _, g := range r.GT {
			rep.GTByLayer[g.Layer]++
		}
	}
	for _, c := range []string{"high", "medium", "low"} {
		rep.Thresholds = append(rep.Thresholds, score(rs, c))
	}
	rep.Matched, rep.Unmatched = details(rs)
	rep.Missed = missed(rs)
	rep.Semantic = semanticReport(rs)
	return rep
}

func inc(m map[string]Cell, k string, hit bool) {
	c := m[k]
	c.Total++
	if hit {
		c.Hit++
	}
	m[k] = c
}

func score(rs []repoResult, minConf string) Threshold {
	t := Threshold{
		MinConfidence: minConf, PrecisionBy: map[string]Cell{}, RecallByType: map[string]Cell{},
		RecallByLayer: map[string]Cell{}, RecallByRepo: map[string]Cell{},
	}
	for _, r := range rs {
		fs := filter(r.Findings, minConf)
		t.Findings += len(fs)
		for _, f := range fs {
			hit := anyGT(f, r.GT)
			inc(t.PrecisionBy, f.Type, hit)
			t.Precision.Total++
			if hit {
				t.Precision.Hit++
			}
		}
		for _, g := range r.GT {
			hit := anyFinding(fs, g)
			inc(t.RecallByType, g.Type, hit)
			inc(t.RecallByLayer, g.Layer, hit)
			inc(t.RecallByRepo, r.Repo, hit)
			t.Recall.Total++
			if hit {
				t.Recall.Hit++
			}
		}
	}
	return t
}

func filter(fs []fview, minConf string) []fview {
	var out []fview
	for _, f := range fs {
		if confRank[f.Confidence] >= confRank[minConf] {
			out = append(out, f)
		}
	}
	return out
}

func anyGT(f fview, gt []GTItem) bool {
	for _, g := range gt {
		if matches(f, g) {
			return true
		}
	}
	return false
}

func firstGT(f fview, gt []GTItem) (string, bool) {
	for _, g := range gt {
		if matches(f, g) {
			line := 0
			if g.Line != nil {
				line = *g.Line
			}
			return fmt.Sprintf("[%s/%s] %s:%d", g.Layer, g.Type, g.File, line), true
		}
	}
	return "", false
}

func anyFinding(fs []fview, g GTItem) bool {
	for _, f := range fs {
		if matches(f, g) {
			return true
		}
	}
	return false
}

func details(rs []repoResult) (matched, unmatched []string) {
	for _, r := range rs {
		for _, f := range r.Findings {
			line := fmt.Sprintf("%s [%s/%s] %s %s", r.Repo, f.Type, f.Confidence, f.Subject, locs(f))
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

// missed lists ground-truth items no finding (at any confidence) matched.
func missed(rs []repoResult) []string {
	var out []string
	for _, r := range rs {
		for _, g := range r.GT {
			if anyFinding(r.Findings, g) {
				continue
			}
			line := 0
			if g.Line != nil {
				line = *g.Line
			}
			out = append(out, fmt.Sprintf("[%s/%s] %s %s:%d %s", g.Layer, g.Type, r.Repo, g.File, line, cut(g.Actual, 120)))
		}
	}
	sort.Strings(out)
	return out
}

func cut(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

func locs(f fview) string {
	if len(f.Locs) == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", f.Locs[0].Path, f.Locs[0].Line)
}

// Markdown renders the report.
func (r *Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# docsp benchmark\n\nRepositories (revision):")
	for _, k := range sortedKeys(r.Repos) {
		fmt.Fprintf(&b, " %s@%s", k, r.Repos[k])
	}
	fmt.Fprintf(&b, "\n\nGround truth: %d items; estimated detector layer:", r.GTTotal)
	for _, k := range sortedKeys(r.GTByLayer) {
		fmt.Fprintf(&b, " %s=%d", k, r.GTByLayer[k])
	}
	b.WriteString("\n\n| min confidence | findings | precision (lower bound) | recall all | recall reference-layer | recall semantic | wrong | obsolete | missing |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range r.Thresholds {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s | %s | %s | %s |\n", t.MinConfidence, t.Findings, t.Precision,
			t.Recall, t.RecallByLayer["reference"], t.RecallByLayer["semantic"],
			t.RecallByType["wrong"], t.RecallByType["obsolete"], t.RecallByType["missing"])
	}
	b.WriteString("\nPrecision by finding type and recall by repository:\n\n")
	for _, t := range r.Thresholds {
		fmt.Fprintf(&b, "- %s:", t.MinConfidence)
		for _, k := range sortedKeys(t.PrecisionBy) {
			fmt.Fprintf(&b, " %s %s;", k, t.PrecisionBy[k])
		}
		for _, k := range sortedKeys(t.RecallByRepo) {
			fmt.Fprintf(&b, " recall %s %s;", k, t.RecallByRepo[k])
		}
		b.WriteString("\n")
	}
	if r.Semantic != nil {
		b.WriteString(r.Semantic.Markdown())
	}
	b.WriteString("\n## Matched findings\n\n")
	for _, m := range r.Matched {
		b.WriteString("- " + m + "\n")
	}
	b.WriteString("\n## Ground-truth items not found\n\n")
	for _, m := range r.Missed {
		b.WriteString("- " + m + "\n")
	}
	b.WriteString("\n## Unmatched findings (not in ground truth: triage manually)\n\n")
	for _, m := range r.Unmatched {
		b.WriteString("- " + m + "\n")
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func (r *Report) write(md, js string) error {
	if md != "" {
		if err := os.WriteFile(md, []byte(r.Markdown()), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", md, err)
		}
	}
	if js != "" {
		b, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		if err := os.WriteFile(js, append(b, '\n'), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", js, err)
		}
	}
	return nil
}
