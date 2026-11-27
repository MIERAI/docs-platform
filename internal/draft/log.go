package draft

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/ledger"
)

// Statuses of a logged draft.
const (
	Proposed = "proposed"
	// RejectedByHarness: the draft broke the minimal-diff rules and was never proposed.
	RejectedByHarness = "rejected_by_harness"
	Accepted          = "accepted"   // the document now carries the draft (or the line was deleted as proposed)
	Superseded        = "superseded" // the line changed, but not to the draft
	Reverted          = "reverted"   // accepted, then the original line came back
)

// Entry is one draft in the log.
type Entry struct {
	ClaimID     string `json:"claim_id"`
	Repo        string `json:"repo"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Category    string `json:"category"`
	Original    string `json:"original"`
	Draft       string `json:"draft"`
	Action      string `json:"action"`
	DiffTokens  int    `json:"diff_tokens"`
	Model       string `json:"model,omitempty"`
	ProposedAt  string `json:"proposed_at"`
	ProposedRef string `json:"proposed_ref"`
	// DocBlob is the document's blob the draft was made against (the claim's): applying the
	// draft refuses any other blob (docsp draft apply).
	DocBlob   string `json:"doc_blob,omitempty"`
	Status    string `json:"status"`
	StatusAt  string `json:"status_at,omitempty"`
	StatusRef string `json:"status_ref,omitempty"`
	// Problems are the harness's objections (rejected_by_harness).
	Problems []string `json:"problems,omitempty"`
}

// Log is the drafts log (JSON Lines, mode 0600, private data).
type Log struct {
	Entries []Entry
}

// LoadLog reads a drafts log; a missing file is empty.
func LoadLog(p string) (*Log, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Log{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("drafts log: %w", err)
	}
	l := &Log{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for n := 1; sc.Scan(); n++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("drafts log line %d: %w", n, err)
		}
		l.Entries = append(l.Entries, e)
	}
	return l, nil
}

// Save writes the log (mode 0600).
func (l *Log) Save(p string) error {
	var b bytes.Buffer
	for _, e := range l.Entries {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("drafts log: %w", err)
		}
		b.Write(append(line, '\n'))
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		return fmt.Errorf("drafts log: %w", err)
	}
	return nil
}

// Open reports whether a claim has a draft still waiting for a decision.
func (l *Log) Open(claimID string) bool {
	for _, e := range l.Entries {
		if e.ClaimID == claimID && e.Status == Proposed {
			return true
		}
	}
	return false
}

// Track updates the drafts of repoName from the documents at ref: a proposed draft whose
// line now reads as the draft (or is gone, for a delete) is accepted; whose line changed to
// something else is superseded; an accepted draft whose original line came back is reverted.
func (l *Log) Track(ctx context.Context, dir, repoName, ref string, now time.Time) error {
	sha, err := ledger.Resolve(ctx, dir, ref)
	if err != nil {
		return err //nolint:wrapcheck // already descriptive
	}
	docs := map[string][]string{}
	lines := func(p string) []string {
		if ls, ok := docs[p]; ok {
			return ls
		}
		text, _ := ledger.FileAt(ctx, dir, sha, p)
		docs[p] = strings.Split(text, "\n")
		return docs[p]
	}
	for i := range l.Entries {
		e := &l.Entries[i]
		if e.Repo != repoName || (e.Status != Proposed && e.Status != Accepted) {
			continue
		}
		if next := transition(e, lines(e.Path)); next != e.Status {
			e.Status, e.StatusAt, e.StatusRef = next, now.UTC().Format(time.RFC3339), sha
		}
	}
	return nil
}

// transition is a draft's next status given its document's lines.
func transition(e *Entry, ls []string) string {
	hasOrig := contains(ls, e.Original)
	hasDraft := e.Action != ActionDelete && contains(ls, e.Draft)
	switch {
	case e.Status == Accepted && hasOrig:
		return Reverted
	case e.Status == Proposed && (hasDraft || e.Action == ActionDelete && !hasOrig):
		return Accepted
	case e.Status == Proposed && !hasOrig:
		return Superseded
	}
	return e.Status
}

func contains(ls []string, line string) bool {
	w := strings.Join(strings.Fields(line), " ")
	for _, l := range ls {
		if strings.Join(strings.Fields(l), " ") == w {
			return true
		}
	}
	return false
}

// Rates are the draft outcomes.
type Rates struct {
	Proposed          int            `json:"proposed"`
	RejectedByHarness int            `json:"rejected_by_harness"`
	Decided           int            `json:"decided"`
	Accepted          int            `json:"accepted"`
	Reverted          int            `json:"reverted"`
	AcceptanceRate    *float64       `json:"acceptance_rate,omitempty"`
	RevertRate        *float64       `json:"revert_rate,omitempty"`
	MeanDiffTokens    float64        `json:"mean_diff_tokens"`
	ByAction          map[string]int `json:"by_action"`
}

// Stats computes the acceptance rate (accepted, including later reverted, over decided
// drafts), the revert rate (over accepted) and the mean diff size of proposed drafts.
func (l *Log) Stats() Rates {
	r := Rates{ByAction: map[string]int{}}
	diff := 0
	for _, e := range l.Entries {
		if e.Status == RejectedByHarness {
			r.RejectedByHarness++
			continue
		}
		r.Proposed++
		diff += e.DiffTokens
		r.ByAction[e.Action]++
		switch e.Status {
		case Accepted:
			r.Decided++
			r.Accepted++
		case Reverted:
			r.Decided++
			r.Accepted++
			r.Reverted++
		case Superseded:
			r.Decided++
		}
	}
	if r.Proposed > 0 {
		r.MeanDiffTokens = float64(diff) / float64(r.Proposed)
	}
	if r.Decided > 0 {
		v := float64(r.Accepted) / float64(r.Decided)
		r.AcceptanceRate = &v
	}
	if r.Accepted > 0 {
		v := float64(r.Reverted) / float64(r.Accepted)
		r.RevertRate = &v
	}
	return r
}

// Recommendation is advice for a document that keeps going stale.
type Recommendation struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Drafts int    `json:"drafts_90d"`
	Advice string `json:"advice"`
}

// Recommend lists documents with at least `after` drafts in the last 90 days: restating
// facts there costs more than generating the document or linking to the source.
func (l *Log) Recommend(after int, now time.Time) []Recommendation {
	n := map[[2]string]int{}
	for _, e := range l.Entries {
		t, err := time.Parse(time.RFC3339, e.ProposedAt)
		if err == nil && now.Sub(t) <= 90*24*time.Hour && e.Status != RejectedByHarness {
			n[[2]string{e.Repo, e.Path}]++
		}
	}
	var out []Recommendation
	for k, c := range n {
		if c >= after {
			out = append(out, Recommendation{
				Repo: k[0], Path: k[1], Drafts: c,
				Advice: "this document keeps going stale: generate it from the source, or replace restated details with links to the files that hold them",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo+out[i].Path < out[j].Repo+out[j].Path })
	return out
}
