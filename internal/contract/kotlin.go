package contract

import (
	"context"
	"path"
	"strings"
)

// ktTok is a Kotlin token; strings keep their raw content (templates are not evaluated).
type ktTok struct {
	kind byte // 'n' name, 's' string, 'p' punctuation, '1' number
	text string
	line int
}

// lexKotlin tokenizes Kotlin source: comments (nested block comments too) are dropped,
// strings (plain and raw, with ${...} templates) become one token.
func lexKotlin(src string) []ktTok {
	var toks []ktTok
	line := 1
	for i := 0; i < len(src); {
		t, n := ktNext(src, i)
		if t.kind != 0 {
			t.line = line
			toks = append(toks, t)
		}
		line += strings.Count(src[i:n], "\n")
		i = n
	}
	return toks
}

// ktNext reads the token at i (kind 0 = whitespace or comment) and returns the next index.
func ktNext(src string, i int) (ktTok, int) {
	c := src[i]
	switch {
	case isSpace(c):
		return ktTok{}, i + 1
	case strings.HasPrefix(src[i:], "//"):
		return ktTok{}, skipLine(src, i)
	case strings.HasPrefix(src[i:], "/*"):
		return ktTok{}, skipNestedComment(src, i)
	case c == '"':
		return ktStringTok(src, i)
	case c == '\'':
		return ktTok{kind: '1', text: "c"}, ktChar(src, i)
	case c == '`':
		n := closeAfter(src, i+1, "`")
		return ktTok{kind: 'n', text: strings.Trim(src[i:n], "`")}, n
	case isIdentStart(c) && c < 0x80:
		n := scanWhile(src, i, func(b byte) bool { return isIdentPart(b) && b != '$' })
		return ktTok{kind: 'n', text: src[i:n]}, n
	case isDigit(c):
		n := scanWhile(src, i, func(b byte) bool { return (isIdentPart(b) || b == '.') && b != '$' })
		return ktTok{kind: '1', text: src[i:n]}, n
	}
	return ktTok{kind: 'p', text: src[i : i+1]}, i + 1
}

// ktStringTok reads a raw """...""" or a plain "..." string.
func ktStringTok(src string, i int) (ktTok, int) {
	if strings.HasPrefix(src[i:], `"""`) {
		n := closeAfter(src, i+3, `"""`)
		return ktTok{kind: 's', text: src[i+3 : max(n-3, i+3)]}, n
	}
	n := ktString(src, i)
	return ktTok{kind: 's', text: src[i+1 : max(n-1, i+1)]}, n
}

// closeAfter returns the index after the first closing delimiter at or after i (or the end).
func closeAfter(src string, i int, delim string) int {
	if j := strings.Index(src[i:], delim); j >= 0 {
		return i + j + len(delim)
	}
	return len(src)
}

// ktChar returns the index after a character literal starting at i.
func ktChar(src string, i int) int {
	n := i + 1
	for n < len(src) && src[n] != '\'' && src[n] != '\n' {
		if src[n] == '\\' {
			n++
		}
		n++
	}
	return min(n+1, len(src))
}

func skipNestedComment(src string, i int) int {
	depth := 0
	for i < len(src) {
		switch {
		case strings.HasPrefix(src[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(src[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(src)
}

// ktString returns the index after a "..." string starting at i (templates ${...} may hold
// nested strings and braces).
func ktString(src string, i int) int {
	n := i + 1
	for n < len(src) && src[n] != '"' && src[n] != '\n' {
		switch {
		case src[n] == '\\':
			n += 2
		case strings.HasPrefix(src[n:], "${"):
			depth := 0
			for n < len(src) {
				if src[n] == '{' {
					depth++
				} else if src[n] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
				n++
			}
			n++
		default:
			n++
		}
	}
	return min(n+1, len(src))
}

// ktAnnotation is "@Name" or "@Name(args)"; Value is the first string argument.
type ktAnnotation struct{ Name, Value string }

// ktParam is a function or constructor parameter, or a property.
type ktParam struct {
	Name, Type  string
	Nullable    bool
	HasDefault  bool
	Private     bool
	Prop        bool // a constructor parameter declaring a property (val / var)
	Annotations []ktAnnotation
}

// ktFun is a function declaration.
type ktFun struct {
	Name        string
	Annotations []ktAnnotation
	Params      []ktParam
	Return      string
	Nullable    bool
	Line        int
	Class       string
}

// ktClass is a class declaration with its constructor parameters and body properties.
type ktClass struct {
	Name        string
	Annotations []ktAnnotation
	Props       []ktParam
	Line        int
	File        string
}

// ktFile is what one Kotlin file declares.
type ktFile struct {
	Classes []ktClass
	Funs    []ktFun
}

var ktModifiers = map[string]bool{
	"public": true, "private": true, "internal": true, "protected": true, "open": true, "final": true,
	"abstract": true, "data": true, "override": true, "suspend": true, "inline": true, "sealed": true,
	"enum": true, "value": true, "inner": true, "lateinit": true, "const": true, "operator": true,
	"infix": true, "tailrec": true, "external": true, "annotation": true, "companion": true, "vararg": true,
	"noinline": true, "crossinline": true, "expect": true, "actual": true,
}

const maxKtSteps = 1 << 22

// parseKotlin reads class and function declarations by token patterns. It never panics.
func parseKotlin(file, src string) ktFile {
	p := &ktParser{toks: lexKotlin(src), file: file}
	p.run()
	return p.out
}

type ktParser struct {
	toks    []ktTok
	i       int
	file    string
	out     ktFile
	pending []ktAnnotation
	private bool
	// classes is a stack of (class name, brace depth at which its body opened)
	classes []ktScope
	depth   int
	steps   int
}

type ktScope struct {
	name  string
	depth int
}

func (p *ktParser) run() {
	for p.i < len(p.toks) && p.steps < maxKtSteps {
		p.steps++
		t := p.toks[p.i]
		var handled bool
		switch t.kind {
		case 'p':
			handled = p.punct(t.text)
		case 'n':
			handled = p.keyword(t.text)
		}
		if !handled {
			p.pending, p.private = nil, false
			p.i++
		}
	}
}

// punct handles "@" (consumed: true) and tracks braces (false: the token is then skipped).
func (p *ktParser) punct(text string) bool {
	switch text {
	case "@":
		p.annotation()
		return true
	case "{":
		p.depth++
	case "}":
		p.depth--
		if n := len(p.classes); n > 0 && p.classes[n-1].depth == p.depth {
			p.classes = p.classes[:n-1]
		}
	}
	return false
}

// keyword handles modifiers and declarations; it reports whether it consumed tokens.
func (p *ktParser) keyword(text string) bool {
	switch {
	case ktModifiers[text]:
		p.private = p.private || text == "private"
		p.i++
	case text == "class" || text == "interface" || text == "object":
		p.class()
	case text == "fun":
		p.fun()
	case text == "val" || text == "var":
		p.property()
	default:
		return false
	}
	return true
}

// annotation reads @Name, @a.b.Name, @Name(...), @field:Name(...).
func (p *ktParser) annotation() {
	p.i++
	var name string
	for p.i < len(p.toks) && p.toks[p.i].kind == 'n' {
		name = p.toks[p.i].text
		p.i++
		if p.i < len(p.toks) && (p.toks[p.i].text == "." || p.toks[p.i].text == ":") {
			p.i++
			continue
		}
		break
	}
	a := ktAnnotation{Name: name}
	if p.i < len(p.toks) && p.toks[p.i].text == "(" {
		end := p.matching(p.i, "(", ")")
		for k := p.i + 1; k < end; k++ {
			if p.toks[k].kind == 's' {
				a.Value = p.toks[k].text
				break
			}
		}
		p.i = end + 1
	}
	p.pending = append(p.pending, a)
}

// matching returns the index of the token closing the one at i.
func (p *ktParser) matching(i int, open, closing string) int {
	d := 0
	for k := i; k < len(p.toks); k++ {
		switch p.toks[k].text {
		case open:
			d++
		case closing:
			d--
			if d == 0 {
				return k
			}
		}
	}
	return len(p.toks) - 1
}

func (p *ktParser) class() {
	kw := p.toks[p.i].text
	p.i++
	if p.i >= len(p.toks) || p.toks[p.i].kind != 'n' {
		if kw == "object" {
			p.pending, p.private = nil, false // object expression or unnamed companion
		}
		return
	}
	c := ktClass{Name: p.toks[p.i].text, Annotations: p.pending, Line: p.toks[p.i].line, File: p.file}
	p.pending, p.private = nil, false
	p.i++
	p.skipClassHeader()
	if p.at("(") {
		end := p.matching(p.i, "(", ")")
		for _, prm := range p.params(p.i+1, end) {
			if prm.Prop && !prm.Private {
				c.Props = append(c.Props, prm)
			}
		}
		p.i = end + 1
	}
	p.out.Classes = append(p.out.Classes, c)
	if p.bodyFollows() {
		p.classes = append(p.classes, ktScope{name: c.Name, depth: p.depth})
	}
}

func (p *ktParser) at(text string) bool { return p.i < len(p.toks) && p.toks[p.i].text == text }

// skipClassHeader skips type parameters, constructor modifiers and annotations.
func (p *ktParser) skipClassHeader() {
	if p.at("<") {
		p.i = p.matching(p.i, "<", ">") + 1
	}
	for p.i < len(p.toks) && p.toks[p.i].kind == 'n' && (ktModifiers[p.toks[p.i].text] || p.toks[p.i].text == "constructor") {
		p.i++
	}
	for p.at("@") {
		p.annotation()
	}
	p.pending = nil
}

var ktDeclKeywords = map[string]bool{"class": true, "fun": true, "val": true, "var": true, "object": true, "interface": true}

// bodyFollows: the next "{" comes before another declaration starts.
func (p *ktParser) bodyFollows() bool {
	for k := p.i; k < len(p.toks); k++ {
		t := p.toks[k]
		switch {
		case t.text == "{":
			return true
		case t.kind == 'n' && ktDeclKeywords[t.text], t.text == "}", t.text == "@":
			return false
		}
	}
	return false
}

// params reads the parameters between toks[start:end].
func (p *ktParser) params(start, end int) []ktParam {
	var out []ktParam
	k := start
	for k < end {
		e := p.paramEnd(k, end)
		if prm, ok := p.param(k, e); ok {
			out = append(out, prm)
		}
		k = e + 1
	}
	return out
}

// paramEnd finds the comma ending a parameter (generics and parentheses aware).
func (p *ktParser) paramEnd(k, end int) int {
	d := 0
	for ; k < end; k++ {
		switch p.toks[k].text {
		case "(", "<", "[", "{":
			d++
		case ")", ">", "]", "}":
			d--
		case ",":
			if d <= 0 {
				return k
			}
		}
		if p.toks[k].text == ">" && k > 0 && p.toks[k-1].text == "-" {
			d++ // "->" of a function type is not a closing angle bracket
		}
	}
	return end
}

func (p *ktParser) param(k, e int) (ktParam, bool) {
	var prm ktParam
	for k < e {
		t := p.toks[k]
		switch {
		case t.text == "@":
			save := p.i
			p.i = k
			p.pending = nil
			p.annotation()
			prm.Annotations = append(prm.Annotations, p.pending...)
			p.pending = nil
			k, p.i = p.i, save
			continue
		case t.kind == 'n' && (t.text == "val" || t.text == "var"):
			prm.Prop = true
		case t.kind == 'n' && t.text == "private":
			prm.Private = true
		case t.kind == 'n' && ktModifiers[t.text]:
		case t.kind == 'n' && k+1 < e && p.toks[k+1].text == ":":
			prm.Name = t.text
			prm.Type, prm.Nullable, prm.HasDefault = p.typeAt(k+2, e)
			return prm, true
		}
		k++
	}
	return prm, false
}

// typeAt reads a type up to "=" (default value) or the end of the parameter.
func (p *ktParser) typeAt(k, e int) (string, bool, bool) {
	var b strings.Builder
	d := 0
	for ; k < e; k++ {
		t := p.toks[k].text
		if t == "=" && d == 0 {
			typ := b.String()
			return strings.TrimSuffix(typ, "?"), strings.HasSuffix(typ, "?"), true
		}
		if t == "<" || t == "(" {
			d++
		}
		if t == ">" || t == ")" {
			d--
		}
		b.WriteString(t)
	}
	typ := b.String()
	return strings.TrimSuffix(typ, "?"), strings.HasSuffix(typ, "?"), false
}

// fun reads "fun [<T>] [Recv.]name(params)[: Type]".
func (p *ktParser) fun() {
	ann := p.pending
	p.pending, p.private = nil, false
	p.i++
	if p.at("<") {
		p.i = p.matching(p.i, "<", ">") + 1
	}
	name, line := p.qualifiedName()
	if name == "" || !p.at("(") {
		return
	}
	end := p.matching(p.i, "(", ")")
	f := ktFun{Name: name, Annotations: ann, Params: p.params(p.i+1, end), Line: line, Return: "Unit"}
	if n := len(p.classes); n > 0 {
		f.Class = p.classes[n-1].name
	}
	p.i = end + 1
	if p.at(":") {
		stop := p.returnTypeEnd(p.i + 1)
		f.Return, f.Nullable, _ = p.typeAt(p.i+1, stop)
		p.i = stop
	}
	p.out.Funs = append(p.out.Funs, f)
}

// qualifiedName reads "name" or "Receiver.name" and returns the last part.
func (p *ktParser) qualifiedName() (string, int) {
	name, line := "", 0
	for p.i < len(p.toks) && p.toks[p.i].kind == 'n' {
		name, line = p.toks[p.i].text, p.toks[p.i].line
		p.i++
		if !p.at(".") {
			break
		}
		p.i++
	}
	return name, line
}

var ktReturnStop = map[string]bool{"where": true, "fun": true, "val": true, "var": true, "class": true}

// returnTypeEnd finds where a return type ends: a body, "=", a new declaration.
func (p *ktParser) returnTypeEnd(k int) int {
	d := 0
	for ; k < len(p.toks); k++ {
		t := p.toks[k]
		stop := t.text == "{" || t.text == "=" || t.text == "@" || t.text == "}" || (t.kind == 'n' && ktReturnStop[t.text])
		if d == 0 && stop {
			return k
		}
		switch t.text {
		case "<", "(":
			d++
		case ">", ")":
			d--
		}
	}
	return k
}

// property reads a class-body "val|var name: Type" (only directly inside a class body).
func (p *ktParser) property() {
	ann, private := p.pending, p.private
	p.pending, p.private = nil, false
	p.i++
	if p.i+2 >= len(p.toks) || p.toks[p.i].kind != 'n' || p.toks[p.i+1].text != ":" {
		return
	}
	n := len(p.classes)
	if n == 0 || p.classes[n-1].depth != p.depth-1 || private {
		return
	}
	name := p.toks[p.i].text
	stop := p.i + 2
	for stop < len(p.toks) && p.toks[stop].text != "=" && p.toks[stop].text != "{" && p.toks[stop].text != "}" && p.toks[stop].line == p.toks[p.i].line {
		stop++
	}
	typ, nullable, _ := p.typeAt(p.i+2, stop)
	for k := range p.out.Classes {
		c := &p.out.Classes[len(p.out.Classes)-1-k]
		if c.Name == p.classes[n-1].name {
			c.Props = append(c.Props, ktParam{Name: name, Type: typ, Nullable: nullable, Annotations: ann})
			break
		}
	}
	p.i = stop
}

// KotlinGraphQL extracts the code surface of GraphQL code-first Kotlin (SmallRye /
// MicroProfile GraphQL): every @Query / @Mutation function of a @GraphQLApi class is an
// operation; arguments come from its parameters (@Name renames, @Source and context
// parameters are skipped), outputs from the fields of its return type's class (to a small
// depth). The group is the module directory (the one holding src/).
func KotlinGraphQL(ctx context.Context, src *Source) (*Surface, error) {
	s := &Surface{Side: SideCode, Adapter: AdapterInfo{Name: "kotlin-graphql", Version: GraphQLVersion}, Source: src.Info()}
	classes := map[string][]ktClass{}
	var files []ktFile
	for _, f := range src.Files() {
		if !strings.HasSuffix(f, ".kt") || strings.Contains(f, "/src/test/") {
			continue
		}
		b, err := src.Read(ctx, f)
		if err != nil {
			s.Gaps = append(s.Gaps, f)
			continue
		}
		kf := parseKotlin(f, string(b))
		for _, c := range kf.Classes {
			classes[c.Name] = append(classes[c.Name], c)
		}
		files = append(files, kf)
	}
	r := ktResolver{classes: classes}
	for i, kf := range files {
		api := map[string]bool{}
		for _, c := range kf.Classes {
			if hasAnn(c.Annotations, "GraphQLApi") {
				api[c.Name] = true
			}
		}
		for _, f := range kf.Funs {
			if op, ok := r.operation(f, api, files[i]); ok {
				s.Operations = append(s.Operations, op)
			}
		}
	}
	s.Normalize()
	return s, nil
}

func hasAnn(as []ktAnnotation, name string) bool {
	_, ok := ann(as, name)
	return ok
}

func ann(as []ktAnnotation, name string) (ktAnnotation, bool) {
	for _, a := range as {
		if a.Name == name {
			return a, true
		}
	}
	return ktAnnotation{}, false
}

// moduleOf: ".../<module>/src/main/kotlin/..." → module.
func moduleOf(f string) string {
	if i := strings.Index(f, "/src/"); i >= 0 {
		return path.Base(f[:i])
	}
	return path.Base(path.Dir(f))
}

type ktResolver struct {
	classes map[string][]ktClass
}

func (r ktResolver) operation(f ktFun, api map[string]bool, kf ktFile) (Operation, bool) {
	kind, name := operationKind(f)
	if !api[f.Class] || kind == "" {
		return Operation{}, false
	}
	file := ""
	if len(kf.Classes) > 0 {
		file = kf.Classes[0].File
	}
	op := Operation{Name: name, Kind: kind, Group: moduleOf(file), Path: file, Line: f.Line, Files: []string{file}}
	for _, prm := range f.Params {
		if hasAnn(prm.Annotations, "Source") || strings.Contains(prm.Type, "DataFetchingEnvironment") || strings.HasSuffix(prm.Type, "Context") {
			continue
		}
		fd, files := r.field(prm, file, 0, map[string]bool{})
		if prm.HasDefault || hasAnn(prm.Annotations, "DefaultValue") {
			fd.Presence = Optional // a default makes the argument optional in the schema
		}
		op.Inputs = append(op.Inputs, fd)
		op.Files = append(op.Files, files...)
	}
	ret, files := r.field(ktParam{Name: "return", Type: f.Return, Nullable: f.Nullable}, file, 0, map[string]bool{})
	op.Outputs = ret.Fields
	op.Files = append(op.Files, files...)
	if ret.Fields == nil && strings.HasPrefix(strings.Trim(ret.Type, "[]"), "obj:") {
		op.Partial = append(op.Partial, "outputs")
	}
	return op, true
}

// operationKind: query or mutation ("" = neither) and the operation name: the annotation's
// value, else @Name, else SmallRye's default from the function name.
func operationKind(f ktFun) (string, string) {
	kind, name := "", ""
	if a, ok := ann(f.Annotations, "Query"); ok {
		kind, name = "query", a.Value
	} else if a, ok := ann(f.Annotations, "Mutation"); ok {
		kind, name = "mutation", a.Value
	}
	if kind == "" {
		return "", ""
	}
	if name == "" {
		if a, ok := ann(f.Annotations, "Name"); ok {
			name = a.Value
		}
	}
	if name == "" {
		name = defaultOperationName(kind, f.Name)
	}
	return kind, name
}

// defaultOperationName is SmallRye's name of an operation without an explicit name: a query
// drops a "get" / "is" prefix, a mutation a "set" prefix (getTransportTypes → transportTypes).
func defaultOperationName(kind, fn string) string {
	prefixes := []string{"get", "is"}
	if kind == "mutation" {
		prefixes = []string{"set"}
	}
	for _, p := range prefixes {
		if len(fn) > len(p) && strings.HasPrefix(fn, p) && fn[len(p)] >= 'A' && fn[len(p)] <= 'Z' {
			return lowerFirst(fn[len(p):])
		}
	}
	return fn
}

// field converts a parameter or property; object types get their class's fields.
func (r ktResolver) field(prm ktParam, file string, depth int, seen map[string]bool) (Field, []string) {
	name := prm.Name
	if a, ok := ann(prm.Annotations, "Name"); ok && a.Value != "" {
		name = a.Value
	}
	typ := ktGraphQLType(prm.Type)
	f := Field{Name: name, Type: typ, Presence: Optional}
	if !prm.Nullable {
		f.Presence = Required
	}
	cls := strings.TrimPrefix(strings.Trim(typ, "[]"), "obj:")
	if !strings.HasPrefix(strings.Trim(typ, "[]"), "obj:") || depth >= 3 || seen[cls] {
		return f, nil
	}
	c, ok := r.class(cls, file)
	if !ok {
		return f, nil
	}
	seen = copySet(seen)
	seen[cls] = true
	files := []string{c.File}
	f.Fields = []Field{}
	for _, p := range c.Props {
		if hasAnn(p.Annotations, "Ignore") || p.Private {
			continue
		}
		sub, more := r.field(p, c.File, depth+1, seen)
		f.Fields = append(f.Fields, sub)
		files = append(files, more...)
	}
	return f, files
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// class finds a class by simple name: same file first, then same module, then any.
func (r ktResolver) class(name, file string) (ktClass, bool) {
	cs := r.classes[name]
	if len(cs) == 0 {
		return ktClass{}, false
	}
	for _, c := range cs {
		if c.File == file {
			return c, true
		}
	}
	for _, c := range cs {
		if moduleOf(c.File) == moduleOf(file) {
			return c, true
		}
	}
	return cs[0], true
}

// ktScalars maps Kotlin / Java types to GraphQL scalars as SmallRye does.
var ktScalars = map[string]string{
	"Int": "Int", "Integer": "Int", "Short": "Int", "Byte": "Int",
	"Long": "BigInteger", "BigInteger": "BigInteger",
	"String": "String", "Char": "String", "UUID": "String", "CharSequence": "String",
	"Boolean": "Boolean",
	"Double":  "Float", "Float": "Float", "BigDecimal": "BigDecimal",
	"LocalDate": "Date", "LocalDateTime": "DateTime", "OffsetDateTime": "DateTime", "ZonedDateTime": "DateTime",
	"Instant": "DateTime", "LocalTime": "Time", "OffsetTime": "Time", "Duration": "Duration",
	"Any": "", "Unit": "",
}

var ktWrappers = map[string]bool{"Uni": true, "CompletionStage": true, "CompletableFuture": true, "Optional": true, "Mono": true}

var ktLists = map[string]bool{
	"List": true, "MutableList": true, "Set": true, "MutableSet": true, "Collection": true,
	"Iterable": true, "Array": true, "Sequence": true, "Multi": true, "ArrayList": true, "Flux": true,
}

// ktGraphQLType converts a Kotlin type ("List<MtItemDto?>") into the GraphQL notation of the
// contract ("[obj:MtItemDto]"); nullability is the field's presence, not part of the type.
func ktGraphQLType(t string) string {
	t = strings.TrimSuffix(strings.TrimSpace(t), "?")
	if t == "" {
		return ""
	}
	base, arg, generic := strings.Cut(t, "<")
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		base = base[i+1:]
	}
	if generic {
		arg = strings.TrimSuffix(arg, ">")
		if j := strings.LastIndexByte(arg, ','); j >= 0 && !strings.Contains(arg[:j], "<") {
			arg = arg[j+1:] // Map<K, V>: the value
		}
		switch {
		case ktLists[base]:
			return "[" + ktGraphQLType(arg) + "]"
		case ktWrappers[base]:
			return ktGraphQLType(arg)
		}
	}
	switch base {
	case "IntArray", "ShortArray":
		return "[Int]"
	case "LongArray":
		return "[BigInteger]"
	}
	if s, ok := ktScalars[base]; ok {
		return s
	}
	return "obj:" + base
}
