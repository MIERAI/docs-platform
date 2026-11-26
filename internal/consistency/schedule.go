// Package consistency keeps documentation consistent over time (docs/design.md §12): the
// cycle-count scheduler picks which claims of the evidence ledger to judge again in a run
// (stale first, then due by age or churn, then enough of the oldest that every claim is
// verified within its class's max age), within a budget; the accuracy sample and the SLO
// report measure the result. Nothing here calls a model.
package consistency

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// SchemaVersion of docs-platform/schedule/v1 and docs-platform/slo/v1.
const SchemaVersion = "1.0"

// Priorities of a pick (lower first).
const (
	PriorityStale   = 0 // evidence changed
	PriorityDue     = 1 // older than its max age, churned, or its doc set burned its budget
	PriorityRecheck = 2 // no evidence and older than its max age
	PriorityCycle   = 3 // cycle count: the oldest fresh claims, to keep pace with the period
)

// ClassC is the class of claims no configured class matches.
const ClassC = "C"

// Input of one plan.
type Input struct {
	Ledger *ledger.Ledger
	Repo   string
	Dir    string
	Ref    string
	Policy instance.Consistency
	State  *State
	Now    time.Time
}

// Pick is one claim chosen for re-judgment.
type Pick struct {
	ID           string  `json:"id"`
	Path         string  `json:"path"`
	Line         int     `json:"line"`
	Class        string  `json:"class"`
	Priority     int     `json:"priority"`
	Reason       string  `json:"reason"`
	AgeDays      float64 `json:"age_days"`
	MaxAgeDays   int     `json:"max_age_days"`
	ChurnCommits int     `json:"churn_commits,omitempty"`
	ChurnLines   int     `json:"churn_lines,omitempty"`
	Required     bool    `json:"required"`
}

// Counts summarise a plan.
type Counts struct {
	Claims    int `json:"claims"`
	Stale     int `json:"stale"`
	Due       int `json:"due"`
	Recheck   int `json:"recheck"`
	Quota     int `json:"cycle_quota"`
	Picked    int `json:"picked"`
	Shortfall int `json:"shortfall"`
}

// Schedule is docs-platform/schedule/v1.
type Schedule struct {
	SchemaVersion string  `json:"schema_version"`
	Repo          string  `json:"repo"`
	Ref           string  `json:"ref"`
	PlannedAt     string  `json:"planned_at"`
	Counts        Counts  `json:"counts"`
	EstimatedUSD  float64 `json:"estimated_usd"`
	// Freshness is the share of claims verified within their max age and not stale (SLI 2).
	Freshness *float64 `json:"freshness,omitempty"`
	Picked    []Pick   `json:"picked"`
	// Shortfall lists required claims the budget left out (freshness will drop).
	Shortfall []Pick `json:"shortfall,omitempty"`
}

// candidate is a claim with everything the plan needs.
type candidate struct {
	Pick
	status    string
	fresh     bool
	pressure  float64 // age / max age
	classRank int
}

// Plan picks the claims to judge again in this run.
func Plan(ctx context.Context, in Input) (*Schedule, error) {
	p := in.Policy.WithDefaults()
	rep, err := ledger.Check(ctx, in.Ledger, in.Dir, in.Repo, in.Ref, in.Now)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	byID := map[string]*ledger.Claim{}
	for i := range in.Ledger.Claims {
		byID[in.Ledger.Claims[i].ID] = &in.Ledger.Claims[i]
	}
	cl := newClassifier(ctx, in.Dir, rep.Ref, p, in.Now)
	var cands []candidate
	for _, s := range rep.Claims {
		cands = append(cands, assess(ctx, in, p, cl, byID[s.ID], s))
	}
	return choose(rep.Repo, rep.Ref, in.Now, p, cands), nil
}

// assess classifies one claim and decides whether (and why) it is due.
func assess(ctx context.Context, in Input, p instance.Consistency, cl *classifier, c *ledger.Claim, s ledger.ClaimStatus) candidate {
	k, rank, maxAge := cl.class(c)
	if o, ok := in.State.override(c.Path); ok && o.MaxAgeDays > 0 && o.MaxAgeDays < maxAge {
		maxAge = o.MaxAgeDays
	}
	age := ageDays(c.VerifiedAt, in.Now)
	cd := candidate{Pick: Pick{ID: c.ID, Path: c.Path, Line: c.Line, Class: k, AgeDays: round1(age), MaxAgeDays: maxAge, Priority: PriorityCycle},
		status: s.Status, classRank: rank, pressure: age / float64(maxAge)}
	if s.NewLine > 0 {
		cd.Line = s.NewLine
	}
	cd.Priority, cd.Reason = dueness(in, s, c, age, maxAge)
	if cd.Priority == PriorityCycle && s.Status == ledger.Fresh && (p.ChurnCommits > 0 || p.ChurnLines > 0) {
		cd.ChurnCommits, cd.ChurnLines = churn(ctx, in.Dir, c.VerifiedRef, in.Ref, footprint(c))
		if p.ChurnCommits > 0 && cd.ChurnCommits >= p.ChurnCommits || p.ChurnLines > 0 && cd.ChurnLines >= p.ChurnLines {
			cd.Priority, cd.Reason = PriorityDue, fmt.Sprintf("churn: %d commits, %d lines in its evidence scope", cd.ChurnCommits, cd.ChurnLines)
		}
	}
	cd.fresh = s.Status != ledger.Stale && age <= float64(maxAge)
	cd.Required = cd.Priority < PriorityCycle
	return cd
}

// dueness is a claim's priority and reason from its status, state and age (churn aside).
func dueness(in Input, s ledger.ClaimStatus, c *ledger.Claim, age float64, maxAge int) (int, string) {
	switch {
	case s.Status == ledger.Stale:
		return PriorityStale, "stale: " + reasonKinds(s.Reasons)
	case in.State.fullReaudit(c.Path):
		return PriorityDue, "error budget burned: full re-audit"
	case age > float64(maxAge) && s.Status == ledger.Recheck:
		return PriorityRecheck, fmt.Sprintf("no evidence, %.0f days old", age)
	case age > float64(maxAge):
		return PriorityDue, fmt.Sprintf("%.0f days old (max %d)", age, maxAge)
	}
	return PriorityCycle, ""
}

// tally counts the candidates and splits them into required ones and cycle-count ones;
// need is how many claims a run must verify so every claim keeps within its max age.
func tally(s *Schedule, p instance.Consistency, cands []candidate) ([]candidate, []candidate, float64) {
	freshN, need := 0, 0.0
	var required, cycle []candidate
	for _, c := range cands {
		need += float64(p.RunIntervalDays) / float64(c.MaxAgeDays)
		if c.fresh {
			freshN++
		}
		switch c.Priority {
		case PriorityStale:
			s.Counts.Stale++
		case PriorityDue:
			s.Counts.Due++
		case PriorityRecheck:
			s.Counts.Recheck++
		}
		if c.Required {
			required = append(required, c)
		} else {
			cycle = append(cycle, c)
		}
	}
	if len(cands) > 0 {
		f := float64(freshN) / float64(len(cands))
		s.Freshness = &f
	}
	return required, cycle, need
}

// choose orders the candidates, adds the cycle quota and applies the budget.
func choose(repoName, ref string, now time.Time, p instance.Consistency, cands []candidate) *Schedule {
	s := &Schedule{SchemaVersion: SchemaVersion, Repo: repoName, Ref: ref, PlannedAt: now.UTC().Format(time.RFC3339), Picked: []Pick{}}
	s.Counts.Claims = len(cands)
	required, cycle, need := tally(s, p, cands)
	sort.SliceStable(required, func(i, j int) bool { return less(required[i], required[j]) })
	sort.SliceStable(cycle, func(i, j int) bool { return cycle[i].pressure > cycle[j].pressure })
	s.Counts.Quota = min(len(cycle), max(0, int(math.Ceil(need))-len(required)))
	for i := range cycle[:s.Counts.Quota] {
		cycle[i].Required, cycle[i].Reason = true, fmt.Sprintf("cycle count: %.0f of %d days", cycle[i].AgeDays, cycle[i].MaxAgeDays)
	}
	all := append(required, cycle[:s.Counts.Quota]...)
	limit := len(all)
	if p.Budget.MaxClaims > 0 {
		limit = min(limit, p.Budget.MaxClaims)
	}
	if p.Budget.MaxUSD > 0 {
		limit = min(limit, int(p.Budget.MaxUSD/p.Budget.USDPerClaim))
	}
	for i, c := range all {
		if i < limit {
			s.Picked = append(s.Picked, c.Pick)
		} else {
			s.Shortfall = append(s.Shortfall, c.Pick)
		}
	}
	s.Counts.Picked, s.Counts.Shortfall = len(s.Picked), len(s.Shortfall)
	s.EstimatedUSD = float64(len(s.Picked)) * p.Budget.USDPerClaim
	return s
}

func less(a, b candidate) bool {
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.classRank != b.classRank {
		return a.classRank < b.classRank
	}
	return a.pressure > b.pressure
}

func ageDays(verifiedAt string, now time.Time) float64 {
	t, err := time.Parse(time.RFC3339, verifiedAt)
	if err != nil {
		return math.Inf(1) // unknown: treat as overdue
	}
	return now.Sub(t).Hours() / 24
}

func round1(x float64) float64 {
	if math.IsInf(x, 0) {
		return 1e6
	}
	return math.Round(x*10) / 10
}

func reasonKinds(rs []ledger.Reason) string {
	ks := make([]string, 0, len(rs))
	for _, r := range rs {
		ks = append(ks, r.Kind)
	}
	return strings.Join(ks, ", ")
}

// footprint is the document and every evidence path of a claim.
func footprint(c *ledger.Claim) []string {
	set := map[string]bool{c.Path: true}
	for _, q := range c.Evidence.Quotes {
		set[q.Path] = true
	}
	for _, d := range c.Evidence.Drift {
		for _, f := range d.Files {
			set[f.Path] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// churn counts commits and changed lines in paths between two revisions.
func churn(ctx context.Context, dir, from, to string, paths []string) (int, int) {
	if !repo.ValidRef(from) || !repo.ValidRef(to) {
		return 0, 0
	}
	log, err := repo.Git(ctx, dir, append([]string{"log", "--format=%H", from + ".." + to, "--"}, paths...)...)
	if err != nil {
		return 0, 0
	}
	commits := len(strings.Fields(log))
	num, _ := repo.Git(ctx, dir, append([]string{"diff", "--numstat", from, to, "--"}, paths...)...)
	lines := 0
	for _, l := range strings.Split(num, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			a, _ := strconv.Atoi(f[0])
			d, _ := strconv.Atoi(f[1])
			lines += a + d
		}
	}
	return commits, lines
}

// classifier assigns ABC classes: the first configured class whose document globs match, or
// whose 90-day churn threshold the claim's footprint reaches; otherwise C.
type classifier struct {
	ctx      context.Context
	dir, ref string
	p        instance.Consistency
	since    string
	cache    map[string]int
}

func newClassifier(ctx context.Context, dir, ref string, p instance.Consistency, now time.Time) *classifier {
	return &classifier{ctx: ctx, dir: dir, ref: ref, p: p, since: now.Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339), cache: map[string]int{}}
}

func (k *classifier) class(c *ledger.Claim) (string, int, int) {
	for i, cl := range k.p.Classes {
		if repo.MatchAny(cl.Docs, c.Path) {
			return cl.Name, i, cl.MaxAgeDays
		}
		if cl.MinCommits90d > 0 && k.commits90(footprint(c)) >= cl.MinCommits90d {
			return cl.Name, i, cl.MaxAgeDays
		}
	}
	return ClassC, len(k.p.Classes), k.p.MaxAgeDays
}

func (k *classifier) commits90(paths []string) int {
	key := strings.Join(paths, "\x00")
	if n, ok := k.cache[key]; ok {
		return n
	}
	out, err := repo.Git(k.ctx, k.dir, append([]string{"log", "--format=%H", "--since=" + k.since, k.ref, "--"}, paths...)...)
	n := 0
	if err == nil {
		n = len(strings.Fields(out))
	}
	k.cache[key] = n
	return n
}

// State carries the SLO's decisions into the scheduler: per document, a shorter max age
// and a pending full re-audit (written by `docsp slo -apply`).
type State struct {
	Docs map[string]DocState `json:"docs"`
	// Report remembers what `docsp report` already reported, and when the last batch went out.
	Report ReportState `json:"report"`
}

// DocState is the state of one documentation set (a document path).
type DocState struct {
	MaxAgeDays  int    `json:"max_age_days,omitempty"`
	FullReaudit bool   `json:"full_reaudit,omitempty"`
	Since       string `json:"since,omitempty"`
	Why         string `json:"why,omitempty"`
}

func (s *State) override(p string) (DocState, bool) {
	if s == nil {
		return DocState{}, false
	}
	d, ok := s.Docs[p]
	return d, ok
}

func (s *State) fullReaudit(p string) bool {
	d, ok := s.override(p)
	return ok && d.FullReaudit
}

// LoadState reads a state file; a missing file is an empty state.
func LoadState(p string) (*State, error) {
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &State{Docs: map[string]DocState{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consistency state: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("consistency state: %w", err)
	}
	if s.Docs == nil {
		s.Docs = map[string]DocState{}
	}
	return &s, nil
}

// Save writes the state (mode 0600).
func (s *State) Save(p string) error {
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return fmt.Errorf("consistency state: %w", err)
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("consistency state: %w", err)
	}
	return nil
}
