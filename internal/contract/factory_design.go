package contract

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FactoryVersion is the version of the factory adapters.
const FactoryVersion = "1.0.0"

// The design language's kinds (种类) whose implementation includes a hand-maintainable part:
// a query file, a compute function or a whole handler. Commands, message handlers and state
// machines are generated end to end and are not part of this contract.
var factoryQueryKinds = map[string]string{
	"按键取一条": "get_by_id", "多条": "list_all", "一览": "list_keyset", "前N条": "top_n",
	"计算": "compute", "自定义": "custom",
	"get_by_id": "get_by_id", "list_all": "list_all", "list_keyset": "list_keyset", "top_n": "top_n",
	"compute": "compute", "custom": "custom",
}

var reGeneratedHeader = regexp.MustCompile(`(?i)code generated .*do not edit`)

// factoryProject is one design root: the directory holding design/, specs/, db/.
type factoryProject struct {
	root    string
	domains map[string]*factoryDomain // design dir → domain
	schema  tableTypes
}

// factoryDomain is the domain file (领域.yaml) of one design directory.
type factoryDomain struct {
	name   string
	enums  map[string]string            // value name → database value
	json   map[string]string            // logical table → physical table (JSON versions)
	fields map[string]map[string]string // logical table → field → type id
	cols   map[string]map[string]string // logical table → field → physical column (外壳列)
}

// FactoryDesign extracts the design surface of a design-first code generator: every
// design/<domain>/<API>.yaml of a query kind, plus hand-written specs/<domain>/<api>/spec.yaml
// (those without a generated header), with types from the domain file and the schema SQL.
func FactoryDesign(ctx context.Context, src *Source) (*Surface, error) {
	s := &Surface{Side: SideDesign, Adapter: AdapterInfo{Name: "factory-design", Version: FactoryVersion}, Source: src.Info()}
	projects := map[string]*factoryProject{}
	project := func(root string) *factoryProject {
		if p := projects[root]; p != nil {
			return p
		}
		p := &factoryProject{root: root, domains: map[string]*factoryDomain{}, schema: tableTypes{}}
		loadSchema(ctx, src, root, p.schema)
		projects[root] = p
		return p
	}
	for _, f := range src.Files() {
		root, kind := factoryRole(f)
		if kind == "" {
			continue
		}
		b, err := src.Read(ctx, f)
		if err != nil {
			s.Gaps = append(s.Gaps, f)
			continue
		}
		p := project(root)
		var op *Operation
		switch kind {
		case "design":
			op, err = p.designOp(ctx, src, f, b)
		case "spec":
			op, err = p.specOp(f, b)
		}
		if err != nil {
			s.Gaps = append(s.Gaps, f)
			continue
		}
		if op != nil {
			s.Operations = append(s.Operations, *op)
		}
	}
	s.Normalize()
	return s, nil
}

// factoryRole tells whether f is a design file or a spec file, and its project root.
func factoryRole(f string) (string, string) {
	base := path.Base(f)
	if !strings.HasSuffix(base, ".yaml") {
		return "", ""
	}
	if i := strings.LastIndex(f, "/design/"); i >= 0 || strings.HasPrefix(f, "design/") {
		if strings.Contains(base, ".审查") || strings.HasSuffix(base, ".lock.yaml") || base == "领域.yaml" || base == "消息.yaml" {
			return "", ""
		}
		return f[:max(i, 0)], "design"
	}
	if base == "spec.yaml" {
		if i := strings.LastIndex(f, "/specs/"); i >= 0 {
			return f[:i], "spec"
		}
		if strings.HasPrefix(f, "specs/") {
			return "", "spec"
		}
	}
	return "", ""
}

func loadSchema(ctx context.Context, src *Source, root string, tt tableTypes) {
	prefix := path.Join(root, "db/schema") + "/"
	if root == "" {
		prefix = "db/schema/"
	}
	for _, f := range src.Files() {
		if strings.HasPrefix(f, prefix) && strings.HasSuffix(f, ".sql") {
			if b, err := src.Read(ctx, f); err == nil {
				parseSchema(string(b), tt)
			}
		}
	}
}

// domain loads the domain file next to a design file (once per directory).
func (p *factoryProject) domain(ctx context.Context, src *Source, dir string) *factoryDomain {
	if d := p.domains[dir]; d != nil {
		return d
	}
	d := &factoryDomain{enums: map[string]string{}, json: map[string]string{}, fields: map[string]map[string]string{}, cols: map[string]map[string]string{}}
	p.domains[dir] = d
	b, err := src.Read(ctx, path.Join(dir, "领域.yaml"))
	if err != nil {
		return d
	}
	var m map[string]any
	if yaml.Unmarshal(b, &m) != nil {
		return d
	}
	d.name = str(m, "领域")
	for _, e := range mapOf(m, "枚举") {
		for name, v := range mapOf(asMap(e), "值") {
			if dbv := scalar(asMap(v)["数据库值"]); dbv != "" {
				d.enums[name] = dbv
			}
		}
	}
	for t, tv := range mapOf(m, "表") {
		tm := asMap(tv)
		if raw := str(tm, "原始表"); raw != "" {
			d.json[t] = raw
		}
		for f, fv := range mapOf(tm, "字段") {
			if d.fields[t] == nil {
				d.fields[t], d.cols[t] = map[string]string{}, map[string]string{}
			}
			d.fields[t][f] = designType(str(asMap(fv), "类型"))
			if c := str(asMap(fv), "列"); c != "" {
				d.cols[t][f] = c
			}
		}
	}
	return d
}

// designOp reads design/<domain>/<API>.yaml.
func (p *factoryProject) designOp(ctx context.Context, src *Source, f string, b []byte) (*Operation, error) {
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", f, err)
	}
	name := str(m, "API")
	kind := factoryQueryKinds[str(m, "种类")]
	if name == "" || kind == "" {
		return nil, nil //nolint:nilnil // not an API of a query kind: nothing to contract
	}
	dom := p.domain(ctx, src, path.Dir(f))
	op := &Operation{Name: name, Kind: kind, Group: firstNonEmpty(dom.name, path.Base(path.Dir(f))), Path: f, Line: 1, Files: []string{f}}
	table := str(m, "数据来源")
	op.Rules = append(op.Rules, p.tableRules(m, dom, table)...)
	op.Inputs = designInputs(m)
	op.Rules = append(op.Rules, designRules(m, dom)...)
	if kind == "compute" || kind == "custom" {
		op.Outputs = designOutputs(m, nil, "", nil)
		op.Rules = append(op.Rules, computeDesignRules(m, name)...)
	} else {
		op.Outputs = designOutputs(m, p.schema, physical(dom, table), dom.fields[table])
		for i, o := range op.Outputs {
			if c := dom.cols[table][o.Column]; c != "" {
				op.Outputs[i].Column = c
			}
		}
	}
	return op, nil
}

func physical(dom *factoryDomain, table string) string {
	if raw := dom.json[table]; raw != "" {
		return raw
	}
	return table
}

func (p *factoryProject) tableRules(m map[string]any, dom *factoryDomain, table string) []Rule {
	var rs []Rule
	if table != "" {
		rs = append(rs, Rule{Kind: RuleTable, Subject: physical(dom, table)})
	}
	for _, key := range []string{"嵌套", "子聚合"} {
		for _, v := range mapOf(m, key) {
			if t := str(asMap(v), "表"); t != "" {
				rs = append(rs, Rule{Kind: RuleTable, Subject: physical(dom, t)})
			}
		}
	}
	return rs
}

// designInputs are the inputs the query binds: inputs with a filter, inputs a derived
// parameter is computed from, and every input of a compute / custom API (its function or
// handler may read them).
func designInputs(m map[string]any) []Field {
	derivedFrom := map[string]bool{}
	in := mapOf(m, "输入")
	for _, dv := range mapOf(m, "派生参数") {
		for name := range in {
			if regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(name) + `([^A-Za-z0-9_]|$)`).MatchString(str(asMap(dv), "计算")) {
				derivedFrom[name] = true
			}
		}
	}
	var out []Field
	for name, v := range in {
		vm := asMap(v)
		filter := str(vm, "筛选")
		if filter == "" && !derivedFrom[name] {
			continue
		}
		f := Field{Name: name, Type: designType(str(vm, "类型")), Presence: Optional, Filter: designFilter(filter)}
		if yes(vm["必填"]) {
			f.Presence = Required
		}
		if f.Type == "tuples" {
			f.Type, f.Presence = "", ""
		}
		out = append(out, f)
	}
	return out
}

// designOutputs: a string value means "the same-named column"; 来自 renames; 类型 overrides.
func designOutputs(m map[string]any, schema tableTypes, table string, jsonFields map[string]string) []Field {
	computed := map[string]bool{}
	for _, key := range []string{"嵌套", "子聚合"} {
		for name := range mapOf(m, key) {
			computed[name] = true
		}
	}
	var out []Field
	for name, v := range mapOf(m, "输出") {
		f := Field{Name: name, Column: name}
		if vm, ok := v.(map[string]any); ok {
			f.Column = firstNonEmpty(str(vm, "来自"), name)
			f.Type = designType(str(vm, "类型"))
			if yes(vm["可以不设置"]) {
				f.Presence = Optional
			}
		}
		if computed[name] {
			f.Column = ""
		}
		if f.Type == "" && f.Column != "" {
			f.Type = firstNonEmpty(jsonFields[f.Column], schema[table][f.Column])
		}
		if schema == nil { // compute / custom: the function decides the columns
			f.Column = ""
		}
		out = append(out, f)
	}
	return out
}

// designRules: derived-parameter filters, fixed conditions, ordering and the row limit.
func designRules(m map[string]any, dom *factoryDomain) []Rule {
	var rs []Rule
	for _, dv := range mapOf(m, "派生参数") {
		if f := designFilter(str(asMap(dv), "筛选")); f != "" {
			col, op, _ := strings.Cut(f, " ")
			rs = append(rs, Rule{Kind: RuleFilter, Subject: col, Value: op})
		}
	}
	for _, c := range listOf(m, "固定条件") {
		rs = append(rs, fixedRules(scalar(c), dom)...)
	}
	for i, o := range listOf(m, "排序") {
		w := strings.Fields(scalar(o))
		if len(w) == 0 {
			continue
		}
		dir := "asc"
		if len(w) > 1 {
			dir = firstNonEmpty(orderWords[w[1]], w[1])
		}
		rs = append(rs, Rule{Kind: RuleOrder, Subject: strconv.Itoa(i + 1), Value: w[0] + " " + dir})
	}
	if n, err := strconv.Atoi(scalar(m["上限"])); err == nil {
		// the generated query fetches one row more than the limit to detect an overflow
		rs = append(rs, Rule{Kind: RuleLimit, Subject: "rows", Value: strconv.Itoa(n + 1)})
	}
	if lim := scalar(m["条数"]); lim != "" {
		rs = append(rs, Rule{Kind: RuleLimit, Subject: "rows", Value: lim})
	}
	return rs
}

var reFixedSplit = regexp.MustCompile(`\s+(或者|并且|或|且|and|or|AND|OR)\s+`)

// fixedRules reads "status = 2 或者 status = 3" into column/operator/value rules; enum value
// names become their database values.
func fixedRules(expr string, dom *factoryDomain) []Rule {
	var rs []Rule
	for _, part := range reFixedSplit.Split(expr, -1) {
		w := strings.Fields(part)
		if len(w) != 3 {
			continue
		}
		op := cmpWords[w[1]]
		if op == "" {
			continue
		}
		v := strings.Trim(w[2], `'"`)
		if dbv := dom.enums[v]; dbv != "" {
			v = dbv
		}
		rs = append(rs, Rule{Kind: RuleFixed, Subject: w[0], Value: op + " " + v})
	}
	return rs
}

// computeDesignRules: the columns the function may read (使用的列 = the query's SELECT list),
// the inputs it may read and the signature the generator calls.
func computeDesignRules(m map[string]any, api string) []Rule {
	var rs []Rule
	for _, c := range listOf(m, "使用的列") {
		rs = append(rs, Rule{Kind: RuleSelects, Subject: scalar(c)}, Rule{Kind: RuleReads, Subject: scalar(c)})
	}
	for name := range mapOf(m, "输入") {
		rs = append(rs, Rule{Kind: RuleReadsInput, Subject: name})
	}
	if factoryQueryKinds[str(m, "种类")] == "compute" {
		rs = append(rs, Rule{Kind: RuleSignature, Subject: lowerFirst(api) + "Logic", Value: computeSignature(api)})
	}
	return rs
}

// computeSignature is the signature the generated handler calls.
func computeSignature(api string) string {
	return "(*" + api + "Request, []" + lowerFirst(api) + "Row) (*" + api + "Response, error)"
}

// sortedKeys returns a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
