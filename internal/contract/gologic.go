package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strings"
)

// logicOps reads the compute functions of one *_logic.go file with go/parser: every
// top-level function named <api>Logic is an operation; its signature, the response fields
// it sets, the row fields (columns) and the request fields (inputs) it reads - in that
// function and in any helper of the file that takes the row or request type.
func logicOps(f string, src []byte) ([]Operation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err //nolint:wrapcheck // the parser's message names the file and position
	}
	var ops []Operation
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || !strings.HasSuffix(fd.Name.Name, "Logic") || fd.Name.Name == "Logic" {
			continue
		}
		api := strings.TrimSuffix(fd.Name.Name, "Logic")
		api = strings.ToUpper(api[:1]) + api[1:]
		op := Operation{Name: api, Kind: "compute", Group: path.Base(path.Dir(f)), Path: f, Line: fset.Position(fd.Pos()).Line, Files: []string{f}}
		op.Rules = append(op.Rules, Rule{Kind: RuleSignature, Subject: fd.Name.Name, Value: signature(fd.Type)})
		u := logicUse{api: api, rowType: lowerFirst(api) + "Row", reads: map[string]bool{}, inputs: map[string]bool{}, sets: map[string]bool{}}
		u.scanFile(file)
		for _, c := range sortedKeys(u.reads) {
			op.Rules = append(op.Rules, Rule{Kind: RuleReads, Subject: c})
		}
		for _, c := range sortedKeys(u.inputs) {
			op.Rules = append(op.Rules, Rule{Kind: RuleReadsInput, Subject: c})
		}
		for _, c := range sortedKeys(u.sets) {
			op.Outputs = append(op.Outputs, Field{Name: c})
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// signature renders parameter and result types, package qualifiers dropped:
// "(*XRequest, []xRow) (*XResponse, error)".
func signature(ft *ast.FuncType) string {
	return "(" + strings.Join(fieldTypes(ft.Params), ", ") + ") (" + strings.Join(fieldTypes(ft.Results), ", ") + ")"
}

func fieldTypes(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, f := range fl.List {
		n := max(len(f.Names), 1)
		for range n {
			out = append(out, typeString(f.Type))
		}
	}
	return out
}

func typeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.ArrayType:
		return "[]" + typeString(t.Elt)
	case *ast.MapType:
		return "map[" + typeString(t.Key) + "]" + typeString(t.Value)
	case *ast.Ellipsis:
		return "..." + typeString(t.Elt)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func" + signature(t)
	case *ast.IndexExpr:
		return typeString(t.X) + "[" + typeString(t.Index) + "]"
	}
	return "?"
}

// logicUse collects what the functions of a logic file read and set.
type logicUse struct {
	api, rowType         string
	reads, inputs, sets  map[string]bool
	rowVars, reqVars     map[string]bool
	respVars, rowsSlices map[string]bool
}

func (u *logicUse) scanFile(file *ast.File) {
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		u.rowVars, u.reqVars, u.respVars, u.rowsSlices = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		u.bindParams(fd.Type)
		if !u.relevant() {
			continue
		}
		ast.Inspect(fd.Body, u.visit)
	}
}

func (u *logicUse) relevant() bool {
	return len(u.rowVars)+len(u.reqVars)+len(u.respVars)+len(u.rowsSlices) > 0
}

// bindParams binds parameters by type: the request, a row, the rows, the response.
func (u *logicUse) bindParams(ft *ast.FuncType) {
	for _, fl := range []*ast.FieldList{ft.Params, ft.Results} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				u.bind(n.Name, typeString(f.Type))
			}
		}
	}
}

func (u *logicUse) bind(name, typ string) {
	switch strings.TrimPrefix(typ, "*") {
	case u.api + "Request":
		u.reqVars[name] = true
	case u.rowType:
		u.rowVars[name] = true
	case "[]" + u.rowType, "[]*" + u.rowType:
		u.rowsSlices[name] = true
	case u.api + "Response":
		u.respVars[name] = true
	}
}

func (u *logicUse) visit(n ast.Node) bool {
	switch x := n.(type) {
	case *ast.RangeStmt:
		u.rangeRows(x)
	case *ast.AssignStmt:
		u.assign(x)
	case *ast.IncDecStmt:
		u.setField(x.X)
	case *ast.CompositeLit:
		u.responseLit(x)
	case *ast.SelectorExpr:
		u.selector(x)
	}
	return true
}

// rangeRows binds the value of "for _, row := range rows".
func (u *logicUse) rangeRows(x *ast.RangeStmt) {
	if id, ok := x.X.(*ast.Ident); ok && u.rowsSlices[id.Name] {
		if v, ok := x.Value.(*ast.Ident); ok {
			u.rowVars[v.Name] = true
		}
	}
}

// responseLit records the keys of a response composite literal.
func (u *logicUse) responseLit(x *ast.CompositeLit) {
	if strings.TrimPrefix(typeString(x.Type), "*") != u.api+"Response" {
		return
	}
	for _, el := range x.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok {
				u.sets[snake(k.Name)] = true
			}
		}
	}
}

// setField records "resp.Field" written to.
func (u *logicUse) setField(e ast.Expr) {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok && u.respVars[id.Name] {
			u.sets[snake(sel.Sel.Name)] = true
		}
	}
}

func (u *logicUse) assign(x *ast.AssignStmt) {
	for i, lhs := range x.Lhs {
		u.setField(lhs)
		if id, ok := lhs.(*ast.Ident); ok && i < len(x.Rhs) {
			u.bindValue(id.Name, x.Rhs[i])
		}
	}
}

// bindValue binds a variable assigned a response / row value.
func (u *logicUse) bindValue(name string, rhs ast.Expr) {
	if ue, ok := rhs.(*ast.UnaryExpr); ok {
		rhs = ue.X
	}
	switch r := rhs.(type) {
	case *ast.CompositeLit:
		u.bind(name, typeString(r.Type))
	case *ast.IndexExpr:
		if s, ok := r.X.(*ast.Ident); ok && u.rowsSlices[s.Name] {
			u.rowVars[name] = true
		}
	case *ast.Ident:
		if u.rowVars[r.Name] {
			u.rowVars[name] = true
		}
	}
}

func (u *logicUse) selector(x *ast.SelectorExpr) {
	name := x.Sel.Name
	switch base := x.X.(type) {
	case *ast.Ident:
		switch {
		case u.rowVars[base.Name]:
			u.reads[snake(name)] = true
		case u.reqVars[base.Name]:
			u.inputs[snake(strings.TrimPrefix(name, "Get"))] = true
		}
	case *ast.IndexExpr:
		if s, ok := base.X.(*ast.Ident); ok && u.rowsSlices[s.Name] {
			u.reads[snake(name)] = true
		}
	}
}
