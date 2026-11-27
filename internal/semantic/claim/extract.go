package claim

import (
	"regexp"
	"strings"
)

// MaxLines bounds how much of one document is read for claims.
const MaxLines = 20000

// StatusLines is how far down a document status line ("Status: draft") may sit: it belongs to the header.
const StatusLines = 10

var (
	reFenceLine = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	reHeading   = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	reBullet    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	reBulletKey = regexp.MustCompile("^(?:`([^`]{1,80})`|\\*\\*([^*]{1,80})\\*\\*|\\[([^\\]]{1,80})\\]\\()")
	reHTMLComm  = regexp.MustCompile(`<!--.*?-->`)
	reTableSep  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}`)
)

// Line is one line of text with its 1-based number.
type Line struct {
	N    int
	Text string
}

// Markdown extracts claims from a Markdown document.
func Markdown(path string, src []byte) []Claim {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	if len(lines) > MaxLines {
		lines = lines[:MaxLines]
	}
	var prose []Line
	section := map[int]string{}
	cur, fence := "", ""
	for i, l := range lines {
		if fence != "" {
			if strings.HasPrefix(strings.TrimSpace(l), fence) {
				fence = ""
			}
			continue
		}
		if m := reFenceLine.FindStringSubmatch(l); m != nil {
			fence = m[1][:3]
			continue
		}
		if m := reHeading.FindStringSubmatch(l); m != nil {
			cur = m[1]
		}
		section[i+1] = cur
		prose = append(prose, Line{N: i + 1, Text: l})
	}
	out := fromLines(path, prose, false)
	for i := range out {
		out[i].Status = out[i].Status && out[i].Line <= StatusLines
	}
	out = append(out, bulletLists(path, prose)...)
	for i := range out {
		out[i].Section = section[out[i].Line]
	}
	return out
}

// Comments extracts claims from source-code comment lines (markers already removed or not).
func Comments(path string, lines []Line) []Claim {
	clean := make([]Line, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l.Text)
		t = strings.TrimLeft(t, "/#*-;")
		clean = append(clean, Line{N: l.N, Text: t})
	}
	return fromLines(path, clean, true)
}

func fromLines(path string, lines []Line, comment bool) []Claim {
	var out []Claim
	for _, l := range lines {
		text := reHTMLComm.ReplaceAllString(l.Text, "")
		if strings.TrimSpace(text) == "" || reTableSep.MatchString(text) {
			continue
		}
		for _, s := range Sentences(text) {
			c := fromText(s, spansOf(s))
			if !c.Checkable() {
				continue
			}
			c.Path, c.Line, c.Comment = path, l.N, comment
			out = append(out, c)
		}
	}
	return out
}

// ProseLines are the non-blank lines of a Markdown document outside fenced code blocks (the
// lines claims come from).
func ProseLines(src []byte) []Line {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	if len(lines) > MaxLines {
		lines = lines[:MaxLines]
	}
	var out []Line
	fence := ""
	for i, l := range lines {
		if fence != "" {
			if strings.HasPrefix(strings.TrimSpace(l), fence) {
				fence = ""
			}
			continue
		}
		if m := reFenceLine.FindStringSubmatch(l); m != nil {
			fence = m[1][:3]
			continue
		}
		if strings.TrimSpace(l) != "" {
			out = append(out, Line{N: i + 1, Text: l})
		}
	}
	return out
}

func spansOf(s string) []string {
	var out []string
	for _, m := range reSpan.FindAllString(s, -1) {
		out = append(out, strings.Trim(m, "`"))
	}
	return out
}

// bulletLists finds runs of bullet items that each start with a name (`x`, **x**, [x](..)) and
// reports them as one list claim on the line introducing the list.
func bulletLists(path string, lines []Line) []Claim {
	var out []Claim
	var run []Line
	flush := func(intro Line) {
		if len(run) >= 2 {
			out = append(out, bulletClaim(path, intro, run))
		}
		run = nil
	}
	intro := Line{}
	for _, l := range lines {
		m := reBullet.FindStringSubmatch(l.Text)
		switch {
		case m != nil && reBulletKey.MatchString(m[1]) && !isIndented(l.Text):
			run = append(run, l)
		case strings.TrimSpace(l.Text) == "":
		case isIndented(l.Text) && len(run) > 0: // nested item or continuation
		default:
			flush(intro)
			intro = Line{}
			if m == nil {
				intro = l
			}
		}
	}
	flush(intro)
	return out
}

func isIndented(s string) bool { return strings.HasPrefix(s, "  ") || strings.HasPrefix(s, "\t") }

func bulletClaim(path string, intro Line, run []Line) Claim {
	var items []string
	for _, l := range run {
		m := reBulletKey.FindStringSubmatch(reBullet.FindStringSubmatch(l.Text)[1])
		items = append(items, m[1]+m[2]+m[3])
	}
	at := intro
	if at.N == 0 || run[0].N-at.N > 2 {
		at = run[0]
	}
	text := strings.TrimSpace(at.Text)
	return Claim{
		Path: path, Line: at.N, Text: text,
		Lists: []List{{Items: normItems(items), Open: reOpen.MatchString(text), Closed: reClosed.MatchString(text)}},
	}
}
