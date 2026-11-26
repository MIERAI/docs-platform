package contract

import (
	"regexp"
	"sort"
	"strings"
)

// sqlQuery is what a query file declares, read by simple parsing (no SQL grammar): the
// parameters with their casts, the outermost SELECT list resolved through derived tables to
// base columns, the base tables, the comparisons of WHERE clauses, the outermost ORDER BY
// and LIMIT.
type sqlQuery struct {
	Name     string
	Cmd      string // :one, :many, :exec ...
	Params   map[string]sqlParam
	Outputs  []sqlOutput
	Wildcard bool
	Tables   []string
	Filters  []sqlCmp // column op single-parameter
	Derived  []sqlCmp // column op expression with parameters
	Fixed    []sqlCmp // column op constant
	Order    []string // "column asc|desc"
	Limit    string
}

type sqlParam struct {
	Type     string
	Optional bool
}

type sqlOutput struct {
	Name, Column, Cast string
}

type sqlCmp struct {
	Column, Op, Value string
	Params            []string
}

var reSQLCName = regexp.MustCompile(`(?m)^\s*--\s*name:\s*([A-Za-z_][A-Za-z0-9_]*)\s*(:[a-z]+)?`)

// clause keywords end an expression at the same depth.
var sqlBoundary = map[string]bool{
	"AND": true, "OR": true, "WHERE": true, "ON": true, "NOT": true, "SELECT": true, "FROM": true,
	"WHEN": true, "THEN": true, "ELSE": true, "CASE": true, "END": true, "ORDER": true, "GROUP": true,
	"LIMIT": true, "OFFSET": true, "HAVING": true, "UNION": true, "JOIN": true, "LEFT": true,
	"INNER": true, "RIGHT": true, "EXISTS": true, "BY": true, "RETURNING": true, "SET": true,
}

var sqlCmpOps = map[string]string{
	"=": "eq", "<>": "ne", "!=": "ne", "<": "lt", "<=": "lte", ">": "gt", ">=": "gte",
	"LIKE": "prefix", "ILIKE": "prefix", "IN": "in",
}

var flipOp = map[string]string{"lt": "gt", "lte": "gte", "gt": "lt", "gte": "lte", "eq": "eq", "ne": "ne"}

// parseSQLQuery reads one query. machinery reports parameters that the generator adds and
// that are not part of the contract (cursor, row limit, clock).
func parseSQLQuery(src string, machinery func(string) bool) sqlQuery {
	q := sqlQuery{Params: map[string]sqlParam{}}
	if m := reSQLCName.FindStringSubmatch(src); m != nil {
		q.Name, q.Cmd = m[1], m[2]
	}
	toks := simplifySQL(lexSQL(src))
	for _, t := range toks {
		if t.Kind == tkParam && (machinery == nil || !machinery(t.Text)) {
			p := q.Params[t.Text]
			if p.Type == "" {
				p.Type = t.Cast
			}
			p.Optional = p.Optional || t.Opt
			q.Params[t.Text] = p
		}
	}
	a := newSQLAnalysis(toks)
	q.Tables = a.tables()
	q.Outputs, q.Wildcard = a.outputs()
	a.comparisons(&q, machinery)
	q.Order, q.Limit = a.orderLimit()
	return q
}

// sqlAnalysis indexes a simplified token stream.
type sqlAnalysis struct {
	toks    []sqlTok
	base    map[string]string // alias → base table
	sub     map[string]int    // alias → index of the SELECT of the derived table
	inWhere []bool
}

func newSQLAnalysis(toks []sqlTok) *sqlAnalysis {
	a := &sqlAnalysis{toks: toks, base: map[string]string{}, sub: map[string]int{}, inWhere: make([]bool, len(toks))}
	for i, t := range toks {
		if t.Kind == tkIdent && (t.Up == "FROM" || t.Up == "JOIN") {
			a.fromItem(i + 1)
		}
		if t.Kind == tkIdent && t.Up == "WHERE" {
			a.markWhere(i)
		}
	}
	return a
}

// fromItem reads "table [AS] alias", "(SELECT ...) [AS] alias" or "LATERAL (...) AS alias".
func (a *sqlAnalysis) fromItem(i int) {
	if i < len(a.toks) && a.toks[i].Up == "LATERAL" {
		i++
	}
	if i >= len(a.toks) {
		return
	}
	t := a.toks[i]
	switch {
	case t.Text == "(" && t.Kind == tkPunct:
		end := a.closing(i)
		if alias := a.aliasAt(end + 1); alias != "" && i+1 < len(a.toks) && a.toks[i+1].Up == "SELECT" {
			a.sub[alias] = i + 1
		}
	case t.Kind == tkCol: // schema.table
		a.addBase(t.Text, i+1)
	case (t.Kind == tkIdent || t.Kind == tkQIdent) && !sqlBoundary[t.Up]:
		if i+1 < len(a.toks) && a.toks[i+1].Text == "(" {
			return // a set-returning function (unnest ...)
		}
		a.addBase(t.Text, i+1)
	}
}

func (a *sqlAnalysis) addBase(table string, aliasAt int) {
	a.base[table] = table
	if alias := a.aliasAt(aliasAt); alias != "" {
		a.base[alias] = table
	}
}

func (a *sqlAnalysis) aliasAt(i int) string {
	if i < len(a.toks) && a.toks[i].Up == "AS" {
		i++
	}
	if i < len(a.toks) && (a.toks[i].Kind == tkIdent || a.toks[i].Kind == tkQIdent) && !sqlBoundary[a.toks[i].Up] {
		return a.toks[i].Text
	}
	return ""
}

// closing returns the index of the parenthesis closing the one at i.
func (a *sqlAnalysis) closing(i int) int {
	d := a.toks[i].Depth
	for j := i + 1; j < len(a.toks); j++ {
		if a.toks[j].Text == ")" && a.toks[j].Kind == tkPunct && a.toks[j].Depth == d {
			return j
		}
	}
	return len(a.toks) - 1
}

func (a *sqlAnalysis) markWhere(i int) {
	d := a.toks[i].Depth
	for j := i + 1; j < len(a.toks); j++ {
		t := a.toks[j]
		if t.Depth < d || (t.Depth == d && t.Kind == tkIdent && (t.Up == "ORDER" || t.Up == "GROUP" || t.Up == "LIMIT" || t.Up == "HAVING" || t.Up == "UNION" || t.Up == "RETURNING")) {
			return
		}
		a.inWhere[j] = true
	}
}

func (a *sqlAnalysis) tables() []string {
	set := map[string]bool{}
	for alias, t := range a.base {
		if alias == t {
			set[t] = true
		}
	}
	var out []string
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// selectItems splits the SELECT list starting at index s (the SELECT token).
func (a *sqlAnalysis) selectItems(s int) [][]sqlTok {
	d := a.toks[s].Depth
	var items [][]sqlTok
	var cur []sqlTok
	for i := a.skipDistinct(s + 1); i < len(a.toks) && !a.endsSelectList(i, d); i++ {
		t := a.toks[i]
		if t.Depth == d && t.Text == "," && t.Kind == tkPunct {
			items = append(items, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		items = append(items, cur)
	}
	return items
}

// skipDistinct skips DISTINCT [ON (...)].
func (a *sqlAnalysis) skipDistinct(i int) int {
	if i >= len(a.toks) || a.toks[i].Up != "DISTINCT" {
		return i
	}
	i++
	if i+1 < len(a.toks) && a.toks[i].Up == "ON" && a.toks[i+1].Text == "(" {
		i = a.closing(i+1) + 1
	}
	return i
}

var selectListEnd = map[string]bool{"FROM": true, "WHERE": true, "ORDER": true, "LIMIT": true, "UNION": true, "GROUP": true}

func (a *sqlAnalysis) endsSelectList(i, d int) bool {
	t := a.toks[i]
	return t.Depth < d || (t.Depth == d && t.Kind == tkIdent && selectListEnd[t.Up])
}

// itemAlias splits "expr AS alias" / "expr alias" / "q.col".
func itemAlias(item []sqlTok) (string, []sqlTok) {
	n := len(item)
	switch {
	case n >= 2 && item[n-2].Up == "AS":
		return item[n-1].Text, item[:n-2]
	case n == 1 && item[0].Kind == tkCol:
		return item[0].Text, item
	case n == 1 && (item[0].Kind == tkIdent || item[0].Kind == tkQIdent):
		return item[0].Text, item
	case n >= 2 && (item[n-1].Kind == tkIdent || item[n-1].Kind == tkQIdent) && !sqlBoundary[item[n-1].Up] && (item[n-2].Kind != tkPunct || item[n-2].Text == ")"):
		return item[n-1].Text, item[:n-1]
	}
	return "", item
}

// outputs resolves the outermost SELECT list.
func (a *sqlAnalysis) outputs() ([]sqlOutput, bool) {
	s := a.outerSelect()
	if s < 0 {
		return nil, false
	}
	var out []sqlOutput
	wildcard := false
	for _, item := range a.selectItems(s) {
		if len(item) == 1 && item[0].Text == "*" {
			wildcard = true
			continue
		}
		name, expr := itemAlias(item)
		if name == "" {
			continue
		}
		col, cast := a.resolve(expr, 0)
		out = append(out, sqlOutput{Name: name, Column: col, Cast: cast})
	}
	return out, wildcard
}

func (a *sqlAnalysis) outerSelect() int {
	best, bd := -1, 1<<30
	for i, t := range a.toks {
		if t.Kind == tkIdent && t.Up == "SELECT" && t.Depth < bd {
			best, bd = i, t.Depth
		}
	}
	return best
}

// resolve follows an output expression to the base column it reads ("" when computed) and
// the cast applied at the top level.
func (a *sqlAnalysis) resolve(expr []sqlTok, depth int) (string, string) {
	expr = unwrapParens(expr)
	if len(expr) == 0 || depth > 16 {
		return "", ""
	}
	cast := expr[len(expr)-1].Cast
	if len(expr) == 1 && expr[0].Kind == tkCol {
		if s, ok := a.sub[expr[0].Qual]; ok {
			for _, it := range a.selectItems(s) {
				if name, inner := itemAlias(it); name == expr[0].Text {
					col, c := a.resolve(inner, depth+1)
					return col, firstNonEmpty(cast, c)
				}
			}
			return "", cast
		}
		return expr[0].Text, cast
	}
	if len(expr) == 1 && expr[0].Kind == tkIdent {
		return expr[0].Text, cast
	}
	if len(expr) == 3 && expr[0].Kind == tkCol && expr[1].Text == "->>" && expr[2].Kind == tkString {
		return expr[2].Text, cast // a key of a JSON column
	}
	return "", cast
}

func unwrapParens(e []sqlTok) []sqlTok {
	for len(e) >= 2 && e[0].Text == "(" && e[0].Kind == tkPunct && e[len(e)-1].Text == ")" && e[len(e)-1].Kind == tkPunct && e[len(e)-1].Depth == e[0].Depth {
		inner := e[1 : len(e)-1]
		balanced := true
		for _, t := range inner {
			if t.Depth <= e[0].Depth {
				balanced = false
				break
			}
		}
		if !balanced {
			break
		}
		cast := e[len(e)-1].Cast
		e = append([]sqlTok(nil), inner...)
		if cast != "" && len(e) > 0 {
			e[len(e)-1].Cast = cast
		}
	}
	return e
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// comparisons classifies every comparison inside WHERE clauses.
func (a *sqlAnalysis) comparisons(q *sqlQuery, machinery func(string) bool) {
	for i, t := range a.toks {
		op, ok := sqlCmpOps[t.Up]
		if t.Kind == tkPunct {
			op, ok = sqlCmpOps[t.Text]
		}
		if !ok || !a.inWhere[i] {
			continue
		}
		left := unwrapParens(a.operand(i, -1))
		right := unwrapParens(a.operand(i, +1))
		if len(right) >= 1 && (right[0].Up == "ANY" || right[0].Up == "ALL") {
			right, op = unwrapParens(right[1:]), "in"
		}
		if !isColRef(left) && isColRef(right) {
			left, right = right, left
			if f, ok := flipOp[op]; ok {
				op = f
			}
		}
		if !isColRef(left) {
			continue
		}
		a.classify(q, left[0].Qual, left[0].Text, op, right, machinery)
	}
}

func isColRef(e []sqlTok) bool { return len(e) == 1 && e[0].Kind == tkCol && e[0].Text != "" }

func (a *sqlAnalysis) classify(q *sqlQuery, qual, col, op string, right []sqlTok, machinery func(string) bool) {
	r := scanOperand(right, machinery)
	switch {
	case r.hasCol && len(r.params) == 0 && len(right) == 1 && right[0].Qual == qual:
		q.Fixed = append(q.Fixed, sqlCmp{Column: col, Op: op, Value: right[0].Text})
	case r.hasCol && len(r.params) == 0, r.mach && len(r.params) == 0:
		return // a join condition; a cursor or clock comparison
	case len(r.params) == 1 && (len(right) == 1 || op == "prefix"):
		q.Filters = append(q.Filters, sqlCmp{Column: col, Op: op, Params: r.params})
	case len(r.params) > 0:
		sort.Strings(r.params)
		q.Derived = append(q.Derived, sqlCmp{Column: col, Op: op, Params: r.params})
	case isLiteral(right):
		q.Fixed = append(q.Fixed, sqlCmp{Column: col, Op: op, Value: right[0].Text})
	}
}

func isLiteral(e []sqlTok) bool {
	return len(e) == 1 && (e[0].Kind == tkNumber || e[0].Kind == tkString || e[0].Kind == tkIdent)
}

// operandScan is what the other side of a comparison holds.
type operandScan struct {
	params       []string
	hasCol, mach bool
}

func scanOperand(e []sqlTok, machinery func(string) bool) operandScan {
	var r operandScan
	for _, t := range e {
		switch {
		case t.Kind == tkParam && machinery != nil && machinery(t.Text):
			r.mach = true
		case t.Kind == tkParam:
			r.params = append(r.params, t.Text)
		case t.Kind == tkCol:
			r.hasCol = true
		}
	}
	return r
}

// operand collects the tokens of one side of the operator at i (dir -1 left, +1 right).
func (a *sqlAnalysis) operand(i, dir int) []sqlTok {
	d := a.toks[i].Depth
	var out []sqlTok
	for j := i + dir; j >= 0 && j < len(a.toks); j += dir {
		t := a.toks[j]
		if t.Depth < d || (t.Depth == d && isOperandBoundary(t)) {
			break
		}
		if t.Depth == d && t.Text == ")" && dir < 0 && t.Kind == tkPunct {
			// a parenthesized left operand: take it whole
			k := a.opening(j)
			out = append(append([]sqlTok(nil), a.toks[k:j+1]...), out...)
			j = k
			continue
		}
		if dir < 0 {
			out = append([]sqlTok{t}, out...)
		} else {
			out = append(out, t)
		}
	}
	return out
}

func isOperandBoundary(t sqlTok) bool {
	if t.Kind == tkIdent && sqlBoundary[t.Up] {
		return true
	}
	if t.Kind == tkPunct && t.Text == "," {
		return true
	}
	if t.Kind == tkPunct {
		if _, ok := sqlCmpOps[t.Text]; ok {
			return true
		}
	}
	return false
}

// opening returns the index of the parenthesis opening the one closing at j.
func (a *sqlAnalysis) opening(j int) int {
	d := a.toks[j].Depth
	for k := j - 1; k >= 0; k-- {
		if a.toks[k].Text == "(" && a.toks[k].Kind == tkPunct && a.toks[k].Depth == d {
			return k
		}
	}
	return 0
}

// orderLimit reads the ORDER BY and LIMIT of the outermost SELECT.
func (a *sqlAnalysis) orderLimit() ([]string, string) {
	s := a.outerSelect()
	if s < 0 {
		return nil, ""
	}
	d := a.toks[s].Depth
	var order []string
	limit := ""
	for i := s; i < len(a.toks); i++ {
		t := a.toks[i]
		if t.Depth != d || t.Kind != tkIdent {
			continue
		}
		if t.Up == "ORDER" && i+1 < len(a.toks) && a.toks[i+1].Up == "BY" {
			order = a.orderItems(i+2, d)
		}
		if t.Up == "LIMIT" && i+1 < len(a.toks) {
			limit = a.toks[i+1].Text
			if a.toks[i+1].Kind == tkParam {
				limit = "@" + limit
			}
		}
	}
	return order, limit
}

func (a *sqlAnalysis) orderItems(i, d int) []string {
	var out []string
	cur, dir := "", "asc"
	flush := func() {
		if cur != "" {
			out = append(out, cur+" "+dir)
		}
		cur, dir = "", "asc"
	}
	for ; i < len(a.toks) && !a.endsOrder(i, d); i++ {
		t := a.toks[i]
		switch {
		case t.Depth == d && t.Text == "," && t.Kind == tkPunct:
			flush()
		case t.Kind == tkIdent && (t.Up == "DESC" || t.Up == "ASC"):
			dir = strings.ToLower(t.Up)
		case cur == "" && isOrderKey(t):
			cur = t.Text
		}
	}
	flush()
	return out
}

func (a *sqlAnalysis) endsOrder(i, d int) bool {
	t := a.toks[i]
	return t.Depth < d || (t.Depth == d && t.Kind == tkIdent && (t.Up == "LIMIT" || t.Up == "OFFSET")) || (t.Text == ";" && t.Kind == tkPunct)
}

func isOrderKey(t sqlTok) bool {
	switch t.Kind {
	case tkCol:
		return true
	case tkIdent, tkQIdent:
		return t.Up != "NULLS" && t.Up != "FIRST" && t.Up != "LAST"
	}
	return false
}
