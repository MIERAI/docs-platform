package golang

import (
	"go/ast"
	"go/token"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

type fieldInfo struct {
	tags []string
	doc  string
}

// tagKeys are the struct tags whose names a document may use for a field.
var tagKeys = []string{"yaml", "json", "toml", "mapstructure", "koanf", "env", "envconfig", "flag"}

// reConstructor: functions whose struct literals are defaults (NewConfig, DefaultOptions, defaults).
var reConstructor = regexp.MustCompile(`^(?:[Nn]ew|[Dd]efault)`)

// indexStructs records the struct types of a file with their fields' tag names and doc.
func (r *factReader) indexStructs(pf *parsedFile) {
	for _, d := range pf.file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			ts, ok := s.(*ast.TypeSpec)
			st, ok2 := ts.Type.(*ast.StructType)
			if !ok || !ok2 {
				continue
			}
			fields := map[string]fieldInfo{}
			for _, f := range st.Fields.List {
				fi := fieldInfo{tags: tagNames(f.Tag), doc: commentText(f.Doc, f.Comment)}
				for _, n := range f.Names {
					fields[n.Name] = fi
				}
			}
			r.structs[pf.dir+"\x00"+ts.Name.Name] = fields
		}
	}
}

func tagNames(t *ast.BasicLit) []string {
	if t == nil {
		return nil
	}
	raw, err := strconv.Unquote(t.Value)
	if err != nil {
		return nil
	}
	var out []string
	for _, k := range tagKeys {
		name, _, _ := strings.Cut(reflect.StructTag(raw).Get(k), ",")
		if name != "" && name != "-" {
			out = appendUnique(out, name)
		}
	}
	return out
}

// fieldFacts records the fields set by struct literals inside constructor functions.
func (r *factReader) fieldFacts(pf *parsedFile) {
	for _, d := range pf.file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !reConstructor.MatchString(fd.Name.Name) {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if cl, ok := n.(*ast.CompositeLit); ok {
				r.literalFields(pf, cl)
			}
			return true
		})
	}
}

func (r *factReader) literalFields(pf *parsedFile, cl *ast.CompositeLit) {
	id, ok := cl.Type.(*ast.Ident)
	if !ok {
		return
	}
	fields, ok := r.structs[pf.dir+"\x00"+id.Name]
	if !ok {
		return
	}
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fi, known := fields[k.Name]
		if !known {
			continue
		}
		v := r.eval(pf, kv.Value)
		if v.Path == pf.path && v.Line == r.line(kv.Pos()) {
			v.Path, v.Line = "", 0
		}
		r.facts = append(r.facts, Fact{
			Kind: FactField, Name: k.Name, Aliases: append([]string{id.Name + "." + k.Name}, fi.tags...),
			Value: v, Usage: fi.doc, Exported: ast.IsExported(k.Name), Pkg: pf.pkg, Program: pf.program,
			Path: pf.path, Line: r.line(kv.Pos()),
		})
	}
}
