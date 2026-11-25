package repo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// DiffLine is one added or removed line of a change.
type DiffLine struct {
	N    int    // line number in the new file (added) or the old file (removed)
	Text string // without the leading + or -
}

// FileDiff is the change of one file.
type FileDiff struct {
	Path    string // new path ("" when the file was deleted)
	OldPath string // old path ("" when the file was added)
	Added   []DiffLine
	Removed []DiffLine
	Binary  bool
}

// Name is the path the file has after the change, or before it when it was deleted.
func (f FileDiff) Name() string {
	if f.Path != "" {
		return f.Path
	}
	return f.OldPath
}

// maxDiffBytes bounds the diff text docs-platform parses.
const maxDiffBytes = 64 << 20

// DiffHunks returns the line-level change between base and head (merge-base semantics, like a
// PR), without context lines.
func DiffHunks(ctx context.Context, dir, base, head string) ([]FileDiff, error) {
	if !ValidRef(base) || !ValidRef(head) {
		return nil, fmt.Errorf("invalid git ref %q or %q", base, head)
	}
	out, err := Git(ctx, dir, "diff", "--no-color", "--no-ext-diff", "--no-renames", "-U0", base+"..."+head, "--")
	if err != nil {
		return nil, err
	}
	if len(out) > maxDiffBytes {
		return nil, fmt.Errorf("diff %s...%s is larger than %d bytes", base, head, maxDiffBytes)
	}
	return ParseUnifiedDiff(out), nil
}

// ParseUnifiedDiff parses `git diff -U0` output. Malformed input never panics: unknown lines
// are skipped and a hunk header that cannot be read ends that file's hunks.
func ParseUnifiedDiff(s string) []FileDiff {
	var out []FileDiff
	var cur *FileDiff
	oldN, newN := 0, 0
	inHunk := false
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			out = append(out, FileDiff{})
			cur, inHunk = &out[len(out)-1], false
		case cur == nil:
		case !inHunk && strings.HasPrefix(l, "--- "):
			cur.OldPath = diffPath(l[4:], "a/")
		case !inHunk && strings.HasPrefix(l, "+++ "):
			cur.Path = diffPath(l[4:], "b/")
		case strings.HasPrefix(l, "Binary files "):
			cur.Binary = true
		case strings.HasPrefix(l, "@@ "):
			oldN, newN, inHunk = hunkStart(l)
		case inHunk && strings.HasPrefix(l, "-"):
			cur.Removed = append(cur.Removed, DiffLine{N: oldN, Text: l[1:]})
			oldN++
		case inHunk && strings.HasPrefix(l, "+"):
			cur.Added = append(cur.Added, DiffLine{N: newN, Text: l[1:]})
			newN++
		}
	}
	return out
}

func diffPath(s, prefix string) string {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strings.TrimPrefix(s, prefix)
}

// hunkStart reads "@@ -a[,b] +c[,d] @@"; ok is false when the header is malformed.
func hunkStart(l string) (int, int, bool) {
	f := strings.Fields(l)
	if len(f) < 3 || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, false
	}
	o, ok1 := rangeStart(f[1][1:])
	n, ok2 := rangeStart(f[2][1:])
	return o, n, ok1 && ok2
}

func rangeStart(s string) (int, bool) {
	a, _, _ := strings.Cut(s, ",")
	n, err := strconv.Atoi(a)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
