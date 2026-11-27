package ledger

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Scope kinds of a quote: the region of its file the claim depends on.
const (
	ScopeGoDecl  = "go_decl"  // the enclosing Go declaration (func, type or value spec, or an element of a literal table)
	ScopeYAMLKey = "yaml_key" // the deepest YAML key (or list item) holding the line, with its subtree
	ScopeLines   = "lines"    // the quoted line ±ScopeContext lines (any other file)
)

// ScopeContext is how many lines around the quoted line a "lines" scope covers.
const ScopeContext = 3

// QuoteScope is the part of a quoted file a claim depends on, at the quote's blob: a change
// of the file outside it does not re-open the claim (the quote is re-anchored instead). Name
// identifies the symbol or key path (empty for "lines"); Start and End are 1-based lines.
type QuoteScope struct {
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// ComputeScope is the scope of line n of a file with content src: a Go declaration for .go
// files, a YAML key path for .yaml/.yml, otherwise (or when the file does not parse) the line
// ±ScopeContext. Nil when n is not a line of src.
func ComputeScope(p, src string, n int) *QuoteScope {
	lines := splitLines(src)
	if n < 1 || n > len(lines) {
		return nil
	}
	var s *QuoteScope
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		s = goScope(src, n)
	case ".yaml", ".yml":
		s = yamlScope(src, n, len(lines))
	}
	if s == nil || s.Start > n || s.End < n || s.End > len(lines) {
		s = &QuoteScope{Kind: ScopeLines, Start: max(1, n-ScopeContext), End: min(len(lines), n+ScopeContext)}
	}
	return s
}

func splitLines(src string) []string {
	if src == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(src, "\n"), "\n")
}

// goScope is the innermost top-level declaration (or value spec, or element of a value's
// composite literal) holding line n; nil outside every declaration or when src does not parse.
func goScope(src string, n int) *QuoteScope {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	g := goScoper{fset: fset, src: src, n: n}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if g.holds(d.Doc, d) {
				return g.scope(d.Doc, d, "func "+g.recv(d)+d.Name.Name)
			}
		case *ast.GenDecl:
			if g.holds(d.Doc, d) {
				return g.genDecl(d)
			}
		}
	}
	return nil
}

type goScoper struct {
	fset *token.FileSet
	src  string
	n    int
}

func (g goScoper) start(doc *ast.CommentGroup, node ast.Node) int {
	if doc != nil {
		return g.fset.Position(doc.Pos()).Line
	}
	return g.fset.Position(node.Pos()).Line
}

func (g goScoper) holds(doc *ast.CommentGroup, node ast.Node) bool {
	return g.start(doc, node) <= g.n && g.n <= g.fset.Position(node.End()).Line
}

func (g goScoper) scope(doc *ast.CommentGroup, node ast.Node, name string) *QuoteScope {
	return &QuoteScope{Kind: ScopeGoDecl, Name: name, Start: g.start(doc, node), End: g.fset.Position(node.End()).Line}
}

// text is the source of a node with its whitespace collapsed.
func (g goScoper) text(node ast.Node) string {
	a, b := g.fset.Position(node.Pos()).Offset, g.fset.Position(node.End()).Offset
	if a < 0 || b > len(g.src) || a > b {
		return ""
	}
	return strings.Join(strings.Fields(g.src[a:b]), " ")
}

func (g goScoper) recv(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	return "(" + g.text(d.Recv.List[0].Type) + ")."
}

// genDecl narrows a declaration to the spec holding the line (and a value to the element of
// its composite literal holding it: one entry of a rules table).
func (g goScoper) genDecl(d *ast.GenDecl) *QuoteScope {
	tok := d.Tok.String()
	for _, sp := range d.Specs {
		switch sp := sp.(type) {
		case *ast.ValueSpec:
			if !g.holds(sp.Doc, sp) {
				continue
			}
			name := tok + " " + identList(sp.Names)
			if s := g.element(sp, name); s != nil {
				return s
			}
			if d.Lparen.IsValid() {
				return g.scope(sp.Doc, sp, name)
			}
			return g.scope(d.Doc, d, name)
		case *ast.TypeSpec:
			if !g.holds(sp.Doc, sp) {
				continue
			}
			if d.Lparen.IsValid() {
				return g.scope(sp.Doc, sp, tok+" "+sp.Name.Name)
			}
			return g.scope(d.Doc, d, tok+" "+sp.Name.Name)
		}
	}
	return g.scope(d.Doc, d, tok+" ("+specNames(d)+")")
}

// element is the element of a value's composite literal holding the line, when the line is
// inside one (not on the line that opens the literal).
func (g goScoper) element(sp *ast.ValueSpec, name string) *QuoteScope {
	if len(sp.Values) != 1 {
		return nil
	}
	v := sp.Values[0]
	if u, ok := v.(*ast.UnaryExpr); ok {
		v = u.X
	}
	cl, ok := v.(*ast.CompositeLit)
	if !ok || g.fset.Position(cl.Lbrace).Line == g.n {
		return nil
	}
	for i, e := range cl.Elts {
		if !g.holds(nil, e) {
			continue
		}
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			return g.scope(nil, e, name+"["+g.text(kv.Key)+"]")
		}
		return g.scope(nil, e, name+"[#"+strconv.Itoa(i)+"]")
	}
	return nil
}

func identList(ids []*ast.Ident) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Name)
	}
	return strings.Join(out, ",")
}

func specNames(d *ast.GenDecl) string {
	var out []string
	for _, sp := range d.Specs {
		switch sp := sp.(type) {
		case *ast.ValueSpec:
			out = append(out, identList(sp.Names))
		case *ast.TypeSpec:
			out = append(out, sp.Name.Name)
		case *ast.ImportSpec:
			out = append(out, sp.Path.Value)
		}
	}
	return strings.Join(out, ",")
}

// yamlScope is the deepest key (or list item) holding line n with its subtree, named by its
// key path; in a stream of several documents the path starts with the document's kind/name
// (or #index). Nil when src does not parse.
func yamlScope(src string, n, total int) *QuoteScope {
	docs, err := yamlDocs(src)
	if err != nil || len(docs) == 0 {
		return nil
	}
	for i, d := range docs {
		lo, hi := 1, total
		if i > 0 {
			lo = d.Line
		}
		if i+1 < len(docs) {
			hi = docs[i+1].Line - 1
		}
		if n < lo || n > hi {
			continue
		}
		prefix := ""
		if len(docs) > 1 {
			prefix = docID(d, i) + "/"
		}
		y := yamlScoper{lines: splitLines(src), n: n}
		return y.walk(d, prefix, lo, hi)
	}
	return nil
}

// yamlDocs are the root nodes of every document in src.
func yamlDocs(src string) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(src))
	var docs []*yaml.Node
	for {
		var d yaml.Node
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller falls back to a line scope
		}
		if d.Kind == yaml.DocumentNode && len(d.Content) == 1 {
			docs = append(docs, d.Content[0])
		}
	}
}

// docID names a document of a stream: kind/metadata.name when it has them, else #index.
func docID(d *yaml.Node, i int) string {
	kind, name := "", ""
	if v := mapValue(d, "kind"); v != nil {
		kind = v.Value
	}
	if m := mapValue(d, "metadata"); m != nil {
		if v := mapValue(m, "name"); v != nil {
			name = v.Value
		}
	}
	if kind != "" && name != "" {
		return kind + ":" + name
	}
	return "#" + strconv.Itoa(i)
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

type yamlScoper struct {
	lines []string
	n     int
}

// walk descends from node (spanning lines lo..hi, named name) to the deepest key or item
// holding the line.
func (y yamlScoper) walk(node *yaml.Node, name string, lo, hi int) *QuoteScope {
	var s *QuoteScope
	switch node.Kind {
	case yaml.MappingNode:
		s = y.walkMap(node, name, hi)
	case yaml.SequenceNode:
		s = y.walkSeq(node, name, hi)
	default: // a scalar or an alias: the node itself
	}
	if s == nil {
		s = y.scope(name, lo, hi)
	}
	return s
}

func (y yamlScoper) walkMap(node *yaml.Node, name string, hi int) *QuoteScope {
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i]
		end := hi
		if i+2 < len(node.Content) {
			end = max(k.Line, node.Content[i+2].Line-1)
		}
		if y.n < k.Line || y.n > end {
			continue
		}
		child := strings.TrimPrefix(name+"."+k.Value, ".")
		if strings.HasSuffix(name, "/") {
			child = name + k.Value
		}
		if y.n == k.Line {
			return y.scope(child, k.Line, end)
		}
		return y.walk(node.Content[i+1], child, k.Line, end)
	}
	return nil
}

func (y yamlScoper) walkSeq(node *yaml.Node, name string, hi int) *QuoteScope {
	for i, it := range node.Content {
		end := hi
		if i+1 < len(node.Content) {
			end = max(it.Line, node.Content[i+1].Line-1)
		}
		if y.n < it.Line || y.n > end {
			continue
		}
		child := name + "[" + itemID(it, i) + "]"
		if y.n == it.Line {
			return y.scope(child, it.Line, end)
		}
		return y.walk(it, child, it.Line, end)
	}
	return nil
}

// scope trims trailing blank, comment-only and document-separator lines (they belong to
// what follows).
func (y yamlScoper) scope(name string, lo, hi int) *QuoteScope {
	for hi > lo && hi > y.n {
		t := strings.TrimSpace(y.lines[hi-1])
		if t != "" && t != "---" && t != "..." && !strings.HasPrefix(t, "#") {
			break
		}
		hi--
	}
	return &QuoteScope{Kind: ScopeYAMLKey, Name: name, Start: lo, End: hi}
}

// itemID names a list item by its name key when it has one (stable when items are
// inserted before it), else by its index.
func itemID(it *yaml.Node, i int) string {
	if v := mapValue(it, "name"); v != nil && v.Kind == yaml.ScalarNode {
		return "name=" + v.Value
	}
	return "#" + strconv.Itoa(i)
}
