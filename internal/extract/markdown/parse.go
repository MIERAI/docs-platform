// Package markdown is the docs-side extractor for Markdown (CommonMark/GFM subset): inline code
// spans, fenced code blocks, links, headings (with GitHub-style anchors) and inline
// suppressions written as <!-- docsp:ignore <reason> -->.
package markdown

import (
	"regexp"
	"strings"
)

// Doc is the parsed structure of one Markdown file. Lines are 1-based.
type Doc struct {
	Headings     []Heading
	Anchors      []Anchor
	Spans        []Inline
	Links        []Inline
	Blocks       []Block
	Text         []Inline
	Suppressions []Suppression
}

// Heading is an ATX or setext heading.
type Heading struct {
	Line  int
	Level int
	Text  string
}

// Anchor is an explicit HTML anchor (<a name=..> or id=..).
type Anchor struct {
	Line int
	ID   string
}

// Inline is a piece of inline content on a line.
type Inline struct {
	Line int
	Text string
	Col  int // byte offset of the item on its line (spans only)
}

// Block is a fenced code block.
type Block struct {
	Line  int // line of the first content line
	Lang  string
	Lines []string
}

// Suppression silences references on lines [From, To].
type Suppression struct {
	From, To int
	Reason   string
}

var (
	reFence    = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})\\s*([^`\\s{]*)")
	reATX      = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	reSetext   = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	reRefDef   = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:[ \t]*<?([^\s>]+)>?`)
	reHTMLAnch = regexp.MustCompile(`<a\s+(?:name|id)="([^"]+)"|\sid="([^"]+)"`)
	reIgnore   = regexp.MustCompile(`<!--\s*docsp:(ignore-start|ignore-end|ignore)\b\s*(.*?)\s*-->`)
)

// Parse parses Markdown source. It never fails; malformed input degrades to plain text.
func Parse(src []byte) *Doc {
	p := &parser{doc: &Doc{}, lines: splitLines(string(src))}
	p.run()
	return p.doc
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

type parser struct {
	doc        *Doc
	lines      []string
	i          int
	prevPara   bool
	ignoreFrom int
	ignoreWhy  string
}

func (p *parser) run() {
	p.skipFrontMatter()
	for p.i < len(p.lines) {
		line := p.lines[p.i]
		if m := reFence.FindStringSubmatch(line); m != nil {
			p.fence(m[1], m[2])
			continue
		}
		p.line(line)
		p.i++
	}
	if p.ignoreFrom > 0 {
		p.doc.Suppressions = append(p.doc.Suppressions, Suppression{From: p.ignoreFrom, To: len(p.lines), Reason: p.ignoreWhy})
	}
}

func (p *parser) skipFrontMatter() {
	if len(p.lines) == 0 || strings.TrimSpace(p.lines[0]) != "---" {
		return
	}
	for j := 1; j < len(p.lines); j++ {
		if t := strings.TrimSpace(p.lines[j]); t == "---" || t == "..." {
			p.i = j + 1
			return
		}
	}
}

func (p *parser) fence(marker, lang string) {
	start := p.i + 1
	b := Block{Line: start + 1, Lang: lang}
	p.i++
	for ; p.i < len(p.lines); p.i++ {
		t := strings.TrimLeft(p.lines[p.i], " ")
		if strings.HasPrefix(t, marker[:1]) && len(p.lines[p.i])-len(t) <= 3 &&
			strings.TrimRight(strings.TrimLeft(t, marker[:1]), " \t") == "" && len(strings.TrimRight(t, " \t")) >= len(marker) {
			p.i++
			break
		}
		b.Lines = append(b.Lines, p.lines[p.i])
	}
	p.doc.Blocks = append(p.doc.Blocks, b)
	p.prevPara = false
}

func (p *parser) line(line string) {
	n := p.i + 1
	p.directives(line, n)
	if m := reATX.FindStringSubmatch(line); m != nil {
		p.doc.Headings = append(p.doc.Headings, Heading{Line: n, Level: len(m[1]), Text: m[2]})
		p.inline(m[2], n)
		p.prevPara = false
		return
	}
	if m := reSetext.FindStringSubmatch(line); m != nil && p.prevPara {
		lvl := 1
		if m[1][0] == '-' {
			lvl = 2
		}
		p.doc.Headings = append(p.doc.Headings, Heading{Line: n - 1, Level: lvl, Text: strings.TrimSpace(p.lines[p.i-1])})
		p.prevPara = false
		return
	}
	if m := reRefDef.FindStringSubmatch(line); m != nil {
		p.doc.Links = append(p.doc.Links, Inline{Line: n, Text: m[1]})
		p.prevPara = false
		return
	}
	p.prevPara = strings.TrimSpace(line) != "" && !isIndentedCode(line)
	if !isIndentedCode(line) || !p.prevParaBefore() {
		p.inline(line, n)
	}
}

func (p *parser) prevParaBefore() bool {
	return p.i > 0 && strings.TrimSpace(p.lines[p.i-1]) != ""
}

func isIndentedCode(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

func (p *parser) directives(line string, n int) {
	for _, m := range reIgnore.FindAllStringSubmatch(line, -1) {
		reason := strings.TrimSpace(m[2])
		switch m[1] {
		case "ignore":
			if reason != "" {
				p.doc.Suppressions = append(p.doc.Suppressions, Suppression{From: n, To: n + 1, Reason: reason})
			}
		case "ignore-start":
			if reason != "" && p.ignoreFrom == 0 {
				p.ignoreFrom, p.ignoreWhy = n, reason
			}
		case "ignore-end":
			if p.ignoreFrom > 0 {
				p.doc.Suppressions = append(p.doc.Suppressions, Suppression{From: p.ignoreFrom, To: n, Reason: p.ignoreWhy})
				p.ignoreFrom = 0
			}
		}
	}
	for _, m := range reHTMLAnch.FindAllStringSubmatch(line, -1) {
		p.doc.Anchors = append(p.doc.Anchors, Anchor{Line: n, ID: m[1] + m[2]})
	}
}
