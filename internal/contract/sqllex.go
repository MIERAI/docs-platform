package contract

import (
	"strings"
)

// Token kinds of the SQL lexer.
const (
	tkIdent  = 'i' // identifier or keyword (Text as written; Up upper-cased)
	tkQIdent = 'q' // "quoted identifier"
	tkNumber = 'n'
	tkString = 's' // 'literal' (Text without quotes)
	tkPunct  = 'p'
	tkParam  = 'P' // a query parameter (sqlc.arg(x), sqlc.narg(x), @x, $1)
	tkCol    = 'c' // a column reference: Qual.Text or Text
)

// sqlTok is one token of a SQL statement. Depth counts open parentheses: an opening
// parenthesis has the depth outside it, its content one more, the closing one the outside
// depth again.
type sqlTok struct {
	Kind  byte
	Text  string
	Up    string
	Qual  string // column reference qualifier (table alias)
	Cast  string // ::type attached to this token (or to the parenthesized expression it closes)
	Opt   bool   // parameter: sqlc.narg (nullable)
	Depth int
}

var sqlMultiPunct = []string{"->>", "#>>", "::", "<>", "!=", "<=", ">=", "||", "=>", "->"}

// lexSQL splits SQL text into tokens; comments are dropped. It never fails: anything it does
// not recognize becomes a one-character punctuation token.
func lexSQL(src string) []sqlTok {
	var toks []sqlTok
	for i := 0; i < len(src); {
		t, n := sqlNext(src, i)
		if t.Kind != 0 {
			toks = append(toks, t)
		}
		i = n
	}
	return toks
}

// sqlNext reads the token at i (Kind 0 = whitespace or comment) and returns the next index.
func sqlNext(src string, i int) (sqlTok, int) {
	c := src[i]
	switch {
	case isSpace(c):
		return sqlTok{}, i + 1
	case strings.HasPrefix(src[i:], "--"):
		return sqlTok{}, skipLine(src, i)
	case strings.HasPrefix(src[i:], "/*"):
		return sqlTok{}, skipBlock(src, i)
	case c == '\'':
		s, n := quoted(src, i, '\'')
		return sqlTok{Kind: tkString, Text: s}, n
	case c == '"':
		s, n := quoted(src, i, '"')
		return sqlTok{Kind: tkQIdent, Text: s, Up: strings.ToUpper(s)}, n
	case isDigit(c):
		n := scanWhile(src, i, func(b byte) bool { return isDigit(b) || b == '.' })
		return sqlTok{Kind: tkNumber, Text: src[i:n]}, n
	case isIdentStart(c):
		n := scanWhile(src, i, isIdentPart)
		return sqlTok{Kind: tkIdent, Text: src[i:n], Up: strings.ToUpper(src[i:n])}, n
	}
	p := punctAt(src, i)
	return sqlTok{Kind: tkPunct, Text: p}, i + len(p)
}

func skipLine(src string, i int) int {
	if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(src)
}

func skipBlock(src string, i int) int {
	if j := strings.Index(src[i+2:], "*/"); j >= 0 {
		return i + 2 + j + 2
	}
	return len(src)
}

// quoted reads a quoted run starting at src[i] == q; a doubled quote is an escaped quote.
func quoted(src string, i int, q byte) (string, int) {
	var b strings.Builder
	n := i + 1
	for n < len(src) {
		if src[n] == q {
			if n+1 < len(src) && src[n+1] == q {
				b.WriteByte(q)
				n += 2
				continue
			}
			return b.String(), n + 1
		}
		b.WriteByte(src[n])
		n++
	}
	return b.String(), n
}

func punctAt(src string, i int) string {
	for _, m := range sqlMultiPunct {
		if strings.HasPrefix(src[i:], m) {
			return m
		}
	}
	return src[i : i+1]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

// simplifySQL folds parameters, casts, column references and COLLATE clauses into single
// tokens and computes depths. isParam reports whether "name" is a parameter function
// (sqlc.arg ...) at toks[i:].
func simplifySQL(toks []sqlTok) []sqlTok {
	var out []sqlTok
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if p, n, ok := paramAt(toks, i); ok {
			out = append(out, p)
			i = n - 1
			continue
		}
		switch {
		case t.Kind == tkPunct && t.Text == "::":
			cast, n := castAt(toks, i+1)
			if len(out) > 0 {
				out[len(out)-1].Cast = cast
			}
			i = n - 1
			continue
		case t.Kind == tkIdent && t.Up == "COLLATE" && i+1 < len(toks):
			i++
			continue
		case (t.Kind == tkIdent || t.Kind == tkQIdent) && i+2 < len(toks) && toks[i+1].Text == "." && (toks[i+2].Kind == tkIdent || toks[i+2].Kind == tkQIdent):
			out = append(out, sqlTok{Kind: tkCol, Qual: t.Text, Text: toks[i+2].Text, Up: strings.ToUpper(toks[i+2].Text)})
			i += 2
			continue
		}
		out = append(out, t)
	}
	setDepths(out)
	return out
}

// paramAt recognizes sqlc.arg(name) / sqlc.narg(name) / sqlc.slice(name), @name and $N.
func paramAt(toks []sqlTok, i int) (sqlTok, int, bool) {
	if p, n, ok := sqlcParamAt(toks, i); ok {
		return p, n, true
	}
	t := toks[i]
	if t.Kind != tkPunct || (t.Text != "@" && t.Text != "$") || i+1 >= len(toks) {
		return sqlTok{}, 0, false
	}
	if next := toks[i+1]; next.Kind == tkIdent || next.Kind == tkNumber {
		name := next.Text
		if t.Text == "$" {
			name = "$" + name
		}
		return sqlTok{Kind: tkParam, Text: name, Up: strings.ToUpper(name)}, i + 2, true
	}
	return sqlTok{}, 0, false
}

func sqlcParamAt(toks []sqlTok, i int) (sqlTok, int, bool) {
	if toks[i].Kind != tkIdent || toks[i].Up != "SQLC" || i+5 >= len(toks) || toks[i+1].Text != "." || toks[i+3].Text != "(" || toks[i+5].Text != ")" {
		return sqlTok{}, 0, false
	}
	switch fn := toks[i+2].Up; fn {
	case "ARG", "NARG", "SLICE":
		name := toks[i+4].Text
		return sqlTok{Kind: tkParam, Text: name, Up: strings.ToUpper(name), Opt: fn == "NARG"}, i + 6, true
	}
	return sqlTok{}, 0, false
}

// castAt reads a type name after "::": ident [ident...] [(n[,m])] [[]].
func castAt(toks []sqlTok, i int) (string, int) {
	var parts []string
	for i < len(toks) && toks[i].Kind == tkIdent {
		w := strings.ToLower(toks[i].Text)
		if len(parts) > 0 && !typeContinues(parts[len(parts)-1], w) {
			break
		}
		parts = append(parts, w)
		i++
	}
	if i+1 < len(toks) && toks[i].Text == "(" && toks[i+1].Kind == tkNumber {
		for i < len(toks) && toks[i].Text != ")" {
			i++
		}
		i++
	}
	dims := 0
	for i+1 < len(toks) && toks[i].Text == "[" && toks[i+1].Text == "]" {
		dims++
		i += 2
	}
	return strings.Join(parts, " ") + strings.Repeat("[]", dims), i
}

// typeContinues reports whether word w continues a multi-word type name after prev
// (double precision, character varying, timestamp with time zone).
func typeContinues(prev, w string) bool {
	switch prev {
	case "double":
		return w == "precision"
	case "character":
		return w == "varying"
	case "timestamp", "time":
		return w == "with" || w == "without" || w == "zone"
	case "with", "without":
		return w == "time"
	}
	return false
}

func setDepths(toks []sqlTok) {
	d := 0
	for i := range toks {
		switch toks[i].Text {
		case "(":
			if toks[i].Kind == tkPunct {
				toks[i].Depth = d
				d++
				continue
			}
		case ")":
			if toks[i].Kind == tkPunct && d > 0 {
				d--
			}
		}
		toks[i].Depth = d
	}
}
