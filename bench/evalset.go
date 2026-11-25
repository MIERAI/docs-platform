package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/MIERAI/docs-platform/internal/evalmodel"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// EvalItem is one frozen model-eval task.
type EvalItem = evalmodel.Item

// Evidence limits (same order as the semantic audit's judge protocol).
const (
	evMaxSnippets = 3
	evMaxBytes    = 2400
	evRadius      = 12
)

// evalItemsFor turns one ground-truth item into frozen eval tasks:
//   - judge positive: the stale doc line vs the evidence at the pinned revision (expected yes;
//     labelled agreement with the strong-AI audit);
//   - judge negative: the same line vs the evidence at the commit that wrote it (expected no;
//     assumed consistent when written);
//   - draft: the positive's claim and evidence, with the ground truth's fix as the reference.
func evalItemsFor(ctx context.Context, dir, repoName, split string, i int, it GTItem, t itemTrace) []EvalItem {
	if t.lineText == "" || strings.TrimSpace(t.lineText) == "" {
		return nil
	}
	taskType := judgev1.TaskClaimContradiction
	if it.Type == "missing" {
		taskType = judgev1.TaskClaimIncomplete
	}
	cl := judgev1.Claim{Repo: repoName, Path: it.File, Line: *it.Line, Text: strings.TrimSpace(t.lineText)}
	query := t.lineText + "\n" + it.Claim
	base := fmt.Sprintf("%s-%03d", repoName, i)
	var out []EvalItem
	if ev := evidenceAt(ctx, dir, "HEAD", repoName, it, query); len(ev) > 0 {
		pos := EvalItem{
			ID: base + "-pos", Source: evalmodel.SourceGTPositive, Label: evalmodel.LabelStrongAI, Repo: repoName,
			Split: split, Revision: "HEAD", Task: evalmodel.TaskJudge, TaskType: taskType, Expected: judgev1.Yes,
			GTType: it.Type, Claim: cl, Evidence: ev,
		}
		out = append(out, pos)
		dr := pos
		dr.ID, dr.Task, dr.TaskType, dr.Expected = base+"-draft", evalmodel.TaskDraft, judgev1.TaskDraftEdit, ""
		dr.ReferenceFix, dr.ReferenceActual = it.Fix, it.Actual
		out = append(out, dr)
	}
	if ev := evidenceAt(ctx, dir, t.docCommit, repoName, it, query); len(ev) > 0 {
		out = append(out, EvalItem{
			ID: base + "-neg", Source: evalmodel.SourceReplayNegative, Label: evalmodel.LabelAssumedOK, Repo: repoName,
			Split: split, Revision: short(t.docCommit), Task: evalmodel.TaskJudge, TaskType: taskType, Expected: judgev1.No,
			GTType: it.Type, Claim: cl, Evidence: ev,
		})
	}
	return out
}

// evidenceAt picks up to three snippets from the item's evidence paths at a revision: the
// whole file when small, else a window around the line sharing most words with the claim.
func evidenceAt(ctx context.Context, dir, rev, repoName string, it GTItem, query string) []judgev1.Evidence {
	words := judge.Words(query)
	var out []judgev1.Evidence
	for _, p := range it.EvidencePaths {
		if p == it.File || len(out) == evMaxSnippets {
			continue
		}
		body, err := gitOut(ctx, dir, "show", rev+":"+p)
		if err != nil || strings.HasPrefix(body, "tree ") || strings.ContainsRune(body, 0) {
			continue
		}
		lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
		from, to := window(lines, words)
		out = append(out, judgev1.Evidence{Repo: repoName, Path: p, From: from, To: to, Text: strings.Join(lines[from-1:to], "\n")})
	}
	return out
}

// window returns a 1-based line range of at most evMaxBytes around the best-matching line.
func window(lines []string, words []string) (int, int) {
	if len(strings.Join(lines, "\n")) <= evMaxBytes {
		return 1, len(lines)
	}
	best, bestScore := 0, -1
	for i, l := range lines {
		ll := strings.ToLower(l)
		sc := 0
		for _, w := range words {
			if strings.Contains(ll, w) {
				sc++
			}
		}
		if sc > bestScore {
			best, bestScore = i, sc
		}
	}
	from, to := max(0, best-evRadius), min(len(lines)-1, best+evRadius)
	for size(lines[from:to+1]) > evMaxBytes && to > from {
		if to-best >= best-from {
			to--
		} else {
			from++
		}
	}
	return from + 1, to + 1
}

func size(ls []string) int {
	n := 0
	for _, l := range ls {
		n += len(l) + 1
	}
	return n
}

func writeEvalSet(p string, items []EvalItem) error {
	if p == "" {
		return nil
	}
	sum, err := evalmodel.WriteSet(p, items)
	if err != nil {
		return err //nolint:wrapcheck // already descriptive
	}
	_, err = fmt.Fprintf(os.Stderr, "bench replay: froze %d eval items in %s (sha256 %s)\n", len(items), p, sum)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
