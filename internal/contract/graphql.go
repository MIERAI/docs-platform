package contract

import (
	"errors"
	"strings"
)

// gqlTok is a GraphQL token: a name, a punctuator, a number, a string.
type gqlTok struct {
	kind byte // 'n' name, 'p' punctuator, '1' number, 's' string
	text string
	pos  int
}

// lexGraphQL tokenizes a GraphQL document (commas and comments are insignificant).
func lexGraphQL(src string) []gqlTok {
	var toks []gqlTok
	for i := 0; i < len(src); {
		t, n := gqlNext(src, i)
		if t.kind != 0 {
			toks = append(toks, t)
		}
		i = n
	}
	return toks
}

// gqlNext reads the token at i (kind 0 = insignificant) and returns the next index.
func gqlNext(src string, i int) (gqlTok, int) {
	c := src[i]
	switch {
	case isSpace(c) || c == ',':
		return gqlTok{}, i + 1
	case c == '#':
		return gqlTok{}, skipLine(src, i)
	case strings.HasPrefix(src[i:], `"""`):
		end := len(src)
		if j := strings.Index(src[i+3:], `"""`); j >= 0 {
			end = i + 3 + j + 3
		}
		return gqlTok{'s', src[i:end], i}, end
	case c == '"':
		s, n := gqlString(src, i)
		return gqlTok{'s', s, i}, n
	case strings.HasPrefix(src[i:], "..."):
		return gqlTok{'p', "...", i}, i + 3
	case isGQLNameStart(c):
		n := scanWhile(src, i, func(b byte) bool { return isGQLNameStart(b) || isDigit(b) })
		return gqlTok{'n', src[i:n], i}, n
	case c == '-' || isDigit(c):
		n := scanWhile(src, i+1, func(b byte) bool { return isDigit(b) || strings.IndexByte(".eE+-", b) >= 0 })
		return gqlTok{'1', src[i:n], i}, n
	}
	return gqlTok{'p', src[i : i+1], i}, i + 1
}

func isGQLNameStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// scanWhile returns the first index from i where ok is false.
func scanWhile(src string, i int, ok func(byte) bool) int {
	for i < len(src) && ok(src[i]) {
		i++
	}
	return i
}

func gqlString(src string, i int) (string, int) {
	n := i + 1
	for n < len(src) && src[n] != '"' && src[n] != '\n' {
		if src[n] == '\\' {
			n++
		}
		n++
	}
	if n >= len(src) {
		return src[i+1:], len(src)
	}
	return src[i+1 : min(n, len(src))], n + 1
}

// gqlOp is one root field of an operation definition.
type gqlOp struct {
	Name, Kind string
	Args       []Field
	Fields     []Field
	Pos        int
}

type gqlParser struct {
	toks      []gqlTok
	i         int
	vars      map[string]string
	fragments map[string][]Field
	steps     int
}

var errGQL = errors.New("graphql: unexpected token")

// maxGQLSteps bounds the parser's work on hostile input.
const maxGQLSteps = 1 << 20

// parseGraphQL returns the root fields of every operation of a document, with argument
// types resolved from the variable definitions and the selected fields (fragments in the
// same document expanded). It never panics; on a syntax error it returns what it read.
func parseGraphQL(src string) ([]gqlOp, error) {
	p := &gqlParser{toks: lexGraphQL(src), fragments: map[string][]Field{}}
	// fragments first, so that spreads resolve whatever the order
	for p.i = 0; p.i < len(p.toks); p.i++ {
		if p.is('n', "fragment") && p.i+3 < len(p.toks) {
			name := p.toks[p.i+1].text
			p.i += 4 // fragment Name on Type
			p.directives()
			if fs, err := p.selectionSet(0); err == nil {
				p.fragments[name] = fs
			}
		}
	}
	var ops []gqlOp
	var err error
	for p.i = 0; p.i < len(p.toks) && err == nil; {
		var more []gqlOp
		more, err = p.definition()
		ops = append(ops, more...)
	}
	return ops, err
}

func (p *gqlParser) is(kind byte, text string) bool {
	return p.i < len(p.toks) && p.toks[p.i].kind == kind && p.toks[p.i].text == text
}

func (p *gqlParser) tick() error {
	p.steps++
	if p.steps > maxGQLSteps {
		return errGQL
	}
	return nil
}

func (p *gqlParser) definition() ([]gqlOp, error) {
	if p.is('n', "fragment") {
		p.i += 4
		p.directives()
		_, err := p.selectionSet(0)
		return nil, err
	}
	kind := "query"
	if p.i < len(p.toks) && p.toks[p.i].kind == 'n' {
		kind = p.toks[p.i].text
		if kind != "query" && kind != "mutation" && kind != "subscription" {
			return nil, errGQL
		}
		p.i++
		if p.i < len(p.toks) && p.toks[p.i].kind == 'n' {
			p.i++ // operation name
		}
	}
	p.vars = map[string]string{}
	if p.is('p', "(") {
		if err := p.variableDefs(); err != nil {
			return nil, err
		}
	}
	p.directives()
	if !p.is('p', "{") {
		return nil, errGQL
	}
	return p.rootFields(kind)
}

func (p *gqlParser) rootFields(kind string) ([]gqlOp, error) {
	p.i++ // {
	var ops []gqlOp
	for p.i < len(p.toks) && !p.is('p', "}") {
		if err := p.tick(); err != nil {
			return ops, err
		}
		pos := p.toks[p.i].pos
		f, args, err := p.field(1)
		if err != nil {
			return ops, err
		}
		if f.Name != "" {
			ops = append(ops, gqlOp{Name: f.Name, Kind: kind, Args: args, Fields: f.Fields, Pos: pos})
		}
	}
	p.i++
	return ops, nil
}

// variableDefs reads ($a: T = default @dir, ...).
func (p *gqlParser) variableDefs() error {
	p.i++
	for p.i < len(p.toks) && !p.is('p', ")") {
		if err := p.tick(); err != nil {
			return err
		}
		if !p.is('p', "$") || p.i+2 >= len(p.toks) || p.toks[p.i+2].text != ":" {
			return errGQL
		}
		name := p.toks[p.i+1].text
		p.i += 3
		p.vars[name] = p.typeRef()
		if p.is('p', "=") {
			p.i++
			p.value()
		}
		p.directives()
	}
	p.i++
	return nil
}

// typeRef reads Name, [T], T! into GraphQL notation.
func (p *gqlParser) typeRef() string {
	if p.i >= len(p.toks) {
		return ""
	}
	var t string
	if p.is('p', "[") {
		p.i++
		t = "[" + p.typeRef() + "]"
		if p.is('p', "]") {
			p.i++
		}
	} else {
		t = p.toks[p.i].text
		p.i++
	}
	if p.is('p', "!") {
		p.i++
		t += "!"
	}
	return t
}

// value skips a value and returns a type guess for a literal ("" when unknown) or the
// variable's declared type.
func (p *gqlParser) value() string {
	if p.i >= len(p.toks) {
		return ""
	}
	t := p.toks[p.i]
	switch {
	case t.kind == 'p' && t.text == "$" && p.i+1 < len(p.toks):
		p.i += 2
		return p.vars[p.toks[p.i-1].text]
	case t.kind == 'p' && (t.text == "[" || t.text == "{"):
		p.skipBalanced()
		return ""
	case t.kind == '1':
		p.i++
		if strings.ContainsAny(t.text, ".eE") {
			return "Float"
		}
		return "Int"
	case t.kind == 's':
		p.i++
		return "String"
	}
	p.i++
	return ""
}

func (p *gqlParser) skipBalanced() {
	depth := 0
	for ; p.i < len(p.toks) && p.steps < maxGQLSteps; p.i++ {
		p.steps++
		switch p.toks[p.i].text {
		case "[", "{", "(":
			depth++
		case "]", "}", ")":
			depth--
			if depth == 0 {
				p.i++
				return
			}
		}
	}
}

func (p *gqlParser) directives() {
	for p.is('p', "@") && p.i+1 < len(p.toks) {
		p.i += 2
		if p.is('p', "(") {
			p.skipBalanced()
		}
	}
}

// field reads [alias:] name [(args)] [@dir] [{ ... }].
func (p *gqlParser) field(depth int) (Field, []Field, error) {
	if p.i >= len(p.toks) || p.toks[p.i].kind != 'n' {
		return Field{}, nil, errGQL
	}
	name := p.toks[p.i].text
	p.i++
	if p.is('p', ":") && p.i+1 < len(p.toks) {
		name = p.toks[p.i+1].text // the field, not its alias
		p.i += 2
	}
	var args []Field
	if p.is('p', "(") {
		args = p.arguments()
	}
	p.directives()
	f := Field{Name: name}
	if p.is('p', "{") {
		fs, err := p.selectionSet(depth)
		if err != nil {
			return f, args, err
		}
		f.Fields = fs
	}
	return f, args, nil
}

func (p *gqlParser) arguments() []Field {
	p.i++
	var args []Field
	for p.i < len(p.toks) && !p.is('p', ")") && p.steps < maxGQLSteps {
		p.steps++
		if p.toks[p.i].kind != 'n' || p.i+1 >= len(p.toks) || p.toks[p.i+1].text != ":" {
			p.i++
			continue
		}
		name := p.toks[p.i].text
		p.i += 2
		typ := p.value()
		args = append(args, gqlField(name, typ))
	}
	p.i++
	return args
}

// gqlField turns a GraphQL type into a field: "!" becomes presence required.
func gqlField(name, typ string) Field {
	f := Field{Name: name, Type: strings.TrimSuffix(typ, "!")}
	if typ != "" {
		f.Presence = Optional
		if strings.HasSuffix(typ, "!") {
			f.Presence = Required
		}
	}
	return f
}

func (p *gqlParser) selectionSet(depth int) ([]Field, error) {
	if !p.is('p', "{") {
		return nil, errGQL
	}
	if depth > 32 {
		p.skipBalanced()
		return nil, nil
	}
	p.i++
	var fs []Field
	for p.i < len(p.toks) && !p.is('p', "}") {
		if err := p.tick(); err != nil {
			return fs, err
		}
		if p.is('p', "...") {
			fs = append(fs, p.spread(depth)...)
			continue
		}
		f, _, err := p.field(depth + 1)
		if err != nil {
			return fs, err
		}
		fs = mergeField(fs, f)
	}
	p.i++
	return fs, nil
}

// spread expands "...Name" (a fragment of the document) or "... on T { }".
func (p *gqlParser) spread(depth int) []Field {
	p.i++
	if p.is('n', "on") {
		p.i += 2
	}
	if p.i < len(p.toks) && p.toks[p.i].kind == 'n' {
		name := p.toks[p.i].text
		p.i++
		p.directives()
		return p.fragments[name]
	}
	p.directives()
	fs, _ := p.selectionSet(depth + 1)
	return fs
}

// mergeField adds f, merging the sub-selections of a field selected twice.
func mergeField(fs []Field, f Field) []Field {
	for i := range fs {
		if fs[i].Name == f.Name {
			for _, sub := range f.Fields {
				fs[i].Fields = mergeField(fs[i].Fields, sub)
			}
			return fs
		}
	}
	return append(fs, f)
}
