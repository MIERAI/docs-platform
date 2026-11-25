package markdown

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	reMdLink  = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	reEmph    = regexp.MustCompile(`(^|\W)[*_]{1,3}|[*_]{1,3}(\W|$)`)
	reHTMLTag = regexp.MustCompile(`<[^>]+>`)
)

// Slugger produces GitHub-compatible heading anchors, de-duplicating repeats with -1, -2, ...
type Slugger struct{ seen map[string]int }

// NewSlugger returns an empty slugger (one per document).
func NewSlugger() *Slugger { return &Slugger{seen: map[string]int{}} }

// Slug returns the anchor for heading text.
func (s *Slugger) Slug(text string) string {
	base := slug(text)
	n, dup := s.seen[base]
	s.seen[base] = n + 1
	if !dup {
		return base
	}
	return base + "-" + strconv.Itoa(n)
}

func slug(text string) string {
	t := reMdLink.ReplaceAllString(text, "$1")
	t = reHTMLTag.ReplaceAllString(t, "")
	t = strings.ReplaceAll(t, "`", "")
	t = reEmph.ReplaceAllString(t, "$1$2")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(t)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}
