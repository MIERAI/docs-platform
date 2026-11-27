package outcome

import (
	"encoding/csv"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// Sample draws n of the ledger's open problem claims uniformly (seeded, reproducible),
// leaving out the claims an audit already labelled. Claims labelled only by a PR tick stay in
// the pool: leaving them out would make the sample as self-selected as the ticks.
func Sample(l *ledger.Ledger, labelled map[string]bool, n int, seed uint64) []ledger.Claim {
	var pool []ledger.Claim
	for _, c := range l.Claims {
		if agentic.IsProblem(c.Category) && !labelled[c.ID] {
			pool = append(pool, c)
		}
	}
	// the ledger's order is not part of its contract: sort first, so a seed draws the same claims
	sort.Slice(pool, func(i, j int) bool { return pool[i].ID < pool[j].ID })
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // sampling, not security
	r.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	pool = pool[:min(max(n, 0), len(pool))]
	sort.Slice(pool, func(i, j int) bool {
		return pool[i].Path < pool[j].Path || pool[i].Path == pool[j].Path && pool[i].Line < pool[j].Line
	})
	return pool
}

// Labelled is the set of claims an audit labelled (any label, skip included: a claim the
// auditor could not decide is not asked again).
func Labelled(events []Event) map[string]bool {
	m := map[string]bool{}
	for _, e := range events {
		if e.Kind == KindAudit && e.ClaimID != "" {
			m[e.ClaimID] = true
		}
	}
	return m
}

// LineReader reads line n of a file at a commit.
type LineReader func(ref, path string, n int) (string, bool)

// csvHeader: what a person reads to judge one claim in about a minute, the two columns they
// fill (human_verdict: tp, fp or skip; note), and what the import needs (path, line,
// verified_ref) last, out of the way.
var csvHeader = []string{
	"claim_id", "repo", "doc", "doc_text", "category", "facts", "evidence_1", "evidence_2", "evidence_3", "model",
	"human_verdict", "note", "path", "line", "verified_ref",
}

// WriteCSV writes an audit sheet of the sampled claims. githubRepo (owner/name) turns
// locations into links at the commit the claim was judged; lines reads the cited code lines.
func WriteCSV(w io.Writer, claims []ledger.Claim, githubRepo string, lines LineReader) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(csvHeader); err != nil {
		return fmt.Errorf("audit csv: %w", err)
	}
	for i := range claims {
		if err := cw.Write(row(&claims[i], githubRepo, lines)); err != nil {
			return fmt.Errorf("audit csv: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("audit csv: %w", err)
	}
	return nil
}

func row(c *ledger.Claim, githubRepo string, lines LineReader) []string {
	ref := c.VerifiedRef
	if c.DocRef != "" {
		ref = c.DocRef // a group claim: the document is at the document repository's commit
	}
	var facts []string
	for _, f := range c.Facts {
		if f.Valid {
			facts = append(facts, f.Status+": "+f.Fact)
		}
	}
	ev := make([]string, 3)
	for i, q := range c.Evidence.Quotes[:min(3, len(c.Evidence.Quotes))] {
		link := location(githubRepo, c.VerifiedRef, q.Path, q.Line)
		if q.Repo != "" && q.Repo != c.Repo {
			link = q.Repo + ":" + location("", c.VerifiedRef, q.Path, q.Line) // another repository: no link guessed
		}
		text, _ := lines(c.VerifiedRef, q.Path, q.Line)
		ev[i] = link + "\n" + safe(strings.TrimSpace(text))
	}
	return []string{
		c.ID, c.Repo, location(githubRepo, ref, c.Path, c.Line), safe(strings.TrimSpace(c.Text)), c.Category,
		safe(strings.Join(facts, "\n")), ev[0], ev[1], ev[2], c.Model,
		"", "", c.Path, strconv.Itoa(c.Line), ref,
	}
}

func location(githubRepo, ref, p string, line int) string {
	if githubRepo == "" {
		return fmt.Sprintf("%s:%d@%.12s", p, line, ref)
	}
	return fmt.Sprintf("https://github.com/%s/blob/%s/%s#L%d", githubRepo, ref, p, line)
}

// safe keeps a spreadsheet from evaluating repository text as a formula (CSV injection): a
// cell starting with = + - @ gets a leading apostrophe, which spreadsheets hide; the import
// strips it again.
func safe(s string) string {
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + s
	}
	return s
}

func unsafe(s string) string {
	if len(s) > 1 && s[0] == '\'' && strings.ContainsRune("=+-@", rune(s[1])) {
		return s[1:]
	}
	return s
}

// LabelsFromCSV reads a filled audit sheet: one audit event per row with a human_verdict
// (tp, fp or skip; rows left empty are not labelled yet). Columns are found by name, so a
// person may reorder or add columns.
func LabelsFromCSV(r io.Reader, at time.Time) ([]Event, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	recs, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("audit csv: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i // a spreadsheet may add a BOM
	}
	for _, h := range []string{"claim_id", "repo", "human_verdict", "path", "line"} {
		if _, ok := col[h]; !ok {
			return nil, fmt.Errorf("audit csv: column %q missing", h)
		}
	}
	get := func(rec []string, h string) string {
		if i, ok := col[h]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	ts := at.UTC().Format(time.RFC3339)
	var out []Event
	for n, rec := range recs[1:] {
		v := strings.ToLower(get(rec, "human_verdict"))
		if v == "" {
			continue
		}
		if v != LabelTP && v != LabelFP && v != LabelSkip || get(rec, "claim_id") == "" {
			return nil, fmt.Errorf("audit csv: row %d: human_verdict %q: want tp, fp or skip, and a claim_id", n+2, v)
		}
		line, _ := strconv.Atoi(get(rec, "line"))
		out = append(out, Event{
			SchemaVersion: SchemaVersion, At: ts, Repo: get(rec, "repo"), ClaimID: get(rec, "claim_id"),
			Path: get(rec, "path"), Line: line, Text: unsafe(get(rec, "doc_text")), Category: get(rec, "category"),
			Kind: KindAudit, Label: v, Source: SourceAudit, Ref: get(rec, "verified_ref"), Note: get(rec, "note"),
		})
	}
	return out, nil
}
