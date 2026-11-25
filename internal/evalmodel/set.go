// Package evalmodel is `docsp eval-model`: it scores a model provider, per task (judge,
// draft), on a FROZEN eval set and decides whether it meets the switching thresholds of the
// instance configuration. The set is built once (by the history replay benchmark) and never
// edited; its sha256 is recorded in every result so runs stay comparable over time.
//
// Labels matter: positives come from a ground truth that a strong AI produced, so scores
// against them measure AGREEMENT WITH A STRONG-AI AUDIT, not accuracy (a strong model scores
// inflated). Negatives come from history: the same claim against the code as it was when
// the doc line was written, assumed consistent at that moment.
package evalmodel

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/MIERAI/docs-platform/judgev1"
)

// Tasks.
const (
	TaskJudge = "judge"
	TaskDraft = "draft"
)

// Sources and labels.
const (
	SourceGTPositive     = "gt_positive"
	SourceReplayNegative = "replay_negative"
	LabelStrongAI        = "agreement_with_strong_ai_audit"
	LabelAssumedOK       = "assumed_consistent_at_doc_commit"
	// SourceControl: a documentation line near the ground truth's lines that a reviewer
	// verified true at the set's revision (a negative of the agentic TEST set).
	SourceControl   = "verified_control"
	LabelVerifiedOK = "verified_consistent_at_revision"
)

// Item is one frozen eval task.
type Item struct {
	ID       string             `json:"id"`
	Source   string             `json:"source"`
	Label    string             `json:"label"`
	Repo     string             `json:"repo"`
	Split    string             `json:"split"`
	Revision string             `json:"revision"`
	Task     string             `json:"task"`      // judge | draft
	TaskType string             `json:"task_type"` // judge/v1 task type
	Expected string             `json:"expected,omitempty"`
	GTType   string             `json:"gt_type,omitempty"`
	Claim    judgev1.Claim      `json:"claim"`
	Evidence []judgev1.Evidence `json:"evidence"`
	// ReferenceFix and ReferenceActual are the ground truth's fix and description of the
	// code (draft task only; for the similarity proxy, never sent to the model).
	ReferenceFix    string `json:"reference_fix,omitempty"`
	ReferenceActual string `json:"reference_actual,omitempty"`
}

// Set is a loaded eval set.
type Set struct {
	Path   string
	SHA256 string
	Items  []Item
}

// maxSetBytes bounds an eval set file.
const maxSetBytes = 64 << 20

// LoadSet reads a JSON Lines eval set.
func LoadSet(p string) (*Set, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("eval set: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSetBytes+1))
	if err != nil {
		return nil, fmt.Errorf("eval set: %w", err)
	}
	if len(b) > maxSetBytes {
		return nil, fmt.Errorf("eval set %s exceeds %d bytes", p, maxSetBytes)
	}
	items, err := ParseSet(b)
	if err != nil {
		return nil, fmt.Errorf("eval set %s: %w", p, err)
	}
	h := sha256.Sum256(b)
	return &Set{Path: p, SHA256: hex.EncodeToString(h[:]), Items: items}, nil
}

// ParseSet decodes JSON Lines items and checks each one.
func ParseSet(b []byte) ([]Item, error) {
	var out []Item
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	seen := map[string]bool{}
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var it Item
		if err := json.Unmarshal(line, &it); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if err := it.check(); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if seen[it.ID] {
			return nil, fmt.Errorf("line %d: duplicate id %q", n, it.ID)
		}
		seen[it.ID] = true
		out = append(out, it)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return out, nil
}

func (it *Item) check() error {
	switch {
	case it.ID == "" || it.Claim.Text == "" || it.Claim.Path == "":
		return errors.New("id, claim.path and claim.text are required")
	case it.Task == TaskJudge && it.Expected != judgev1.Yes && it.Expected != judgev1.No:
		return fmt.Errorf("item %s: judge items need expected yes or no", it.ID)
	case it.Task != TaskJudge && it.Task != TaskDraft:
		return fmt.Errorf("item %s: task %q: want judge or draft", it.ID, it.Task)
	}
	switch it.TaskType {
	case judgev1.TaskClaimContradiction, judgev1.TaskClaimIncomplete, judgev1.TaskChangeContradiction, judgev1.TaskDraftEdit,
		judgev1.TaskClaimFacts:
		return nil
	}
	return fmt.Errorf("item %s: unknown task_type %q", it.ID, it.TaskType)
}

// WriteSet writes items as JSON Lines (mode 0600) and returns the file's sha256.
func WriteSet(p string, items []Item) (string, error) {
	var b bytes.Buffer
	for _, it := range items {
		line, err := json.Marshal(it)
		if err != nil {
			return "", fmt.Errorf("encode %s: %w", it.ID, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("write eval set: %w", err)
	}
	h := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(h[:]), nil
}
