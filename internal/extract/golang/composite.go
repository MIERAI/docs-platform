package golang

import (
	"go/ast"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

func typeName(e ast.Expr) (pkg, name string) {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name, t.Sel.Name
		}
	case *ast.Ident:
		return "", t.Name
	}
	return "", ""
}

func (v *visitor) fields(c *ast.CompositeLit) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for _, el := range c.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); ok {
			out[k.Name] = kv.Value
		}
	}
	return out
}

func (v *visitor) composite(c *ast.CompositeLit) {
	pkg, name := typeName(c.Type)
	switch {
	case pkg == "cobra" && name == "Command":
		v.cobra(c)
	case strings.HasSuffix(name, "Opts") && (pkg == "prometheus" || pkg == "promauto"):
		v.promOpts(c)
	}
}

func (v *visitor) cobra(c *ast.CompositeLit) {
	f := v.fields(c)
	use, ok := v.str(f["Use"])
	if !ok {
		return
	}
	word, _, _ := strings.Cut(strings.TrimSpace(use), " ")
	if word == "" {
		return
	}
	v.add(model.KindCLICommand, word, c.Pos(), true, model.ConfHigh)
	if a, ok := f["Aliases"].(*ast.CompositeLit); ok {
		for _, e := range a.Elts {
			if s, ok := v.str(e); ok {
				v.add(model.KindCLICommand, s, e.Pos(), false, model.ConfHigh)
			}
		}
	}
}

func (v *visitor) promOpts(c *ast.CompositeLit) {
	f := v.fields(c)
	name, ok := v.str(f["Name"])
	if !ok || name == "" {
		return
	}
	parts := []string{}
	for _, k := range []string{"Namespace", "Subsystem"} {
		if s, ok := v.str(f[k]); ok && s != "" {
			parts = append(parts, s)
		}
	}
	v.add(model.KindMetric, strings.Join(append(parts, name), "_"), c.Pos(), true, model.ConfHigh)
}

// argSwitch recognises hand-rolled sub-command dispatch: switch os.Args[1] / args[0] /
// flag.Arg(0) { case "check": ... }.
func (v *visitor) argSwitch(s *ast.SwitchStmt) {
	if v.program == "" || !isArgExpr(s.Tag) {
		return
	}
	for _, st := range s.Body.List {
		cc, ok := st.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, e := range cc.List {
			if w, ok := v.str(e); ok && w != "" && !strings.HasPrefix(w, "-") {
				v.add(model.KindCLICommand, w, e.Pos(), !isHelpWord(w), model.ConfMedium)
			}
		}
	}
}

func isHelpWord(w string) bool { return w == "help" || w == "version" }

func isArgExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.IndexExpr:
		if sel, ok := x.X.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "os" && sel.Sel.Name == "Args" {
				return true
			}
		}
		if id, ok := x.X.(*ast.Ident); ok && (id.Name == "args" || id.Name == "argv") {
			return true
		}
	case *ast.CallExpr:
		name, recv := callName(x.Fun)
		return (recv == "flag" || recv == "fs") && name == "Arg"
	}
	return false
}
