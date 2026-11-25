// Package lineidx maps byte offsets to 1-based line numbers for regex-based extractors.
package lineidx

import (
	"bytes"
	"sort"
)

// Index holds the offsets of line starts.
type Index []int

// New indexes content.
func New(b []byte) Index {
	idx := Index{0}
	for i := 0; ; {
		j := bytes.IndexByte(b[i:], '\n')
		if j < 0 {
			return idx
		}
		i += j + 1
		idx = append(idx, i)
	}
}

// Line returns the 1-based line containing byte offset off.
func (x Index) Line(off int) int {
	return sort.Search(len(x), func(i int) bool { return x[i] > off })
}
