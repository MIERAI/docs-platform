package contract

import (
	"crypto/sha1" //nolint:gosec // a stable id, not a security boundary (same rule as findings/v1)
	"encoding/hex"
	"sort"
	"strings"
)

// Item kinds.
const (
	KindOpMissingInCode     = "operation.missing_in_code"
	KindOpMissingInDesign   = "operation.missing_in_design"
	KindInMissingInCode     = "input.missing_in_code"
	KindInMissingInDesign   = "input.missing_in_design"
	KindInType              = "input.type"
	KindInPresence          = "input.presence"
	KindInFilter            = "input.filter"
	KindOutMissingInCode    = "output.missing_in_code"
	KindOutMissingInDesign  = "output.missing_in_design"
	KindOutType             = "output.type"
	KindOutColumn           = "output.column"
	KindRuleMissingInCode   = "rule.missing_in_code"
	KindRuleMissingInDesign = "rule.missing_in_design"
	KindRuleChanged         = "rule.changed"
)

// Severities.
const (
	SevHigh   = "high"
	SevMedium = "medium"
	SevLow    = "low"
	SevInfo   = "info"
)

// Item is one mismatch between the design and the code.
type Item struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	// Subject is what differs inside the operation: "inputs.sku", "outputs.items.price",
	// "rules.order.1"; empty for an operation-level item.
	Subject  string `json:"subject,omitempty"`
	Design   string `json:"design,omitempty"`
	Code     string `json:"code,omitempty"`
	Severity string `json:"severity"`
	// DesignPath / CodePath locate the operation on each side.
	DesignPath string `json:"design_path,omitempty"`
	DesignLine int    `json:"design_line,omitempty"`
	CodePath   string `json:"code_path,omitempty"`
	CodeLine   int    `json:"code_line,omitempty"`
	// Rename is a fuzzy name match on the other side: a candidate for a human, never a fact.
	Rename    *Rename    `json:"rename_candidate,omitempty"`
	Direction *Direction `json:"direction,omitempty"`
	Message   string     `json:"message"`
	// Recommendation depends on the doc set's authority.
	Recommendation string `json:"recommendation,omitempty"`

	designTerm, codeTerm   string
	designFiles, codeFiles []string
}

// Rename is a rename candidate.
type Rename struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// DiffOptions tune the comparison for an adapter family.
type DiffOptions struct {
	// SelectionSubset: the design's outputs are a client's selection of the code's type
	// (GraphQL): code fields the design does not select are informational.
	SelectionSubset bool
}

// Diff compares two surfaces. Operations are matched by name (and by group when a name is
// defined more than once on a side).
func Diff(design, code *Surface, o DiffOptions) []Item {
	var items []Item
	dIdx, cIdx := index(design.Operations), index(code.Operations)
	for _, name := range sortedKeys(dIdx) {
		for _, d := range dIdx[name] {
			c, ok := pick(cIdx[name], d.Group)
			if !ok {
				items = append(items, opItem(KindOpMissingInCode, d, Operation{}, SevHigh))
				continue
			}
			items = append(items, diffOp(d, c, o)...)
		}
	}
	for _, name := range sortedKeys(cIdx) {
		if _, ok := dIdx[name]; ok {
			continue
		}
		for _, c := range cIdx[name] {
			items = append(items, opItem(KindOpMissingInDesign, Operation{}, c, SevMedium))
		}
	}
	addRenameCandidates(items)
	for i := range items {
		items[i].ID = itemID(items[i])
	}
	return items
}

func index(ops []Operation) map[string][]Operation {
	m := map[string][]Operation{}
	for _, op := range ops {
		m[op.Name] = append(m[op.Name], op)
	}
	return m
}

// pick chooses the code operation of the same name, preferring the same group.
func pick(ops []Operation, group string) (Operation, bool) {
	if len(ops) == 0 {
		return Operation{}, false
	}
	for _, op := range ops {
		if strings.EqualFold(op.Group, group) {
			return op, true
		}
	}
	return ops[0], true
}

func opItem(kind string, d, c Operation, sev string) Item {
	it := base(d, c)
	it.Kind, it.Severity = kind, sev
	it.designTerm, it.codeTerm = firstNonEmpty(d.Name, c.Name), firstNonEmpty(d.Name, c.Name)
	return it
}

func base(d, c Operation) Item {
	return Item{
		Operation: firstNonEmpty(d.Name, c.Name), Group: firstNonEmpty(d.Group, c.Group),
		DesignPath: d.Path, DesignLine: d.Line, CodePath: c.Path, CodeLine: c.Line,
		designFiles: d.Files, codeFiles: c.Files,
	}
}

func partial(d, c Operation, part string) bool {
	for _, p := range append(append([]string(nil), d.Partial...), c.Partial...) {
		if p == part {
			return true
		}
	}
	return false
}

func diffOp(d, c Operation, o DiffOptions) []Item {
	var items []Item
	b := base(d, c)
	if !partial(d, c, "inputs") {
		items = append(items, diffFields(b, "inputs", d.Inputs, c.Inputs, true, o)...)
	}
	if !partial(d, c, "outputs") {
		items = append(items, diffFields(b, "outputs", d.Outputs, c.Outputs, false, o)...)
	}
	items = append(items, diffRules(b, d.Rules, c.Rules)...)
	return items
}

// fieldDiff compares the inputs or the outputs of one operation pair.
type fieldDiff struct {
	b     Item
	input bool
	o     DiffOptions
}

func (f fieldDiff) item(where, kind, name, dv, cv, sev string) Item {
	it := f.b
	it.Kind, it.Subject, it.Design, it.Code, it.Severity = kind, where+"."+name, dv, cv, sev
	it.designTerm, it.codeTerm = leaf(name), leaf(name)
	return it
}

// diffFields compares inputs or outputs (recursively for nested fields).
func diffFields(b Item, where string, ds, cs []Field, input bool, o DiffOptions) []Item {
	f := fieldDiff{b: b, input: input, o: o}
	return f.fields(where, ds, cs)
}

func (f fieldDiff) fields(where string, ds, cs []Field) []Item {
	var items []Item
	missCode, missDesign := KindOutMissingInCode, KindOutMissingInDesign
	if f.input {
		missCode, missDesign = KindInMissingInCode, KindInMissingInDesign
	}
	for _, d := range ds {
		c, ok := field(cs, d.Name)
		if !ok {
			items = append(items, f.item(where, missCode, d.Name, describe(d), "", SevHigh))
			continue
		}
		items = append(items, f.pair(where, d, c)...)
	}
	for _, c := range cs {
		if _, ok := field(ds, c.Name); !ok {
			items = append(items, f.item(where, missDesign, c.Name, "", describe(c), f.codeOnlySeverity(c)))
		}
	}
	return items
}

func (f fieldDiff) codeOnlySeverity(c Field) string {
	switch {
	case f.input && c.Presence != Required:
		return SevLow
	case !f.input && f.o.SelectionSubset:
		return SevInfo
	}
	return SevMedium
}

// pair compares a field present on both sides.
func (f fieldDiff) pair(where string, d, c Field) []Item {
	var items []Item
	differs := func(a, b string) bool { return a != "" && b != "" && a != b }
	typeKind := KindOutType
	if f.input {
		typeKind = KindInType
	}
	if typesDiffer(d.Type, c.Type) {
		items = append(items, f.item(where, typeKind, d.Name, d.Type, c.Type, SevHigh))
	}
	if f.input && differs(d.Presence, c.Presence) {
		items = append(items, f.item(where, KindInPresence, d.Name, d.Presence, c.Presence, presenceSeverity(d, c)))
	}
	if f.input && differs(d.Filter, c.Filter) {
		items = append(items, f.item(where, KindInFilter, d.Name, d.Filter, c.Filter, SevHigh))
	}
	if !f.input && differs(d.Column, c.Column) {
		items = append(items, f.item(where, KindOutColumn, d.Name, d.Column, c.Column, SevHigh))
	}
	if len(d.Fields) > 0 && len(c.Fields) > 0 {
		items = append(items, f.fields(where+"."+d.Name, d.Fields, c.Fields)...)
	}
	return items
}

// presenceSeverity: code requiring what the design calls optional breaks callers (medium);
// the reverse is harmless (low).
func presenceSeverity(d, c Field) string {
	if d.Presence == Optional && c.Presence == Required {
		return SevMedium
	}
	return SevLow
}

func leaf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func describe(f Field) string {
	var parts []string
	for _, p := range []string{f.Type, f.Presence, f.Filter} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if f.Column != "" && f.Column != f.Name {
		parts = append(parts, "column "+f.Column)
	}
	return strings.Join(parts, " ")
}

// subsetRules are rules where the code may use less than the design allows (a function need
// not read every declared column): design-only entries are informational.
var subsetRules = map[string]bool{RuleReads: true, RuleReadsInput: true}

// slotRules are compared per subject (position, name): a different value is a change.
var slotRules = map[string]bool{RuleOrder: true, RuleLimit: true, RuleSignature: true}

func diffRules(b Item, ds, cs []Rule) []Item {
	dKinds := map[string]bool{}
	for _, r := range ds {
		dKinds[r.Kind] = true
	}
	dSet, cSet := ruleSet(ds), ruleSet(cs)
	var items []Item
	for _, k := range sortedKeys(dSet) {
		if _, ok := cSet[k]; !ok {
			items = append(items, designOnlyRule(b, dSet[k], cs))
		}
	}
	for _, k := range sortedKeys(cSet) {
		if _, ok := dSet[k]; ok {
			continue
		}
		if it, ok := codeOnlyRule(b, cSet[k], ds, dKinds); ok {
			items = append(items, it)
		}
	}
	return items
}

// designOnlyRule: a rule of the design that the code lacks (or holds with another value).
func designOnlyRule(b Item, r Rule, cs []Rule) Item {
	if slotRules[r.Kind] {
		if c, ok := slot(cs, r); ok {
			return ruleItem(b, KindRuleChanged, r, r.Value, c.Value, SevMedium)
		}
	}
	sev := SevMedium
	if subsetRules[r.Kind] {
		sev = SevInfo
	}
	return ruleItem(b, KindRuleMissingInCode, r, r.Value, "", sev)
}

// codeOnlyRule: a rule of the code that the design lacks; false when already reported as
// changed, or when the design does not declare rules of this kind at all.
func codeOnlyRule(b Item, r Rule, ds []Rule, dKinds map[string]bool) (Item, bool) {
	if slotRules[r.Kind] {
		if _, ok := slot(ds, r); ok {
			return Item{}, false
		}
	}
	if subsetRules[r.Kind] && !dKinds[r.Kind] {
		return Item{}, false
	}
	return ruleItem(b, KindRuleMissingInDesign, r, "", r.Value, SevMedium), true
}

func ruleSet(rs []Rule) map[string]Rule {
	m := map[string]Rule{}
	for _, r := range rs {
		m[ruleKey(r)] = r
	}
	return m
}

func slot(rs []Rule, r Rule) (Rule, bool) {
	for _, x := range rs {
		if x.Kind == r.Kind && x.Subject == r.Subject {
			return x, true
		}
	}
	return Rule{}, false
}

func ruleItem(b Item, kind string, r Rule, dv, cv, sev string) Item {
	it := b
	it.Kind, it.Subject, it.Design, it.Code, it.Severity = kind, "rules."+r.Kind+"."+r.Subject, dv, cv, sev
	term := r.Subject
	switch r.Kind {
	case RuleOrder:
		term = strings.Fields(firstNonEmpty(dv, cv) + " ?")[0]
	case RuleLimit:
		it.designTerm, it.codeTerm = "上限", "LIMIT"
		return it
	case RuleSignature:
		it.designTerm, it.codeTerm = it.Operation, r.Subject
		return it
	}
	it.designTerm, it.codeTerm = term, term
	return it
}

// typesDiffer compares two normalized types; unknown (empty) types never differ, object
// types are compared through their fields, and generic integer / number types of a
// description language accept every concrete one.
func typesDiffer(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return !typeCompatible(a, b)
}

func typeCompatible(a, b string) bool {
	a, b = strings.TrimSuffix(a, "!"), strings.TrimSuffix(b, "!")
	if a == b {
		return true
	}
	if la, ok := listElem(a); ok {
		lb, ok := listElem(b)
		return ok && typeCompatible(la, lb)
	}
	if _, ok := listElem(b); ok {
		return false
	}
	if strings.HasPrefix(a, "obj:") && strings.HasPrefix(b, "obj:") {
		return true
	}
	return generic[a] != "" && generic[a] == generic[b] && (isGeneric(a) || isGeneric(b))
}

// listElem unwraps [T] / list<T>.
func listElem(t string) (string, bool) {
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		return t[1 : len(t)-1], true
	}
	if strings.HasPrefix(t, "list<") && strings.HasSuffix(t, ">") {
		return t[5 : len(t)-1], true
	}
	return "", false
}

var generic = map[string]string{
	"integer": "int", "Int": "int", "BigInteger": "int", "Long": "int", "Short": "int",
	"number": "num", "Float": "num", "BigDecimal": "num",
}

func isGeneric(t string) bool { return t == "integer" || t == "number" }

func itemID(it Item) string {
	h := sha1.Sum([]byte(it.Kind + "|" + it.Group + "|" + it.Operation + "|" + it.Subject)) //nolint:gosec // stable id only
	return hex.EncodeToString(h[:])[:12]
}

// SortItems orders items by group, operation, kind, subject.
func SortItems(items []Item) {
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a], items[b]
		for _, p := range [][2]string{{x.Group, y.Group}, {x.Operation, y.Operation}, {x.Kind, y.Kind}, {x.Subject, y.Subject}} {
			if p[0] != p[1] {
				return p[0] < p[1]
			}
		}
		return false
	})
}
