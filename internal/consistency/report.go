package consistency

import (
	"sort"
	"time"

	"github.com/MIERAI/docs-platform/internal/draft"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Severities of a reported finding.
const (
	// Immediate: contradicted and dangling_reference - the document says something false or
	// points at nothing.
	Immediate = "immediate"
	// Batched: outdated_wording and incomplete_list - true-ish but aged; collected and
	// reported together every batch_days.
	Batched = "batched"
)

// severity of a claim category ("" = not a finding).
func severity(cat string) string {
	switch cat {
	case "contradicted", "dangling_reference":
		return Immediate
	case "outdated_wording", "incomplete_list":
		return Batched
	}
	return ""
}

// Finding is one reported claim.
type Finding struct {
	ID         string         `json:"id"`
	Repo       string         `json:"repo"`
	Path       string         `json:"path"`
	Line       int            `json:"line"`
	Text       string         `json:"text"`
	Category   string         `json:"category"`
	Severity   string         `json:"severity"`
	Target     *ledger.Target `json:"target,omitempty"`
	VerifiedAt string         `json:"verified_at"`
	Facts      []string       `json:"facts,omitempty"`
}

// Suppressed is a finding a suppression silences.
type Suppressed struct {
	Finding
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
}

// ConsistencyReport is docs-platform/report/v1: the validated problems of the ledger, for
// people. It never fails a build: nobody is forced to update documentation; contradicted and
// dangling references are reported at once, aged wording and incomplete lists in batches.
type ConsistencyReport struct { //nolint:revive // Report is the SLO report builder's name
	SchemaVersion string       `json:"schema_version"`
	ReportedAt    string       `json:"reported_at"`
	Immediate     []Finding    `json:"immediate"`
	Batched       []Finding    `json:"batched"`
	BatchDue      bool         `json:"batch_due"`
	NextBatch     string       `json:"next_batch,omitempty"`
	Held          int          `json:"held_for_next_batch"`
	Suppressed    []Suppressed `json:"suppressed,omitempty"`
	// ExpiredSuppressions no longer silence anything (their findings are reported again).
	ExpiredSuppressions []instance.Suppression `json:"expired_suppressions,omitempty"`
	Recommendations     []draft.Recommendation `json:"recommendations,omitempty"`
}

// ReportState remembers what was reported, so a finding is reported once (until its
// category changes) and batches are spaced.
type ReportState struct {
	Reported  map[string]string `json:"reported,omitempty"` // claim id -> category
	LastBatch string            `json:"last_batch,omitempty"`
}

// BuildReport selects the findings to report now. Findings already reported with the same
// category are not repeated; batched findings are held until the batch is due (flush
// forces it). The state is updated with what is reported.
func BuildReport(l *ledger.Ledger, p instance.Consistency, st *ReportState, now time.Time, flush bool) *ConsistencyReport {
	p = p.WithDefaults()
	if st.Reported == nil {
		st.Reported = map[string]string{}
	}
	r := &ConsistencyReport{SchemaVersion: SchemaVersion, ReportedAt: now.UTC().Format(time.RFC3339), Immediate: []Finding{}, Batched: []Finding{}}
	r.BatchDue = flush || batchDue(st.LastBatch, now, p.BatchDays)
	active := activeSuppressions(p.Suppressions, now, r)
	var held []Finding
	for i := range l.Claims {
		c := &l.Claims[i]
		sev := severity(c.Category)
		if sev == "" || st.Reported[c.ID] == c.Category {
			continue
		}
		f := finding(c, sev)
		if s, ok := suppressedBy(active, c); ok {
			r.Suppressed = append(r.Suppressed, Suppressed{Finding: f, Reason: s.Reason, Expires: s.Expires})
			continue
		}
		switch {
		case sev == Immediate:
			r.Immediate = append(r.Immediate, f)
		case r.BatchDue:
			r.Batched = append(r.Batched, f)
		default:
			held = append(held, f)
		}
	}
	for _, f := range append(append([]Finding{}, r.Immediate...), r.Batched...) {
		st.Reported[f.ID] = f.Category
	}
	if r.BatchDue {
		st.LastBatch = r.ReportedAt
	}
	r.Held = len(held)
	if !r.BatchDue {
		r.NextBatch = nextBatch(st.LastBatch, p.BatchDays)
	}
	sortFindings(r.Immediate)
	sortFindings(r.Batched)
	return r
}

func finding(c *ledger.Claim, sev string) Finding {
	f := Finding{ID: c.ID, Repo: c.Repo, Path: c.Path, Line: c.Line, Text: c.Text, Category: c.Category, Severity: sev, Target: c.Target, VerifiedAt: c.VerifiedAt}
	for _, x := range c.Facts {
		if x.Status == c.Category && x.Valid {
			f.Facts = append(f.Facts, x.Fact)
		}
	}
	return f
}

func batchDue(last string, now time.Time, days int) bool {
	t, err := time.Parse(time.RFC3339, last)
	return err != nil || now.Sub(t) >= time.Duration(days)*24*time.Hour
}

func nextBatch(last string, days int) string {
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return ""
	}
	return t.Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
}

// activeSuppressions keeps the unexpired suppressions and lists the expired ones.
func activeSuppressions(ss []instance.Suppression, now time.Time, r *ConsistencyReport) []instance.Suppression {
	var out []instance.Suppression
	for _, s := range ss {
		exp, err := time.Parse(time.DateOnly, s.Expires)
		if err != nil || !now.Before(exp.Add(24*time.Hour)) {
			r.ExpiredSuppressions = append(r.ExpiredSuppressions, s)
			continue
		}
		out = append(out, s)
	}
	return out
}

func suppressedBy(ss []instance.Suppression, c *ledger.Claim) (instance.Suppression, bool) {
	for _, s := range ss {
		if s.Claim == c.ID || s.Path != "" && repo.Match(s.Path, c.Path) {
			return s, true
		}
	}
	return instance.Suppression{}, false
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Repo+fs[i].Path != fs[j].Repo+fs[j].Path {
			return fs[i].Repo+fs[i].Path < fs[j].Repo+fs[j].Path
		}
		return fs[i].Line < fs[j].Line
	})
}
