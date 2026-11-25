// Package comments treats source-code comments as documentation: header comments, package
// docs and inline notes often name files, scripts and settings, and drift like any README. It
// extracts references from comments (inline code and file paths) and, as a plugin.Stripper,
// lets the core index code tokens without comment text (so a stale comment cannot vouch for
// itself).
package comments

import (
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/refclass"
)

// Name and Version of this extractor.
const (
	Name    = "comments"
	Version = "0.1.0"
)

// Extractor is the source-comment extractor.
type Extractor struct{}

// Info implements plugin.Extractor.
func (Extractor) Info() plugin.Info {
	return plugin.Info{Name: Name, Version: Version, Side: plugin.SideComments}
}

type syntax struct{ slash, hash bool }

var byExt = map[string]syntax{
	".go": {slash: true}, ".js": {slash: true}, ".mjs": {slash: true}, ".ts": {slash: true},
	".java": {slash: true}, ".rs": {slash: true}, ".kt": {slash: true}, ".proto": {slash: true},
	".tf": {slash: true, hash: true}, ".hcl": {slash: true, hash: true},
	".sh": {hash: true}, ".bash": {hash: true}, ".py": {hash: true}, ".yaml": {hash: true}, ".yml": {hash: true},
	".toml": {hash: true}, ".rb": {hash: true}, ".conf": {hash: true}, ".mk": {hash: true},
}

var byBase = map[string]syntax{"Makefile": {hash: true}, "Dockerfile": {hash: true}, ".gitignore": {hash: true}}

func lookup(p string) (syntax, bool) {
	if s, ok := byBase[path.Base(p)]; ok {
		return s, true
	}
	if strings.HasPrefix(path.Base(p), "Dockerfile") {
		return syntax{hash: true}, true
	}
	s, ok := byExt[strings.ToLower(path.Ext(p))]
	return s, ok
}

// Accepts implements plugin.Extractor.
func (Extractor) Accepts(p string) bool {
	_, ok := lookup(p)
	return ok && !strings.Contains("/"+p, "/testdata/")
}

// Segment is the text of one comment line.
type Segment struct {
	Line       int
	Start, End int // byte offsets of the comment text in the file
	Text       string
}

// Segments returns every comment line of a file.
func Segments(p string, b []byte) []Segment {
	syn, ok := lookup(p)
	if !ok {
		return nil
	}
	var out []Segment
	inBlock := false
	off := 0
	for i, line := range strings.SplitAfter(string(b), "\n") {
		segs, block := lineComments(line, syn, inBlock)
		inBlock = block
		for _, s := range segs {
			out = append(out, Segment{Line: i + 1, Start: off + s[0], End: off + s[1], Text: line[s[0]:s[1]]})
		}
		off += len(line)
	}
	return out
}

// lineComments returns [start,end) ranges of comment text on one line.
func lineComments(line string, syn syntax, inBlock bool) ([][2]int, bool) {
	if inBlock {
		if j := strings.Index(line, "*/"); j >= 0 {
			return [][2]int{{0, j}}, false
		}
		return [][2]int{{0, len(line)}}, true
	}
	for i := 0; i < len(line); i++ {
		if c := line[i]; c == '"' || c == '\'' || c == '`' {
			i = skipString(line, i)
			continue
		}
		if r, block, ok := commentAt(line, i, syn); ok {
			return [][2]int{r}, block
		}
	}
	return nil, false
}

// commentAt reports whether a comment starts at line[i], its text range and whether it is an
// unterminated block comment.
func commentAt(line string, i int, syn syntax) ([2]int, bool, bool) {
	rest := line[i:]
	switch {
	case syn.hash && rest[0] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
		return [2]int{i + 1, len(line)}, false, true
	case syn.slash && strings.HasPrefix(rest, "//") && (i == 0 || line[i-1] != ':'):
		return [2]int{i + 2, len(line)}, false, true
	case syn.slash && strings.HasPrefix(rest, "/*"):
		if j := strings.Index(rest[2:], "*/"); j >= 0 {
			return [2]int{i + 2, i + 2 + j}, false, true
		}
		return [2]int{i + 2, len(line)}, true, true
	}
	return [2]int{}, false, false
}

func skipString(line string, i int) int {
	q := line[i]
	for j := i + 1; j < len(line); j++ {
		if line[j] == '\\' && q != '`' {
			j++
			continue
		}
		if line[j] == q {
			return j
		}
	}
	return len(line)
}

// Strip returns the content with comment text blanked (offsets and lines preserved).
func (Extractor) Strip(p string, b []byte) []byte {
	segs := Segments(p, b)
	if len(segs) == 0 {
		return b
	}
	out := append([]byte(nil), b...)
	for _, s := range segs {
		for k := s.Start; k < s.End && k < len(out); k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	return out
}

var reSpan = regexp.MustCompile("`([^`\n]+)`")

// Extract implements plugin.Extractor.
func (Extractor) Extract(f plugin.File) (model.Result, error) {
	var r model.Result
	for _, s := range Segments(f.Path, f.Content) {
		for _, m := range reSpan.FindAllStringSubmatchIndex(s.Text, -1) {
			q := refclass.Qualifier(s.Text, m[0])
			for _, c := range refclass.Span(s.Text[m[2]:m[3]]) {
				r.References = append(r.References, ref(f.Path, s.Line, c, q))
			}
		}
		prose := reSpan.ReplaceAllStringFunc(s.Text, func(x string) string { return strings.Repeat(" ", len(x)) })
		for _, c := range refclass.ProsePaths(prose) {
			r.References = append(r.References, ref(f.Path, s.Line, c, refclass.Qualifier(prose, c.Offset)))
		}
	}
	return r, nil
}

func ref(p string, line int, c refclass.Candidate, qualifier string) model.Reference {
	return model.Reference{
		Kind: c.Kind, Text: c.Text, Program: c.Program, Path: p, Line: line,
		Confidence: c.Confidence, Context: "comment", Source: Name, Qualifier: qualifier,
	}
}
