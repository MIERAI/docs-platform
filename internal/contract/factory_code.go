package contract

import (
	"context"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// factoryMachinery reports the parameters the generator adds (clock, cursor, page size).
func factoryMachinery(name string) bool {
	return name == "factory_now" || name == "row_limit" || strings.HasPrefix(name, "cursor_")
}

var reQueryName = regexp.MustCompile(`(?m)^\s*--\s*name:\s*`)

// FactoryCode extracts the implementation surface of the escape hatches: the SQL query files
// (db/queries/*.sql, one operation per "-- name:" block) and the compute functions
// (internal/handler/**/*_logic.go). Types of selected columns come from the schema SQL.
func FactoryCode(ctx context.Context, src *Source) (*Surface, error) {
	s := &Surface{Side: SideCode, Adapter: AdapterInfo{Name: "factory-code", Version: FactoryVersion}, Source: src.Info()}
	c := codeCollector{schemas: map[string]tableTypes{}, byName: map[string]*Operation{}}
	for _, f := range src.Files() {
		var ops []Operation
		var err error
		switch {
		case strings.HasSuffix(f, ".sql") && strings.Contains("/"+f, "/db/queries/"):
			ops, err = queryOps(ctx, src, f, c.schema(ctx, src, f))
		case strings.HasSuffix(f, "_logic.go") && strings.Contains("/"+f, "/internal/handler/"):
			var b []byte
			if b, err = src.Read(ctx, f); err == nil {
				ops, err = logicOps(f, b)
			}
		default:
			continue
		}
		if err != nil {
			s.Gaps = append(s.Gaps, f)
		}
		for _, op := range ops {
			c.add(op)
		}
	}
	for _, n := range c.order {
		s.Operations = append(s.Operations, *c.byName[n])
	}
	s.Normalize()
	return s, nil
}

// codeCollector joins the query and the compute function of each API.
type codeCollector struct {
	schemas map[string]tableTypes
	byName  map[string]*Operation
	order   []string
}

// schema loads the schema SQL of the project holding a query file (once per project).
func (c *codeCollector) schema(ctx context.Context, src *Source, f string) tableTypes {
	root := strings.TrimSuffix(f[:strings.LastIndex("/"+f, "/db/queries/")], "/")
	if c.schemas[root] == nil {
		c.schemas[root] = tableTypes{}
		loadSchema(ctx, src, root, c.schemas[root])
	}
	return c.schemas[root]
}

func (c *codeCollector) add(op Operation) {
	if prev := c.byName[op.Name]; prev != nil {
		mergeOp(prev, op)
		return
	}
	o := op
	c.byName[op.Name] = &o
	c.order = append(c.order, op.Name)
}

// mergeOp joins the query and the compute function of one API.
func mergeOp(dst *Operation, src Operation) {
	if src.Kind == "compute" {
		if dst.Kind != "compute" {
			dst.Rules = append(dst.Rules, selectsOf(*dst)...)
		}
		dst.Kind = "compute"
		dst.Outputs = src.Outputs
		dst.Path, dst.Line = src.Path, src.Line
	} else if dst.Kind == "compute" {
		dst.Rules = append(dst.Rules, selectsOf(src)...)
	}
	for _, in := range src.Inputs {
		if _, ok := field(dst.Inputs, in.Name); !ok {
			dst.Inputs = append(dst.Inputs, in)
		}
	}
	dst.Rules = append(dst.Rules, src.Rules...)
	dst.Files = append(dst.Files, src.Files...)
	dst.Partial = nil
}

// selectsOf turns a query's outputs into "selects" rules (a compute API's query feeds its
// function, the API's outputs come from the function).
func selectsOf(op Operation) []Rule {
	var rs []Rule
	for _, o := range op.Outputs {
		rs = append(rs, Rule{Kind: RuleSelects, Subject: o.Name})
	}
	return rs
}

func queryOps(ctx context.Context, src *Source, f string, schema tableTypes) ([]Operation, error) {
	b, err := src.Read(ctx, f)
	if err != nil {
		return nil, err
	}
	text := string(b)
	idx := reQueryName.FindAllStringIndex(text, -1)
	var ops []Operation
	for i, loc := range idx {
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		q := parseSQLQuery(text[loc[0]:end], factoryMachinery)
		if q.Name == "" {
			continue
		}
		op := Operation{Name: q.Name, Kind: "query", Group: queryGroup(f), Path: f, Line: lineOf(text, loc[0]), Files: []string{f}}
		applySQL(&op, q, schema, "")
		ops = append(ops, op)
	}
	return ops, nil
}

// queryGroup: "order.list-stuck-orders.sql" → "order".
func queryGroup(f string) string {
	b := path.Base(f)
	if i := strings.IndexByte(b, '.'); i > 0 && i < len(b)-4 {
		return b[:i]
	}
	return path.Base(path.Dir(path.Dir(path.Dir(f))))
}

func lineOf(text string, off int) int { return strings.Count(text[:off], "\n") + 1 }

// applySQL fills an operation from a parsed query. kind "compute" puts the SELECT list into
// "selects" rules instead of outputs.
func applySQL(op *Operation, q sqlQuery, schema tableTypes, kind string) {
	op.Inputs = append(op.Inputs, sqlInputs(q)...)
	for _, t := range q.Tables {
		op.Rules = append(op.Rules, Rule{Kind: RuleTable, Subject: t})
	}
	op.Rules = append(op.Rules, sqlRules(q)...)
	main := ""
	if len(q.Tables) > 0 {
		main = q.Tables[0]
	}
	for _, o := range q.Outputs {
		if kind == "compute" {
			op.Rules = append(op.Rules, Rule{Kind: RuleSelects, Subject: o.Name})
			continue
		}
		typ := sqlType(o.Cast)
		if typ == "" && o.Column != "" {
			typ = columnType(schema, q.Tables, main, o.Column)
		}
		op.Outputs = append(op.Outputs, Field{Name: o.Name, Column: o.Column, Type: typ})
	}
	if q.Wildcard {
		op.Partial = append(op.Partial, "outputs")
	}
}

// sqlInputs: one input per parameter, with the filters it drives.
func sqlInputs(q sqlQuery) []Field {
	filters := map[string][]string{}
	for _, c := range q.Filters {
		filters[c.Params[0]] = append(filters[c.Params[0]], c.Column+" "+c.Op)
	}
	var out []Field
	for _, name := range sortedKeys(q.Params) {
		p := q.Params[name]
		if group, _, ok := strings.Cut(name, "__"); ok {
			// a list of groups is bound as one parallel array per column
			if _, seen := field(out, group); !seen {
				out = append(out, Field{Name: group})
			}
			continue
		}
		sort.Strings(filters[name])
		f := Field{Name: name, Type: sqlType(p.Type), Presence: Required, Filter: strings.Join(filters[name], ",")}
		if p.Optional {
			f.Presence = Optional
		}
		if strings.HasPrefix(f.Type, "list<") || f.Type == "timestamp" {
			// the generator binds an empty list, and a nullable timestamp, whatever the design says
			f.Presence = ""
		}
		out = append(out, f)
	}
	return out
}

// columnType finds a column's type in the query's tables (the first table that has it).
func columnType(schema tableTypes, tables []string, main, col string) string {
	if t := schema[main][col]; t != "" {
		return t
	}
	for _, tb := range tables {
		if t := schema[tb][col]; t != "" {
			return t
		}
	}
	return ""
}

func sqlRules(q sqlQuery) []Rule {
	var rs []Rule
	for _, c := range q.Derived {
		rs = append(rs, Rule{Kind: RuleFilter, Subject: c.Column, Value: c.Op})
	}
	for _, c := range q.Fixed {
		if strings.HasPrefix(c.Column, "_") || strings.EqualFold(c.Value, "NULL") {
			continue // the generator's own bookkeeping columns
		}
		rs = append(rs, Rule{Kind: RuleFixed, Subject: c.Column, Value: c.Op + " " + c.Value})
	}
	for i, o := range q.Order {
		rs = append(rs, Rule{Kind: RuleOrder, Subject: strconv.Itoa(i + 1), Value: o})
	}
	if _, err := strconv.Atoi(q.Limit); err == nil {
		rs = append(rs, Rule{Kind: RuleLimit, Subject: "rows", Value: q.Limit})
	}
	return rs
}
