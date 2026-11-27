package draftverify

import (
	"context"
	"errors"
	"strings"
)

// ErrLineGone is returned when the drafted line is no longer in the document.
var ErrLineGone = errors.New("the drafted line is no longer in the document")

// ApplyLine is text with one line drafted: the line equal (whitespace-normalised) to
// original - at line when it still is there, else the nearest such line - replaced by
// replacement, or removed when del. The text's line ending style and final newline are kept.
func ApplyLine(text string, line int, original, replacement string, del bool) (string, error) {
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	ls := strings.Split(text, nl)
	want := norm(original)
	at, best := -1, 0
	for i, l := range ls {
		if norm(l) != want {
			continue
		}
		if d := abs(i + 1 - line); at < 0 || d < best {
			at, best = i, d
		}
	}
	if at < 0 || want == "" {
		return "", ErrLineGone
	}
	if del {
		ls = append(ls[:at], ls[at+1:]...)
	} else {
		ls[at] = leading(ls[at]) + strings.TrimLeft(replacement, " \t")
	}
	return strings.Join(ls, nl), nil
}

// leading keeps the line's indentation when the replacement has none of its own.
func leading(l string) string {
	return l[:len(l)-len(strings.TrimLeft(l, " \t"))]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Refused is the result for a draft that cannot even be placed in the document (its line
// is gone): the guard fails and nothing else runs.
func Refused(ctx context.Context, in *Input, why string) *Result {
	r := &Result{
		SchemaVersion: SchemaVersion, Schema: Schema, Repo: in.RepoName, Path: in.Path, ClaimID: in.ClaimID,
		BaseBlob: in.BaseBlob, CurrentBlob: BlobID(ctx, in.Root, in.Old), ChangedLines: []int{}, Sandbox: "none",
	}
	if in.Sandbox != nil {
		r.Sandbox = in.Sandbox.Name()
	}
	r.Gates = []Gate{{Name: GateGuard, Status: Fail, Detail: why}}
	for _, n := range []string{GateFormat, GateClaims, GateExamples} {
		r.Gates = append(r.Gates, Gate{Name: n, Status: Skipped, Detail: "not run: the draft cannot be placed in the document"})
	}
	return r
}
