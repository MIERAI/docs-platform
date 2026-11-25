package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// ReplayReport is the history replay benchmark: was the stale section flagged at the moment
// of the change that made it stale?
type ReplayReport struct {
	Repos []ReplayRepo `json:"repos"`
	Rows  []ReplayRow  `json:"rows"`
	Cost  []CostRow    `json:"cost"`
}

// ReplayRow aggregates one slice (split, repo or ground-truth type).
type ReplayRow struct {
	Slice       string `json:"slice"`
	Items       int    `json:"items"`
	Scored      int    `json:"scored"`
	NoCause     int    `json:"no_causing_commit"`
	First       int    `json:"flagged_at_first_causing_commit"`
	Any         int    `json:"flagged_at_any_causing_commit"`
	ByPath      int    `json:"hits_by_path"`
	ByIdent     int    `json:"hits_by_identifier"`
	MedianCause int    `json:"median_causing_commits"`
}

// CostRow is the re-read load per replayed change.
type CostRow struct {
	Slice         string  `json:"slice"`
	Commits       int     `json:"commits"`
	Errors        int     `json:"errors"`
	MeanReread    float64 `json:"mean_reread_sections"`
	MedianReread  int     `json:"median_reread_sections"`
	P90Reread     int     `json:"p90_reread_sections"`
	ZeroShare     float64 `json:"share_of_changes_flagging_nothing"`
	MeanSections  float64 `json:"mean_sections_in_tree"`
	RereadPercent float64 `json:"mean_percent_of_sections_to_reread"`
}

func (r *ReplayReport) summarize() {
	slices := map[string]func(ReplayRepo, ReplayItem) bool{
		"dev (api-factory)":               func(rr ReplayRepo, _ ReplayItem) bool { return rr.Split == "dev" },
		"test (gitops + infra, held out)": func(rr ReplayRepo, _ ReplayItem) bool { return rr.Split == "test" },
	}
	order := []string{"dev (api-factory)", "test (gitops + infra, held out)"}
	for _, rr := range r.Repos {
		name := rr.Repo
		slices["repo "+name] = func(x ReplayRepo, _ ReplayItem) bool { return x.Repo == name }
		order = append(order, "repo "+name)
	}
	for _, sp := range []string{"dev", "test"} {
		for _, ty := range []string{"wrong", "obsolete", "missing"} {
			key := sp + " / " + ty
			slices[key] = func(x ReplayRepo, it ReplayItem) bool { return x.Split == sp && it.Type == ty }
			order = append(order, key)
		}
	}
	for _, k := range order {
		r.Rows = append(r.Rows, r.row(k, slices[k]))
	}
	r.Cost = append(r.Cost, r.cost("dev", func(x ReplayRepo) bool { return x.Split == "dev" }),
		r.cost("test", func(x ReplayRepo) bool { return x.Split == "test" }))
}

func (r *ReplayReport) row(name string, keep func(ReplayRepo, ReplayItem) bool) ReplayRow {
	row := ReplayRow{Slice: name}
	var causes []int
	for _, rr := range r.Repos {
		for _, it := range rr.Items {
			if !keep(rr, it) {
				continue
			}
			if row.tally(it) {
				causes = append(causes, len(it.Causing))
			}
		}
	}
	row.MedianCause = median(causes)
	return row
}

// tally counts one item; it reports whether the item was scored.
func (row *ReplayRow) tally(it ReplayItem) bool {
	row.Items++
	if it.Status != "scored" {
		row.NoCause++
		return false
	}
	row.Scored++
	if it.First {
		row.First++
	}
	if !it.Any {
		return true
	}
	row.Any++
	if len(it.Reasons) > 0 && it.Reasons[0].Kind == "path" {
		row.ByPath++
	} else {
		row.ByIdent++
	}
	return true
}

func (r *ReplayReport) cost(name string, keep func(ReplayRepo) bool) CostRow {
	c := CostRow{Slice: name}
	var rr []int
	sumSecs, sumPct, zero := 0, 0.0, 0
	for _, repo := range r.Repos {
		if !keep(repo) {
			continue
		}
		for _, cm := range repo.Commits {
			if cm.Err != "" {
				c.Errors++
				continue
			}
			c.Commits++
			rr = append(rr, cm.Reread)
			sumSecs += cm.Sections
			if cm.Sections > 0 {
				sumPct += 100 * float64(cm.Reread) / float64(cm.Sections)
			}
			if cm.Reread+cm.Updated == 0 {
				zero++
			}
		}
	}
	if c.Commits == 0 {
		return c
	}
	sort.Ints(rr)
	total := 0
	for _, v := range rr {
		total += v
	}
	n := float64(c.Commits)
	c.MeanReread, c.MedianReread, c.P90Reread = float64(total)/n, rr[len(rr)/2], rr[min(len(rr)-1, len(rr)*9/10)]
	c.ZeroShare, c.MeanSections, c.RereadPercent = float64(zero)/n, float64(sumSecs)/n, sumPct/n
	return c
}

func median(v []int) int {
	if len(v) == 0 {
		return 0
	}
	sort.Ints(v)
	return v[len(v)/2]
}

func pct(a, b int) string {
	if b == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d (%.0f%%)", a, b, 100*float64(a)/float64(b))
}

// Markdown renders the replay report.
func (r *ReplayReport) Markdown() string {
	var b strings.Builder
	b.WriteString("# History replay: flagged at the moment of the change?\n\n")
	b.WriteString("| slice | items | scored | no causing commit | flagged at first causing commit | flagged at any causing commit | hits via path / identifier | median causing commits |\n|---|---|---|---|---|---|---|---|\n")
	for _, x := range r.Rows {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s | %d / %d | %d |\n", x.Slice, x.Items, x.Scored, x.NoCause,
			pct(x.First, x.Scored), pct(x.Any, x.Scored), x.ByPath, x.ByIdent, x.MedianCause)
	}
	b.WriteString("\nA section counts as flagged only when it is listed as `reread` (a section the change itself edited is not counted). ")
	b.WriteString("Random baseline: flagging the same number of sections at random would hit a stale section with probability ≈ the mean % of sections to re-read below.\n")
	b.WriteString("\n## Re-read load per replayed change\n\n| split | commits | errors | mean sections to re-read | median | p90 | changes flagging nothing | mean sections in tree | mean % of sections to re-read |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cost {
		fmt.Fprintf(&b, "| %s | %d | %d | %.1f | %d | %d | %.0f%% | %.0f | %.1f%% |\n", c.Slice, c.Commits, c.Errors, c.MeanReread,
			c.MedianReread, c.P90Reread, 100*c.ZeroShare, c.MeanSections, c.RereadPercent)
	}
	b.WriteString("\n## Items without an identifiable causing commit\n\n")
	for _, rr := range r.Repos {
		for _, it := range rr.Items {
			if it.Status != "scored" {
				fmt.Fprintf(&b, "- %s %s:%d [%s] %s\n", rr.Repo, it.File, it.Line, it.Type, it.Why)
			}
		}
	}
	b.WriteString("\n## Missed (scored, never flagged)\n\n")
	for _, rr := range r.Repos {
		for _, it := range rr.Items {
			if it.Status == "scored" && !it.Any {
				fmt.Fprintf(&b, "- %s %s:%d [%s] causing %d (replayed %d, line not found in %d)\n", rr.Repo, it.File, it.Line, it.Type, len(it.Causing), it.Replayed, it.LineMissing)
			}
		}
	}
	return b.String()
}

func (r *ReplayReport) write(md, js string) error {
	if md != "" {
		if err := os.WriteFile(md, []byte(r.Markdown()), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", md, err)
		}
	}
	if js == "" {
		return nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := os.WriteFile(js, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", js, err)
	}
	return nil
}
