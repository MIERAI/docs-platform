package markdown

import "strings"

// inline extracts code spans and links from one line and records the remaining prose.
func (p *parser) inline(line string, n int) {
	prose := p.codeSpans(line, n)
	p.links(prose, n)
	p.doc.Text = append(p.doc.Text, Inline{Line: n, Text: prose})
}

// codeSpans records `code` spans and returns the line with spans blanked out.
func (p *parser) codeSpans(line string, n int) string {
	var prose strings.Builder
	i := 0
	for i < len(line) {
		if line[i] == '\\' && i+1 < len(line) {
			prose.WriteString(line[i : i+2])
			i += 2
			continue
		}
		if line[i] != '`' {
			prose.WriteByte(line[i])
			i++
			continue
		}
		run := runLen(line, i, '`')
		end := closingRun(line, i+run, run)
		if end < 0 {
			prose.WriteString(line[i : i+run])
			i += run
			continue
		}
		content := line[i+run : end]
		if len(content) >= 2 && content[0] == ' ' && content[len(content)-1] == ' ' && strings.TrimSpace(content) != "" {
			content = content[1 : len(content)-1]
		}
		p.doc.Spans = append(p.doc.Spans, Inline{Line: n, Text: content, Col: i})
		prose.WriteString(strings.Repeat(" ", end+run-i))
		i = end + run
	}
	return prose.String()
}

func runLen(s string, i int, c byte) int {
	j := i
	for j < len(s) && s[j] == c {
		j++
	}
	return j - i
}

// closingRun finds the next backtick run of exactly length run at or after i, or -1.
func closingRun(s string, i, run int) int {
	for i < len(s) {
		if s[i] != '`' {
			i++
			continue
		}
		l := runLen(s, i, '`')
		if l == run {
			return i
		}
		i += l
	}
	return -1
}

// links records inline link destinations [text](dest "title") and <dest> autolinks.
func (p *parser) links(s string, n int) {
	for i := 0; i < len(s); i++ {
		if s[i] != ']' || i+1 >= len(s) || s[i+1] != '(' {
			continue
		}
		end := matchParen(s, i+1)
		if end < 0 {
			continue
		}
		dest := strings.TrimSpace(s[i+2 : end])
		if j := strings.IndexAny(dest, " \t"); j >= 0 {
			dest = dest[:j]
		}
		dest = strings.TrimSuffix(strings.TrimPrefix(dest, "<"), ">")
		if dest != "" {
			p.doc.Links = append(p.doc.Links, Inline{Line: n, Text: dest})
		}
		i = end
	}
}

// matchParen returns the index of the ')' closing the '(' at open, honouring nesting.
func matchParen(s string, open int) int {
	depth := 0
	for j := open; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		case '\\':
			j++
		}
	}
	return -1
}
