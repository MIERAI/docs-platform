package refclass

import "strings"

// Words splits a command line into words with simple POSIX-like quoting (single quotes,
// double quotes, backslash escapes). Operators && || ; | are returned as their own words.
// It never fails: unbalanced quotes end at the end of the line.
func Words(line string) []string {
	w := &wordSplitter{rs: []rune(line)}
	for w.i = 0; w.i < len(w.rs); w.i++ {
		w.step(w.rs[w.i])
	}
	w.flush()
	return w.words
}

type wordSplitter struct {
	rs    []rune
	i     int
	words []string
	cur   strings.Builder
	quote rune
	has   bool
}

func (w *wordSplitter) flush() {
	if w.has {
		w.words = append(w.words, w.cur.String())
	}
	w.cur.Reset()
	w.has = false
}

func (w *wordSplitter) step(r rune) {
	switch {
	case w.quote != 0:
		if r == w.quote {
			w.quote = 0
		} else {
			w.cur.WriteRune(r)
		}
	case r == '\'' || r == '"':
		w.quote, w.has = r, true
	case r == '\\' && w.i+1 < len(w.rs):
		w.i++
		w.cur.WriteRune(w.rs[w.i])
		w.has = true
	case r == ' ' || r == '\t':
		w.flush()
	case r == ';' || r == '|' || r == '&':
		w.flush()
		op := string(r)
		if w.i+1 < len(w.rs) && w.rs[w.i+1] == r {
			op += string(r)
			w.i++
		}
		w.words = append(w.words, op)
	default:
		w.cur.WriteRune(r)
		w.has = true
	}
}

// splitCommands splits words at shell operators.
func splitCommands(words []string) [][]string {
	var (
		out [][]string
		cur []string
	)
	for _, w := range words {
		switch w {
		case "&&", "||", ";", "|", "&", ";;":
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = nil
		default:
			cur = append(cur, w)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// stripComment removes a trailing shell comment (a # at the start of a word, outside quotes).
func stripComment(line string) string {
	var quote rune
	prev := ' '
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '#' && (prev == ' ' || prev == '\t'):
			return line[:i]
		}
		prev = r
	}
	return line
}
