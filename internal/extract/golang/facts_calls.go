package golang

import (
	"go/ast"
	"go/token"
	"regexp"
	"strings"
)

// flagSpec locates the arguments of a typed flag definition.
type flagSpec struct {
	name, short, def, usage, args int
	kind                          ValueKind
}

// flagSpecs maps typed flag-definition methods (flag, pflag, cobra) to their arguments.
var flagSpecs = func() map[string]flagSpec {
	kinds := map[string]ValueKind{
		"String": ValString, "Bool": ValBool, "Duration": ValDuration, "Float64": ValFloat, "Float32": ValFloat,
		"Int": ValInt, "Int64": ValInt, "Int32": ValInt, "Int16": ValInt, "Int8": ValInt,
		"Uint": ValInt, "Uint64": ValInt, "Uint32": ValInt, "Uint16": ValInt, "Uint8": ValInt,
	}
	m := map[string]flagSpec{}
	for t, k := range kinds {
		m[t] = flagSpec{name: 0, short: -1, def: 1, usage: 2, args: 3, kind: k}
		m[t+"P"] = flagSpec{name: 0, short: 1, def: 2, usage: 3, args: 4, kind: k}
		m[t+"Var"] = flagSpec{name: 1, short: -1, def: 2, usage: 3, args: 4, kind: k}
		m[t+"VarP"] = flagSpec{name: 1, short: 2, def: 3, usage: 4, args: 5, kind: k}
	}
	return m
}()

var (
	httpMethods = map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
		"CONNECT": true, "TRACE": true,
	}
	reRouteParam = regexp.MustCompile(`\{[^}]*\}|:[A-Za-z_]\w*|\*[A-Za-z_]\w*`)
	// prefixCalls mount a router under a prefix the extractor does not follow.
	prefixCalls = map[string]bool{"Mount": true, "StripPrefix": true, "PathPrefix": true, "Subrouter": true}
	groupCalls  = map[string]bool{"Group": true, "Route": true}
)

// NormalizeRoute writes route parameters ({id}, :id, *rest) as {} so that spellings compare.
func NormalizeRoute(p string) string { return reRouteParam.ReplaceAllString(p, "{}") }

type callScan struct {
	r  *factReader
	pf *parsedFile
	// envDefaults are defaults seen around os.Getenv calls, by the call's position.
	envDefaults map[token.Pos]Value
	// grouped are identifiers assigned from Group/Route calls; inGroup are the bodies of
	// function literals passed to them.
	grouped map[string]bool
	inGroup [][2]token.Pos
}

func (r *factReader) callFacts(pf *parsedFile) {
	c := &callScan{r: r, pf: pf, envDefaults: map[token.Pos]Value{}, grouped: map[string]bool{}}
	ast.Inspect(pf.file, c.prescan)
	ast.Inspect(pf.file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			c.call(call)
		}
		return true
	})
}

// prescan finds env defaults (if v == "" { v = x }, cmp.Or) and router groups.
func (c *callScan) prescan(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.BlockStmt:
		for i := 0; i+1 < len(x.List); i++ {
			c.envIfDefault(x.List[i], x.List[i+1])
		}
	case *ast.CallExpr:
		c.prescanCall(x)
	case *ast.AssignStmt:
		c.prescanAssign(x)
	}
	return true
}

// prescanAssign marks `api := r.Group("/api")`: routes on api are not anchored.
func (c *callScan) prescanAssign(x *ast.AssignStmt) {
	if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
		return
	}
	call, ok := x.Rhs[0].(*ast.CallExpr)
	id, ok2 := x.Lhs[0].(*ast.Ident)
	if !ok || !ok2 {
		return
	}
	if name, _ := callName(call.Fun); groupCalls[name] {
		c.grouped[id.Name] = true
	}
}

func (c *callScan) prescanCall(x *ast.CallExpr) {
	name, _ := callName(x.Fun)
	c.groupCall(name, x)
	if name == "Or" && len(x.Args) == 2 && isGetenv(x.Args[0]) {
		if v := c.r.eval(c.pf, x.Args[1]); v.Kind != ValNone {
			c.envDefaults[x.Args[0].Pos()] = v
		}
	}
	if prefixCalls[name] {
		c.r.prefixed = true
	}
}

func (c *callScan) groupCall(name string, x *ast.CallExpr) {
	if !groupCalls[name] {
		return
	}
	for _, a := range x.Args {
		if fl, ok := a.(*ast.FuncLit); ok {
			c.inGroup = append(c.inGroup, [2]token.Pos{fl.Pos(), fl.End()})
		}
	}
}

func isGetenv(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	name, recv := callName(call.Fun)
	return (recv == "os" || recv == "syscall") && (name == "Getenv" || name == "LookupEnv")
}

// envIfDefault recognises
//
//	v := os.Getenv("X")            v, ok := os.LookupEnv("X")
//	if v == "" { v = "default" }   if !ok { v = "default" }
func (c *callScan) envIfDefault(a, b ast.Stmt) {
	as, ok := a.(*ast.AssignStmt)
	ifs, ok2 := b.(*ast.IfStmt)
	if !ok || !ok2 || len(as.Rhs) != 1 || !isGetenv(as.Rhs[0]) || ifs.Init != nil || ifs.Else != nil {
		return
	}
	v, _ := as.Lhs[0].(*ast.Ident)
	if v == nil || !emptyTest(ifs.Cond, v.Name, as) {
		return
	}
	if e := assignedTo(ifs.Body, v.Name); e != nil {
		if d := c.r.eval(c.pf, e); d.Kind != ValNone {
			c.envDefaults[as.Rhs[0].Pos()] = d
		}
	}
}

// assignedTo returns x when the block is exactly `name = x`.
func assignedTo(b *ast.BlockStmt, name string) ast.Expr {
	if len(b.List) != 1 {
		return nil
	}
	set, _ := b.List[0].(*ast.AssignStmt)
	if set == nil || len(set.Lhs) != 1 || len(set.Rhs) != 1 || set.Tok != token.ASSIGN {
		return nil
	}
	if lhs, ok := set.Lhs[0].(*ast.Ident); !ok || lhs.Name != name {
		return nil
	}
	return set.Rhs[0]
}

// emptyTest: `v == ""` (Getenv) or `!ok` (LookupEnv's second result).
func emptyTest(cond ast.Expr, v string, as *ast.AssignStmt) bool {
	switch x := cond.(type) {
	case *ast.BinaryExpr:
		id, _ := x.X.(*ast.Ident)
		lit, _ := x.Y.(*ast.BasicLit)
		return x.Op == token.EQL && id != nil && id.Name == v && lit != nil && lit.Value == `""`
	case *ast.UnaryExpr:
		id, _ := x.X.(*ast.Ident)
		if x.Op != token.NOT || id == nil || len(as.Lhs) != 2 {
			return false
		}
		ok, _ := as.Lhs[1].(*ast.Ident)
		return ok != nil && ok.Name == id.Name
	}
	return false
}

func (c *callScan) call(x *ast.CallExpr) {
	name, recv := callName(x.Fun)
	switch {
	case (recv == "os" || recv == "syscall") && (name == "Getenv" || name == "LookupEnv"):
		c.env(x, c.envDefaults[x.Pos()], true)
	case reEnvFunc.MatchString(name) && recv != "os" && recv != "syscall" && !reEnvWrite.MatchString(name):
		c.envHelper(x)
	case name == "HandleFunc" || name == "Handle":
		c.httpRoute(x)
	case name == "Method" || name == "MethodFunc":
		c.methodRoute(x)
	default:
		c.methodCall(name, recv, x)
	}
	if spec, ok := flagSpecs[name]; ok {
		c.flag(x, spec)
	}
}

func (c *callScan) str(e ast.Expr) (string, bool) {
	v := c.r.eval(c.pf, e)
	return v.Str, v.Kind == ValString
}

func (c *callScan) env(x *ast.CallExpr, def Value, ok bool) {
	if len(x.Args) == 0 || !ok {
		return
	}
	s, isStr := c.str(x.Args[0])
	if !isStr || !reEnvName.MatchString(s) {
		return
	}
	c.r.facts = append(c.r.facts, Fact{
		Kind: FactEnv, Name: s, Value: def, Exported: true, Pkg: c.pf.pkg, Program: c.pf.program,
		Path: c.pf.path, Line: c.r.line(x.Pos()),
	})
}

// envHelper: getenv("X", "default") style helpers; a helper with more or fewer arguments
// reads the variable with no visible default.
func (c *callScan) envHelper(x *ast.CallExpr) {
	var def Value
	if len(x.Args) == 2 {
		def = c.r.eval(c.pf, x.Args[1])
	}
	c.env(x, def, len(x.Args) >= 1 && len(x.Args) <= 2)
}

func (c *callScan) flag(x *ast.CallExpr, spec flagSpec) {
	if len(x.Args) != spec.args {
		return
	}
	name, ok := c.str(x.Args[spec.name])
	usage, ok2 := c.str(x.Args[spec.usage])
	if !ok || !ok2 || !reFlagName.MatchString(name) {
		return
	}
	f := Fact{
		Kind: FactFlag, Name: name, Usage: usage, Exported: true, Pkg: c.pf.pkg, Program: c.pf.program,
		Path: c.pf.path, Line: c.r.line(x.Args[spec.name].Pos()),
	}
	if spec.short >= 0 {
		if s, ok := c.str(x.Args[spec.short]); ok && len(s) == 1 {
			f.Aliases = []string{s}
		}
	}
	v := c.r.eval(c.pf, x.Args[spec.def])
	if v.Kind == ValInt && spec.kind == ValFloat {
		v.Kind = ValFloat
	}
	if v.Kind == spec.kind {
		if v.Path == "" {
			if l := c.r.line(x.Args[spec.def].Pos()); l != f.Line {
				v.Path, v.Line = c.pf.path, l
			}
		}
		f.Value = v
	}
	c.r.facts = append(c.r.facts, f)
}

// httpRoute: mux.HandleFunc("GET /items/{id}", h), http.Handle("/x", h).
func (c *callScan) httpRoute(x *ast.CallExpr) {
	if len(x.Args) != 2 {
		return
	}
	pat, ok := c.str(x.Args[0])
	if !ok {
		return
	}
	method, p := "", pat
	if m, rest, found := strings.Cut(pat, " "); found && httpMethods[m] {
		method, p = m, strings.TrimSpace(rest)
	}
	c.route(x, method, p, x.Args[1])
}

// methodRoute: chi's r.Method("GET", "/x", h).
func (c *callScan) methodRoute(x *ast.CallExpr) {
	if len(x.Args) != 3 {
		return
	}
	m, ok := c.str(x.Args[0])
	p, ok2 := c.str(x.Args[1])
	if ok && ok2 && httpMethods[strings.ToUpper(m)] {
		c.route(x, strings.ToUpper(m), p, x.Args[2])
	}
}

// methodCall: chi's r.Get("/x", h), gin's r.GET("/x", h), echo's e.POST("/x", h).
func (c *callScan) methodCall(name, recv string, x *ast.CallExpr) {
	m := strings.ToUpper(name)
	if !httpMethods[m] || recv == "http" || len(x.Args) < 2 || name != m && name != strings.ToUpper(name[:1])+strings.ToLower(name[1:]) {
		return
	}
	switch h := x.Args[1].(type) {
	case *ast.Ident:
		if h.Name == "nil" {
			return
		}
	case *ast.SelectorExpr, *ast.FuncLit, *ast.CallExpr:
	default:
		return // not a handler (a cache Get, a map lookup)
	}
	if p, ok := c.str(x.Args[0]); ok {
		c.route(x, m, p, x.Args[1])
	}
}

func (c *callScan) route(x *ast.CallExpr, method, p string, handler ast.Expr) {
	if !strings.HasPrefix(p, "/") {
		return
	}
	f := Fact{
		Kind: FactRoute, Name: NormalizeRoute(p), Method: method, Usage: handlerName(handler), Exported: true,
		Anchored: c.anchored(x), Pkg: c.pf.pkg, Program: c.pf.program, Path: c.pf.path, Line: c.r.line(x.Pos()),
	}
	c.r.facts = append(c.r.facts, f)
}

func (c *callScan) anchored(x *ast.CallExpr) bool {
	for _, g := range c.inGroup {
		if x.Pos() >= g[0] && x.Pos() < g[1] {
			return false
		}
	}
	if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok && c.grouped[id.Name] {
			return false
		}
	}
	return true
}

func handlerName(e ast.Expr) string {
	switch h := e.(type) {
	case *ast.Ident:
		return h.Name
	case *ast.SelectorExpr:
		return h.Sel.Name
	case *ast.CallExpr:
		return handlerName(h.Fun)
	}
	return ""
}
