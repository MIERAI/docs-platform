package golang

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
)

// FactKind is the kind of a typed fact read from Go code.
type FactKind string

// Fact kinds.
const (
	FactConst FactKind = "const" // a package-level constant and its value
	FactFlag  FactKind = "flag"  // a command-line flag (flag, pflag, cobra) and its default
	FactEnv   FactKind = "env"   // an environment variable read, with its default when visible
	FactRoute FactKind = "route" // an HTTP route (net/http patterns, chi, gin, echo)
	FactField FactKind = "field" // a config struct field set in a constructor literal
)

// ValueKind is the type of a fact's value.
type ValueKind string

// Value kinds. ValNone: no value is visible (an expression the extractor does not evaluate).
const (
	ValNone     ValueKind = ""
	ValInt      ValueKind = "int"
	ValFloat    ValueKind = "float"
	ValString   ValueKind = "string"
	ValBool     ValueKind = "bool"
	ValDuration ValueKind = "duration" // Num in seconds
)

// Value is a constant value evaluated from Go source without type checking.
type Value struct {
	Kind ValueKind `json:"kind,omitempty"`
	Num  float64   `json:"num,omitempty"`
	Str  string    `json:"str,omitempty"`
	// Path and Line locate the declaration the value was read from when it is not the fact's
	// own line (a constant used as a flag default).
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
}

// IsZero reports whether the value is its type's zero value (0, "", false, 0s).
func (v Value) IsZero() bool {
	switch v.Kind {
	case ValString:
		return v.Str == ""
	case ValInt, ValFloat, ValDuration, ValBool:
		return v.Num == 0
	case ValNone:
	}
	return false
}

// Fact is one typed fact of Go code.
type Fact struct {
	Kind FactKind `json:"kind"`
	// Name: the constant, the flag name without dashes, the environment variable, the route
	// path (parameters as {}), the struct field.
	Name string `json:"name"`
	// Aliases: a flag's shorthand; a field's Type.Field and struct tag names.
	Aliases []string `json:"aliases,omitempty"`
	// Method is the HTTP method of a route ("" = any method).
	Method string `json:"method,omitempty"`
	Value  Value  `json:"value"`
	// Usage is a flag's usage string, or the doc comment of a constant or field, or a route's
	// handler expression.
	Usage    string `json:"usage,omitempty"`
	Exported bool   `json:"exported,omitempty"`
	// Anchored: a route registered with its full path (not under a router group or a
	// mounted prefix the extractor cannot see).
	Anchored bool   `json:"anchored,omitempty"`
	Pkg      string `json:"pkg"`
	Program  string `json:"program,omitempty"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
}

// Source is one Go file to read facts from.
type Source struct {
	Path string
	Src  []byte
}

type parsedFile struct {
	path, dir, pkg, program string
	file                    *ast.File
}

type constRef struct {
	pf   *parsedFile
	expr ast.Expr
	line int
}

// factReader reads typed facts from a set of files; constants resolve across the files of a
// package directory and, exported, through their package name.
type factReader struct {
	fset     *token.FileSet
	files    []*parsedFile
	consts   map[string]constRef // dir \x00 name
	byPkg    map[string][]string // package name \x00 exported name -> keys of consts
	memo     map[string]Value
	visiting map[string]bool
	facts    []Fact
	// structs are the struct types by dir \x00 type name: field -> tag names and doc.
	structs map[string]map[string]fieldInfo
	// prefixed: some router is mounted under a prefix (routes are then not anchored).
	prefixed bool
}

// ExtractFacts parses Go files (test files and testdata excluded by the caller or skipped
// here) and returns their typed facts, constants resolved across files. Files that do not
// parse are skipped: facts are evidence, a partial set is still exact.
func ExtractFacts(srcs []Source) []Fact {
	r := &factReader{
		fset: token.NewFileSet(), consts: map[string]constRef{}, byPkg: map[string][]string{},
		memo: map[string]Value{}, visiting: map[string]bool{}, structs: map[string]map[string]fieldInfo{},
	}
	for _, s := range srcs {
		if strings.HasSuffix(s.Path, "_test.go") || strings.Contains("/"+s.Path, "/testdata/") {
			continue
		}
		f, err := parser.ParseFile(r.fset, s.Path, s.Src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		pf := &parsedFile{path: s.Path, dir: path.Dir(s.Path), pkg: f.Name.Name, file: f}
		if f.Name.Name == "main" {
			pf.program = programName(s.Path)
		}
		r.files = append(r.files, pf)
		r.indexConsts(pf)
		r.indexStructs(pf)
	}
	for _, pf := range r.files {
		r.constFacts(pf)
		r.callFacts(pf)
		r.fieldFacts(pf)
	}
	if r.prefixed {
		for i := range r.facts {
			r.facts[i].Anchored = false
		}
	}
	sort.SliceStable(r.facts, func(i, j int) bool {
		if r.facts[i].Path != r.facts[j].Path {
			return r.facts[i].Path < r.facts[j].Path
		}
		return r.facts[i].Line < r.facts[j].Line
	})
	return r.facts
}

func (r *factReader) line(p token.Pos) int { return r.fset.Position(p).Line }

func (r *factReader) indexConsts(pf *parsedFile) {
	for _, d := range pf.file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue // iota continuation: no expression of its own
			}
			for i, n := range vs.Names {
				k := pf.dir + "\x00" + n.Name
				r.consts[k] = constRef{pf: pf, expr: vs.Values[i], line: r.line(n.Pos())}
				if ast.IsExported(n.Name) {
					pk := pf.pkg + "\x00" + n.Name
					r.byPkg[pk] = appendUnique(r.byPkg[pk], k)
				}
			}
		}
	}
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// constFacts records every package-level constant with an evaluable value.
func (r *factReader) constFacts(pf *parsedFile) {
	for _, d := range pf.file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			if vs, ok := s.(*ast.ValueSpec); ok && len(vs.Names) == len(vs.Values) {
				r.specFacts(pf, gd, vs)
			}
		}
	}
}

func (r *factReader) specFacts(pf *parsedFile, gd *ast.GenDecl, vs *ast.ValueSpec) {
	doc := commentText(vs.Doc, vs.Comment)
	if doc == "" && len(gd.Specs) == 1 {
		doc = commentText(gd.Doc, nil)
	}
	for _, n := range vs.Names {
		if n.Name == "_" {
			continue
		}
		v := r.resolve(pf.dir + "\x00" + n.Name)
		v.Path, v.Line = "", 0
		r.facts = append(r.facts, Fact{
			Kind: FactConst, Name: n.Name, Value: v, Usage: doc, Exported: ast.IsExported(n.Name),
			Pkg: pf.pkg, Path: pf.path, Line: r.line(n.Pos()),
		})
	}
}

func commentText(a, b *ast.CommentGroup) string {
	var parts []string
	for _, g := range []*ast.CommentGroup{a, b} {
		if g != nil {
			parts = append(parts, strings.TrimSpace(g.Text()))
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// resolve evaluates the constant with the given key (cycles and unknowns give ValNone).
func (r *factReader) resolve(key string) Value {
	if v, ok := r.memo[key]; ok {
		return v
	}
	c, ok := r.consts[key]
	if !ok || r.visiting[key] {
		return Value{}
	}
	r.visiting[key] = true
	v := r.eval(c.pf, c.expr)
	delete(r.visiting, key)
	if v.Kind != ValNone {
		v.Path, v.Line = c.pf.path, c.line
	}
	r.memo[key] = v
	return v
}

var durUnits = map[string]float64{
	"Nanosecond": 1e-9, "Microsecond": 1e-6, "Millisecond": 1e-3, "Second": 1, "Minute": 60, "Hour": 3600,
}

// eval evaluates a constant expression: literals, true/false, time units, unary minus,
// products and sums of those, and references to constants.
func (r *factReader) eval(pf *parsedFile, e ast.Expr) Value {
	switch x := e.(type) {
	case *ast.BasicLit:
		return literal(x)
	case *ast.ParenExpr:
		return r.eval(pf, x.X)
	case *ast.Ident:
		return r.ident(pf, x.Name)
	case *ast.SelectorExpr:
		return r.selector(pf, x)
	case *ast.UnaryExpr:
		v := r.eval(pf, x.X)
		if x.Op == token.SUB && isNumeric(v.Kind) {
			v.Num = -v.Num
			return v
		}
	case *ast.BinaryExpr:
		return binary(x.Op, r.eval(pf, x.X), r.eval(pf, x.Y))
	case *ast.CallExpr:
		return r.conversion(pf, x)
	}
	return Value{}
}

// conversion evaluates time.Duration(n) and simple numeric/string conversions of a constant.
func (r *factReader) conversion(pf *parsedFile, c *ast.CallExpr) Value {
	if len(c.Args) != 1 {
		return Value{}
	}
	v := r.eval(pf, c.Args[0])
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := fn.X.(*ast.Ident); ok && id.Name == "time" && fn.Sel.Name == "Duration" && v.Kind == ValInt {
			return Value{Kind: ValDuration, Num: v.Num * 1e-9}
		}
	case *ast.Ident:
		switch fn.Name {
		case "int", "int64", "int32", "uint", "uint64", "uint32":
			if v.Kind == ValInt {
				return v
			}
		case "float64", "float32":
			if isNumeric(v.Kind) && v.Kind != ValDuration {
				return Value{Kind: ValFloat, Num: v.Num}
			}
		case "string":
			if v.Kind == ValString {
				return v
			}
		}
	}
	return Value{}
}

func literal(x *ast.BasicLit) Value {
	switch x.Kind {
	case token.INT:
		n, err := strconv.ParseInt(strings.ReplaceAll(x.Value, "_", ""), 0, 64)
		if err == nil {
			return Value{Kind: ValInt, Num: float64(n)}
		}
	case token.FLOAT:
		n, err := strconv.ParseFloat(strings.ReplaceAll(x.Value, "_", ""), 64)
		if err == nil {
			return Value{Kind: ValFloat, Num: n}
		}
	case token.STRING:
		s, err := strconv.Unquote(x.Value)
		if err == nil {
			return Value{Kind: ValString, Str: s}
		}
	default:
	}
	return Value{}
}

func (r *factReader) ident(pf *parsedFile, name string) Value {
	switch name {
	case "true":
		return Value{Kind: ValBool, Num: 1}
	case "false":
		return Value{Kind: ValBool}
	}
	return r.resolve(pf.dir + "\x00" + name)
}

func (r *factReader) selector(pf *parsedFile, x *ast.SelectorExpr) Value {
	id, ok := x.X.(*ast.Ident)
	if !ok {
		return Value{}
	}
	if id.Name == "time" {
		if u, ok := durUnits[x.Sel.Name]; ok {
			return Value{Kind: ValDuration, Num: u}
		}
		return Value{}
	}
	if !ast.IsExported(x.Sel.Name) {
		return Value{}
	}
	keys := r.byPkg[id.Name+"\x00"+x.Sel.Name]
	if len(keys) != 1 || keys[0] == pf.dir+"\x00"+x.Sel.Name {
		return Value{} // unknown or ambiguous package name
	}
	return r.resolve(keys[0])
}

func isNumeric(k ValueKind) bool { return k == ValInt || k == ValFloat || k == ValDuration }

// binary combines two evaluated operands: numbers with + - * / <<, a duration times a number.
func binary(op token.Token, a, b Value) Value {
	switch {
	case a.Kind == ValString && b.Kind == ValString && op == token.ADD:
		return Value{Kind: ValString, Str: a.Str + b.Str}
	case !isNumeric(a.Kind) || !isNumeric(b.Kind):
		return Value{}
	}
	kind := numKind(a.Kind, b.Kind, op)
	if kind == ValNone {
		return Value{}
	}
	return arith(op, kind, a, b)
}

// arith applies a numeric operator; kind is the result's kind.
func arith(op token.Token, kind ValueKind, a, b Value) Value {
	switch op {
	case token.MUL:
		if a.Kind == ValDuration && b.Kind == ValDuration {
			return Value{Kind: kind, Num: a.Num * b.Num * 1e9} // time.Duration(5) * time.Second
		}
		return Value{Kind: kind, Num: a.Num * b.Num}
	case token.ADD:
		return Value{Kind: kind, Num: a.Num + b.Num}
	case token.SUB:
		return Value{Kind: kind, Num: a.Num - b.Num}
	case token.QUO:
		if b.Num == 0 || kind == ValInt && int64(a.Num)%int64(b.Num) != 0 {
			return Value{}
		}
		return Value{Kind: kind, Num: a.Num / b.Num}
	case token.SHL:
		if kind == ValInt && b.Num >= 0 && b.Num < 63 {
			return Value{Kind: ValInt, Num: float64(int64(a.Num) << int64(b.Num))}
		}
	default:
	}
	return Value{}
}

// numKind is the kind of a numeric operation's result (ValNone when it makes no sense).
func numKind(a, b ValueKind, op token.Token) ValueKind {
	switch {
	case a == ValDuration && b == ValDuration:
		if op == token.ADD || op == token.SUB || op == token.MUL {
			return ValDuration
		}
		return ValNone
	case a == ValDuration || b == ValDuration:
		if op == token.MUL || op == token.QUO && a == ValDuration {
			return ValDuration
		}
		return ValNone
	case a == ValFloat || b == ValFloat:
		return ValFloat
	}
	return ValInt
}
