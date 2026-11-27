// Package outcome measures the precision of docs-platform's own problem reports
// (docs-platform/outcome/v1): how often a claim it reports as a problem really is one. Every
// run appends what happened to the reported claims to an append-only JSON Lines stream -
// reported, resolved (and how), re-judged on unchanged inputs, labelled by a person - and
// Report turns the stream into two SLIs with an error budget. No model is called: the labels
// come from a small random audit, the PR comment's tick boxes and the documentation's own
// history.
//
// The stream holds documentation text and paths of the inspected repositories, like the
// ledger: private data, mode 0600, never inside an inspected repository.
package outcome

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// SchemaVersion of docs-platform/outcome/v1.
const SchemaVersion = "1.0"

// Kinds of event.
const (
	// KindReported: the claim became a problem (it was not one, or not in the ledger).
	KindReported = "reported"
	// KindResolved: a reported claim is no longer a problem, or left the ledger.
	KindResolved = "resolved"
	// KindRejudged: judged again on unchanged inputs and the verdict held - the denominator
	// of the flip rate next to the flips.
	KindRejudged = "rejudged"
	// KindHuman: a person ticked a box in the PR comment.
	KindHuman = "human"
	// KindAudit: a person judged a claim of the random audit sample.
	KindAudit = "audit"
)

// How a claim was reported or resolved.
const (
	HowNew = "new" // reported: not in the ledger before
	// HowFlip: the verdict changed on unchanged inputs (same line, every evidence blob
	// unchanged) - one of the two verdicts is wrong.
	HowFlip        = "flip"
	HowCodeChanged = "code_changed" // the evidence changed
	HowStable      = "stable"       // rejudged: the verdict held on unchanged inputs
	HowDocRemoved  = "doc_removed"  // resolved: the document is gone
	HowDocEdited   = "doc_edited"   // resolved: the line was edited after it was reported
	HowDropped     = "dropped"      // resolved: left the ledger while its line is still there
)

// Labels and their sources.
const (
	LabelTP   = "tp"   // the report was right
	LabelFP   = "fp"   // a false positive
	LabelSkip = "skip" // the auditor could not decide

	// SourceAudit is the random sample: the only unbiased source, the SLI is computed from it.
	SourceAudit = "audit"
	// SourcePRTick and SourceProxy are self-selected (only the reports someone acted on):
	// supporting signals, never the SLI.
	SourcePRTick = "pr_tick"
	SourceProxy  = "proxy"
)

// Event is one line of the stream.
type Event struct {
	SchemaVersion string `json:"schema_version"`
	At            string `json:"at"`
	Repo          string `json:"repo"`
	// ClaimID is the ledger's claim id ("" for a PR tick no ledger claim could be matched to).
	ClaimID  string `json:"claim_id,omitempty"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Text     string `json:"text,omitempty"`
	Category string `json:"category,omitempty"`
	Kind     string `json:"kind"`
	How      string `json:"how,omitempty"`
	Label    string `json:"label,omitempty"`
	Source   string `json:"source,omitempty"`
	Ref      string `json:"ref,omitempty"`
	PR       int    `json:"pr,omitempty"`
	Option   string `json:"option,omitempty"`
	Note     string `json:"note,omitempty"`
}

func event(c *ledger.Claim, kind, how, ref, at string) Event {
	return Event{
		SchemaVersion: SchemaVersion, At: at, Repo: c.Repo, ClaimID: c.ID, Path: c.Path, Line: c.Line,
		Text: c.Text, Category: c.Category, Kind: kind, How: how, Ref: ref,
	}
}

// Diff is what one run did to the reported claims: before is the ledger at the start of the
// run, after the ledger once the recheck merged and retired claims, ref the commit judged.
// blobAt and docAt read a file's blob and content at ref. A claim only counts as judged
// again when its verified_at or verified_ref moved.
func Diff(before, after *ledger.Ledger, ref string, at time.Time, blobAt func(path string) (blob string, ok bool), docAt func(path string) (string, bool)) []Event {
	ts := at.UTC().Format(time.RFC3339)
	prev := map[string]*ledger.Claim{}
	for i := range before.Claims {
		prev[before.Claims[i].ID] = &before.Claims[i]
	}
	var out []Event
	seen := map[string]bool{}
	for i := range after.Claims {
		a := &after.Claims[i]
		seen[a.ID] = true
		if e, ok := judged(prev[a.ID], a, ref, ts, blobAt); ok {
			out = append(out, e)
		}
	}
	for i := range before.Claims {
		b := &before.Claims[i]
		if !seen[b.ID] && agentic.IsProblem(b.Category) {
			out = append(out, gone(b, ref, ts, docAt))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return out
}

// judged is the event of a claim present after the run (none when it was not judged again,
// or judged again with changed inputs and the same answer).
func judged(b, a *ledger.Claim, ref, ts string, blobAt func(string) (string, bool)) (Event, bool) {
	isP := agentic.IsProblem(a.Category)
	if b == nil {
		return event(a, KindReported, HowNew, ref, ts), isP
	}
	if a.VerifiedAt == b.VerifiedAt && a.VerifiedRef == b.VerifiedRef {
		return Event{}, false
	}
	wasP, same := agentic.IsProblem(b.Category), unchanged(b, a, blobAt)
	how := HowCodeChanged
	if same {
		how = HowFlip
	}
	switch {
	case isP && !wasP:
		return event(a, KindReported, how, ref, ts), true
	case wasP && !isP:
		e := event(a, KindResolved, how, ref, ts)
		e.Category = b.Category // what was resolved: the reported problem
		return e, true
	case same:
		return event(a, KindRejudged, HowStable, ref, ts), true
	}
	return Event{}, false
}

// unchanged reports whether the inputs of b's verdict are the same at ref: the same line and
// every evidence blob it cites. Evidence this cannot check from blobs alone (an absence
// search's file set, a bare file name, another repository's code) counts as changed: the flip
// rate is then a lower bound, never inflated.
func unchanged(b, a *ledger.Claim, blobAt func(string) (string, bool)) bool {
	if a.Line != b.Line || b.Target != nil {
		return false
	}
	files, absent, ok := inputs(b)
	if !ok {
		return false
	}
	for _, f := range files {
		if got, ok := blobAt(f.Path); !ok || got != f.Blob {
			return false
		}
	}
	for _, p := range absent {
		if _, ok := blobAt(p); ok {
			return false
		}
	}
	return true
}

// inputs are the files a verdict rests on (with their blobs) and the paths that must stay
// absent; ok is false when part of the evidence cannot be checked from blobs alone.
func inputs(c *ledger.Claim) ([]ledger.File, []string, bool) {
	ev := c.Evidence
	if len(ev.Absence) > 0 {
		return nil, nil, false
	}
	files := append([]ledger.File{}, ev.Pinned...)
	for _, q := range ev.Quotes {
		if q.Repo != "" && q.Repo != c.Repo {
			return nil, nil, false
		}
		files = append(files, ledger.File{Path: q.Path, Blob: q.Blob})
	}
	var absent []string
	for _, d := range ev.Drift {
		files = append(files, d.Files...)
		if d.RemovedPath != "" {
			absent = append(absent, d.RemovedPath)
		}
	}
	for _, pm := range ev.PathMissing {
		if pm.Name != "" {
			return nil, nil, false
		}
		absent = append(absent, pm.Candidates...)
	}
	return files, absent, true
}

// gone is the event of a reported claim the run removed from the ledger. A line edited after
// it was reported is the "doc co-changed" proxy of a true positive: someone fixed the
// document - or reworded it for another reason, which is why the proxy never feeds the SLI.
func gone(b *ledger.Claim, ref, ts string, docAt func(string) (string, bool)) Event {
	text, ok := docAt(b.Path)
	switch {
	case !ok:
		return event(b, KindResolved, HowDocRemoved, ref, ts)
	case ledger.FindLine(text, b.Text, b.Line) == 0:
		e := event(b, KindResolved, HowDocEdited, ref, ts)
		e.Label, e.Source = LabelTP, SourceProxy
		return e
	}
	return event(b, KindResolved, HowDropped, ref, ts)
}

// maxLine bounds one line of the stream.
const maxLine = 1 << 20

// Read decodes a stream; blank lines are skipped.
func Read(r io.Reader) ([]Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var out []Event
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("outcome: line %d: %w", n, err)
		}
		if !strings.HasPrefix(e.SchemaVersion, "1.") {
			return nil, fmt.Errorf("outcome: line %d: schema_version %q: this docsp reads 1.x", n, e.SchemaVersion)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("outcome: %w", err)
	}
	return out, nil
}

// Load reads a stream file; a missing file is an empty stream.
func Load(p string) ([]Event, error) {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("outcome: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Read(f)
}

// Append adds events to a stream file (created with mode 0600, parents 0700). Lines are only
// ever appended: a stream is the history, nothing in it is rewritten.
func Append(p string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("outcome: %w", err)
	}
	var b strings.Builder
	for _, e := range events {
		e.SchemaVersion = SchemaVersion
		j, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("outcome: %w", err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("outcome: %w", err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return fmt.Errorf("outcome: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("outcome: %w", err)
	}
	return nil
}
