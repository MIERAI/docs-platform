package consistency

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/findings"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/judgev1"
)

// SourceAccuracySample marks the items of an accuracy sample.
const SourceAccuracySample = "accuracy_sample"

// Sample draws n claims of repoName uniformly (seeded, reproducible) whose line still
// exists at ref, as an eval set of claim_facts items expecting "no" (the documentation
// holds). The operator re-audits it with `eval-model -mode agentic` (a paid strong model,
// harness-validated) and feeds the result to the SLO report: the share that still holds is
// the documentation's record accuracy (SLI 1).
func Sample(ctx context.Context, l *ledger.Ledger, dir, repoName, ref string, n int, seed uint64, now time.Time) ([]evalmodel.Item, error) {
	rep, err := ledger.Check(ctx, l, dir, repoName, ref, now)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	byID := map[string]*ledger.Claim{}
	for i := range l.Claims {
		byID[l.Claims[i].ID] = &l.Claims[i]
	}
	var pool []ledger.ClaimStatus
	for _, s := range rep.Claims {
		if !hasDocReason(s) {
			pool = append(pool, s)
		}
	}
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // sampling, not security
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	pool = pool[:min(n, len(pool))]
	sort.Slice(pool, func(i, j int) bool {
		return pool[i].Path < pool[j].Path || pool[i].Path == pool[j].Path && pool[i].Line < pool[j].Line
	})
	items := make([]evalmodel.Item, 0, len(pool))
	for _, s := range pool {
		line := s.Line
		if s.NewLine > 0 {
			line = s.NewLine
		}
		c := byID[s.ID]
		items = append(items, evalmodel.Item{
			ID: SampleID(repoName, s.Path, line), Source: SourceAccuracySample, Label: "record_accuracy_sample", Repo: repoName,
			Split: "sample", Revision: rep.Ref, Task: evalmodel.TaskJudge, TaskType: judgev1.TaskClaimFacts, Expected: judgev1.No,
			Claim: judgev1.Claim{Repo: repoName, Path: s.Path, Line: line, Text: c.Text}, Evidence: []judgev1.Evidence{},
		})
	}
	return items, nil
}

func hasDocReason(s ledger.ClaimStatus) bool {
	for _, r := range s.Reasons {
		if r.Kind == ledger.ReasonDocLineChanged || r.Kind == ledger.ReasonDocRemoved {
			return true
		}
	}
	return false
}

// SampleID identifies a sampled line; the document path is recoverable from it (docOf).
func SampleID(repoName, p string, line int) string {
	return "sample:" + repoName + ":" + p + ":" + strconv.Itoa(line)
}

// SLI is one indicator against its objective.
type SLI struct {
	Value  *float64 `json:"value,omitempty"`
	Target float64  `json:"target"`
	// N is the denominator; Bad the count against the objective.
	N   int  `json:"n"`
	Bad int  `json:"bad"`
	Met bool `json:"met"`
	// Low and High bound Value (Wilson 95%) when it is estimated from a sample.
	Low  *float64 `json:"low,omitempty"`
	High *float64 `json:"high,omitempty"`
	Note string   `json:"note,omitempty"`
}

// Budget is the accuracy error budget: the share of sampled claims allowed to be wrong.
type Budget struct {
	Allowed   float64 `json:"allowed"`
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
	Burned    bool    `json:"burned"`
}

// DocAccuracy is the sampled accuracy of one documentation set (a document).
type DocAccuracy struct {
	Path    string `json:"path"`
	Sampled int    `json:"sampled"`
	Wrong   int    `json:"wrong"`
	Burned  bool   `json:"burned"`
}

// Action is a decision the error budget triggers.
type Action struct {
	Path       string `json:"path"`
	Action     string `json:"action"`
	MaxAgeDays int    `json:"max_age_days,omitempty"`
	Why        string `json:"why"`
}

// SLOReport is docs-platform/slo/v1.
type SLOReport struct {
	SchemaVersion string        `json:"schema_version"`
	Repo          string        `json:"repo"`
	Ref           string        `json:"ref"`
	ReportedAt    string        `json:"reported_at"`
	Accuracy      SLI           `json:"accuracy"`
	Freshness     SLI           `json:"freshness"`
	Phantom       SLI           `json:"phantom_rate"`
	Budget        *Budget       `json:"error_budget,omitempty"`
	ByDoc         []DocAccuracy `json:"by_doc,omitempty"`
	Actions       []Action      `json:"actions,omitempty"`
	Cost          Cost          `json:"cost"`
}

// Cost is what measuring and keeping the SLO cost.
type Cost struct {
	SampleUSD      float64 `json:"sample_usd"`
	SampleBilling  string  `json:"sample_billing,omitempty"`
	ScheduledUSD   float64 `json:"scheduled_usd_estimate"`
	ScheduledCount int     `json:"scheduled_claims"`
}

// SLOInput is what the report is computed from.
type SLOInput struct {
	Repo     string
	Ref      string
	Policy   instance.Consistency
	Schedule *Schedule
	// Sample is the eval/v1 result of an accuracy sample (nil = not measured).
	Sample *evalmodel.Result
	// Check is the reference check of the repository (phantom rate), Ledger its claims.
	Check  *findings.Document
	Ledger *ledger.Ledger
	Now    time.Time
}

// Report computes the three SLIs, the error budget and the actions it triggers.
func Report(in SLOInput) *SLOReport {
	p := in.Policy.WithDefaults()
	r := &SLOReport{SchemaVersion: SchemaVersion, Repo: in.Repo, Ref: in.Ref, ReportedAt: in.Now.UTC().Format(time.RFC3339)}
	r.Accuracy = SLI{Target: p.SLO.Accuracy, Note: "no accuracy sample: run `docsp sample`, re-audit it with eval-model -mode agentic, pass the result with -sample-result"}
	if in.Sample != nil {
		accuracy(r, in.Sample, p)
	}
	r.Freshness = SLI{Target: p.SLO.Freshness}
	if s := in.Schedule; s != nil && s.Freshness != nil {
		r.Freshness.Value, r.Freshness.N = s.Freshness, s.Counts.Claims
		r.Freshness.Bad = s.Counts.Claims - int(math.Round(*s.Freshness*float64(s.Counts.Claims)))
		r.Freshness.Met = *s.Freshness >= p.SLO.Freshness
		r.Cost.ScheduledUSD, r.Cost.ScheduledCount = s.EstimatedUSD, s.Counts.Picked
	}
	r.Phantom = phantom(in, p)
	return r
}

func accuracy(r *SLOReport, res *evalmodel.Result, p instance.Consistency) {
	by, n, wrong, unknown := sampled(res, r.Repo)
	r.Accuracy.N, r.Accuracy.Bad = n, wrong
	r.Accuracy.Note = fmt.Sprintf("%d sampled claims re-audited, %d undecided (excluded)", n+unknown, unknown)
	r.Cost.SampleUSD, r.Cost.SampleBilling = res.Scores.CostUSD, res.Provider
	if n == 0 {
		return
	}
	v := float64(n-wrong) / float64(n)
	lo, hi := wilson(n-wrong, n)
	r.Accuracy.Value, r.Accuracy.Low, r.Accuracy.High, r.Accuracy.Met = &v, &lo, &hi, v >= p.SLO.Accuracy
	allowed := 1 - p.SLO.Accuracy
	b := &Budget{Allowed: allowed, Used: 1 - v}
	b.Remaining, b.Burned = (allowed-b.Used)/allowed, b.Used > allowed
	r.Budget = b
	for _, d := range by {
		d.Burned = float64(d.Wrong)/float64(d.Sampled) > allowed
		r.ByDoc = append(r.ByDoc, *d)
		if d.Burned {
			r.Actions = append(r.Actions, Action{
				Path: d.Path, Action: "full_reaudit", MaxAgeDays: max(1, p.MaxAgeDays/2),
				Why: fmt.Sprintf("%d of %d sampled claims wrong: more than the %.0f%% error budget", d.Wrong, d.Sampled, 100*allowed),
			})
		}
	}
	sort.Slice(r.ByDoc, func(i, j int) bool { return r.ByDoc[i].Path < r.ByDoc[j].Path })
	sort.Slice(r.Actions, func(i, j int) bool { return r.Actions[i].Path < r.Actions[j].Path })
}

// sampled aggregates a sample's re-audit per document: decided claims, wrong ones (the
// re-audit found a problem), undecided ones.
func sampled(res *evalmodel.Result, repoName string) (map[string]*DocAccuracy, int, int, int) {
	by := map[string]*DocAccuracy{}
	n, wrong, unknown := 0, 0, 0
	for _, it := range res.Items {
		if it.Source != SourceAccuracySample || !strings.HasPrefix(it.ID, "sample:"+repoName+":") {
			continue // a sample may cover several repositories
		}
		switch it.Verdict {
		case judgev1.Yes:
			wrong++
		case judgev1.No:
		default:
			unknown++
			continue
		}
		n++
		d := by[docOf(it.ID)]
		if d == nil {
			d = &DocAccuracy{Path: docOf(it.ID)}
			by[d.Path] = d
		}
		d.Sampled++
		if it.Verdict == judgev1.Yes {
			d.Wrong++
		}
	}
	return by, n, wrong, unknown
}

// docOf recovers the document of a sample item id; claims are keyed by the ledger path in
// the actions, so the id keeps it.
func docOf(id string) string {
	f := strings.SplitN(id, ":", 3)
	if len(f) == 3 {
		if i := strings.LastIndex(f[2], ":"); i > 0 {
			return f[2][:i]
		}
	}
	return id
}

// wilson is the 95% Wilson score interval of k successes in n.
func wilson(k, n int) (float64, float64) {
	const z = 1.96
	p, nf := float64(k)/float64(n), float64(n)
	d := 1 + z*z/nf
	c := (p + z*z/(2*nf)) / d
	h := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / d
	return math.Max(0, c-h), math.Min(1, c+h)
}

// phantom is SLI 3: dangling references the reference check reports at medium confidence or
// better, plus open dangling_reference claims, over the checkable references and claims.
func phantom(in SLOInput, p instance.Consistency) SLI {
	s := SLI{Target: p.SLO.PhantomRate, Note: "unresolved references (medium confidence or better) + open dangling_reference claims"}
	if in.Check == nil {
		s.Note = "reference check not run"
		return s
	}
	checkable := 0
	for _, m := range in.Check.Metrics {
		if m.Name == "docs_references" && (m.Labels["status"] == "resolved" || m.Labels["status"] == "unresolved") {
			checkable += int(m.Value)
		}
	}
	bad := 0
	for _, f := range in.Check.Findings {
		if strings.HasPrefix(f.Check, "docs.reference.unresolved.") && f.Severity != "info" {
			bad++
		}
	}
	n := checkable
	if in.Ledger != nil {
		for _, c := range in.Ledger.Claims {
			if c.Repo != in.Repo {
				continue
			}
			n++
			if c.Category == "dangling_reference" {
				bad++
			}
		}
	}
	s.N, s.Bad = n, bad
	if n > 0 {
		v := float64(bad) / float64(n)
		s.Value, s.Met = &v, v <= p.SLO.PhantomRate
	}
	return s
}

// Apply records the report's actions in the state: a full re-audit and a shorter max age
// for each document that burned its budget.
func Apply(st *State, r *SLOReport, now time.Time) {
	for _, a := range r.Actions {
		st.Docs[a.Path] = DocState{MaxAgeDays: a.MaxAgeDays, FullReaudit: true, Since: now.UTC().Format(time.RFC3339), Why: a.Why}
	}
}

// Reaudited clears the full re-audit of documents whose claims were all judged again.
func Reaudited(st *State, docs []string) {
	for _, d := range docs {
		if s, ok := st.Docs[d]; ok {
			s.FullReaudit = false
			st.Docs[d] = s
		}
	}
}
