package draftverify

import "strings"

// Change is one changed line pair: Old (1-based line in the old text, 0 = added) replaced by
// New (1-based line in the new text, 0 = deleted).
type Change struct {
	Old, New         int
	OldText, NewText string
	// at is, for a deleted line, the new-text line it followed (0 = the start).
	at int
}

// maxLCS bounds the quadratic part of the line diff; larger middles become one hunk.
const maxLCS = 4 << 20

// splitLines splits text into lines (a final newline does not start an empty line).
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// diffLines lists the changed lines of a and b: common prefix and suffix are trimmed, the
// middle is aligned by the longest common subsequence, and inside each hunk removed and
// added lines are paired in order (a replaced line is one Change with both sides).
func diffLines(a, b []string) []Change {
	p, s := trimCommon(a, b)
	ma, mb := a[p:len(a)-s], b[p:len(b)-s]
	var out []Change
	flush := func(i0, i1, j0, j1 int) {
		for k := 0; i0+k < i1 || j0+k < j1; k++ {
			c := Change{}
			if i0+k < i1 {
				c.Old, c.OldText = p+i0+k+1, ma[i0+k]
			}
			if j0+k < j1 {
				c.New, c.NewText = p+j0+k+1, mb[j0+k]
			} else {
				c.at = p + j1
			}
			out = append(out, c)
		}
	}
	if len(ma)*len(mb) > maxLCS {
		flush(0, len(ma), 0, len(mb))
		return out
	}
	lcs := table(ma, mb)
	i, j, hi, hj := 0, 0, 0, 0
	for i < len(ma) && j < len(mb) {
		switch {
		case ma[i] == mb[j]:
			flush(hi, i, hj, j)
			i, j = i+1, j+1
			hi, hj = i, j
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	flush(hi, len(ma), hj, len(mb))
	return out
}

// trimCommon is the length of the common prefix and (then) suffix of a and b.
func trimCommon(a, b []string) (int, int) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	return p, s
}

// table[i][j] is the LCS length of a[i:] and b[j:].
func table(a, b []string) [][]int {
	t := make([][]int, len(a)+1)
	for i := range t {
		t[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	return t
}

// touchedNew is changedNew plus the new-text lines around each deleted line (a block that
// lost a line is a changed block).
func touchedNew(cs []Change) map[int]bool {
	m := changedNew(cs)
	for _, c := range cs {
		if c.New == 0 {
			m[c.at], m[c.at+1] = true, true
		}
	}
	return m
}

// changedNew is the set of new-text lines a change touches.
func changedNew(cs []Change) map[int]bool {
	m := map[int]bool{}
	for _, c := range cs {
		if c.New > 0 {
			m[c.New] = true
		}
	}
	return m
}
