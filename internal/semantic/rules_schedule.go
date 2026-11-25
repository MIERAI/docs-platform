package semantic

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/semantic/evidence"
)

// tzOffsets are fixed UTC offsets (hours) of unambiguous zone abbreviations.
var tzOffsets = map[string]int{"UTC": 0, "GMT": 0, "Z": 0, "JST": 9, "KST": 9, "PST": -8, "PDT": -7, "EST": -5, "EDT": -4, "CET": 1, "CEST": 2}

func isSchedule(v claim.Value) bool {
	return v.Kind == claim.Cron || v.Kind == claim.Clock || v.Kind == claim.Every
}

// scheduleFiles are the files named by the claim that define (or once defined) a schedule.
func (c *checker) scheduleFiles(cl *claim.Claim) []string {
	var out []string
	for _, p := range c.codePaths(cl.Idents) {
		if len(c.crons(p)) > 0 || len(c.ix.CommentedCron[p]) > 0 || isWorkflow(p) {
			out = append(out, p)
		}
	}
	return out
}

func isWorkflow(p string) bool {
	return strings.Contains(p, ".github/workflows/") || strings.Contains(p, ".gitea/workflows/")
}

// crons are the active cron expressions of a file.
func (c *checker) crons(p string) []evidence.Fact {
	var out []evidence.Fact
	for _, f := range c.ix.Facts {
		if f.Path != p || f.Commented {
			continue
		}
		for _, v := range f.Vals {
			if v.Kind == claim.Cron {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// checkSchedules compares "daily at 09:00 JST", "every 2 minutes" or a cron expression with the
// schedule of the workflow / CronJob the claim names.
func (c *checker) checkSchedules(cl *claim.Claim) {
	var vals []claim.Value
	for _, v := range cl.Values {
		if isSchedule(v) {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return
	}
	for _, p := range c.scheduleFiles(cl) {
		crons := c.crons(p)
		if len(crons) == 0 {
			if len(c.ix.CommentedCron[p]) > 0 {
				c.noSchedule(cl, p, vals[0])
			}
			continue
		}
		for _, v := range vals {
			if !anyCron(crons, v) {
				c.scheduleMismatch(cl, p, v, crons)
			}
		}
	}
}

func (c *checker) noSchedule(cl *claim.Claim, p string, v claim.Value) {
	off := c.ix.CommentedCron[p][0]
	c.add(cl, Issue{
		Rule: RuleScheduleDisabled, Subject: path.Base(p) + " schedule", Confidence: model.ConfMedium,
		Detail:     fmt.Sprintf("the docs say %q, but the schedule in %s is commented out", v.Raw, p),
		Suggestion: "Say how the job is triggered now, or restore the schedule.",
		Quote:      c.quoteLine(p, off.Line), Locations: []Location{{Path: p, Line: off.Line, Role: "code"}},
	})
}

func (c *checker) scheduleMismatch(cl *claim.Claim, p string, v claim.Value, crons []evidence.Fact) {
	conf := model.ConfMedium
	if v.Kind == claim.Clock && v.TZ == "" {
		conf = model.ConfLow // the reader's time zone is not written down
	}
	c.add(cl, Issue{
		Rule: RuleScheduleMismatch, Subject: path.Base(p) + " " + v.Raw, Confidence: conf,
		Detail:     fmt.Sprintf("the docs say %q, %s runs on %q (cron is UTC unless a time zone is set)", v.Raw, p, crons[0].Value),
		Suggestion: fmt.Sprintf("Describe the schedule %q.", crons[0].Value),
		Quote:      c.quoteLine(p, crons[0].Line), Locations: factLocs(crons),
	})
}

func anyCron(crons []evidence.Fact, v claim.Value) bool {
	for _, f := range crons {
		for _, fv := range f.Vals {
			if fv.Kind == claim.Cron && cronMatches(fv.Text, v) {
				return true
			}
		}
	}
	return false
}

// cronMatches reports whether a 5-field cron expression agrees with a documented schedule.
func cronMatches(expr string, v claim.Value) bool {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return true // unknown syntax: no claim either way
	}
	switch v.Kind {
	case claim.Cron:
		return expr == v.Text
	case claim.Every:
		iv, ok := cronInterval(f)
		return !ok || equalNum(iv, v.Num)
	case claim.Clock:
		return clockMatches(f, v)
	case claim.Duration, claim.Size, claim.Percent, claim.Count, claim.Version:
	}
	return true
}

func clockMatches(f []string, v claim.Value) bool {
	mins, okM := fieldValues(f[0], 60)
	hours, okH := fieldValues(f[1], 24)
	if !okM || !okH {
		return true
	}
	at := int(v.Num)
	if off, ok := tzOffsets[v.TZ]; ok {
		at = ((at-off*60)%1440 + 1440) % 1440
	}
	return mins[at%60] && hours[at/60]
}

// fieldValues expands a numeric cron field ("5", "1,2", "0-3", "*/15"); ok is false for "*".
func fieldValues(field string, n int) (map[int]bool, bool) {
	if field == "*" || field == "?" {
		return nil, false
	}
	out := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		rng, step, _ := strings.Cut(part, "/")
		st := 1
		if step != "" {
			st, _ = strconv.Atoi(step)
		}
		lo, hi := 0, n-1
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			lo, _ = strconv.Atoi(a)
			hi = lo
			if isRange {
				hi, _ = strconv.Atoi(b)
			}
		}
		for x := lo; x <= hi && x < n && st > 0; x += st {
			out[x] = true
		}
	}
	return out, len(out) > 0
}

// cronInterval returns the period of simple cron expressions (every N minutes/hours, daily,
// weekly, monthly).
func cronInterval(f []string) (float64, bool) {
	if s, ok := strings.CutPrefix(f[0], "*/"); ok && f[1] == "*" {
		n, err := strconv.Atoi(s)
		return float64(n * 60), err == nil
	}
	if f[0] == "*" && f[1] == "*" {
		return 60, true
	}
	if !isNum(f[0]) {
		return 0, false
	}
	if s, ok := strings.CutPrefix(f[1], "*/"); ok {
		n, err := strconv.Atoi(s)
		return float64(n * 3600), err == nil
	}
	switch {
	case f[1] == "*":
		return 3600, true
	case f[2] == "*" && f[3] == "*" && f[4] == "*":
		return 86400 / float64(max(1, strings.Count(f[1], ",")+1)), true
	case f[2] == "*" && f[3] == "*":
		return 604800 / float64(max(1, strings.Count(f[4], ",")+1)), true
	case isNum(f[2]) && f[3] == "*":
		return 2592000, true
	}
	return 0, false
}

func isNum(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}
