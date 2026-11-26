// Package contract is the deterministic contract layer (docs/design.md §13): it extracts a
// normalized interface surface (operations, inputs, outputs, types, declared rules) from a
// design-first specification and from the code that implements it, diffs the two, and uses
// git history to tell which side moved. No model is involved.
//
// Adapters turn one kind of source into a Surface:
//
//   - factory-design / factory-code: the design language of a design-first code generator
//     (design/<domain>/<API>.yaml, the domain file, the schema SQL and the lock files) and the
//     files the generator cannot keep in step - the SQL query files (escape hatch
//     datasource.sql) and the pure compute functions (escape hatch compute);
//   - openapi-graphql: OpenAPI documents whose paths embed GraphQL query examples;
//   - kotlin-graphql: GraphQL code-first Kotlin (SmallRye / MicroProfile GraphQL).
//
// The output is a docs-platform/contract/v1 document.
package contract

import (
	"sort"
	"strings"
)

// SchemaVersion is the contract/v1 minor this package writes.
const SchemaVersion = "1.0"

// Sides of a contract.
const (
	SideDesign = "design"
	SideCode   = "code"
)

// Presence of an input or output ("" = the adapter cannot tell).
const (
	Required = "required"
	Optional = "optional"
)

// Surface is one side's normalized contract.
type Surface struct {
	Side       string      `json:"side"`
	Adapter    AdapterInfo `json:"adapter"`
	Source     SourceInfo  `json:"source"`
	Operations []Operation `json:"operations"`
	// Gaps are files that failed to parse: their operations are unknown, not absent.
	Gaps []string `json:"gaps,omitempty"`
}

// AdapterInfo names the adapter that produced a surface.
type AdapterInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SourceInfo is where a surface was read: a repository at a revision, limited to path globs.
type SourceInfo struct {
	Repo   string   `json:"repo"`
	Ref    string   `json:"ref"`
	Commit string   `json:"commit,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	// Worktree: read from the working tree (uncommitted changes included), not from a commit.
	Worktree bool `json:"worktree,omitempty"`
}

// Operation is one callable unit: an API of the generator, a GraphQL root field.
type Operation struct {
	Name string `json:"name"`
	// Kind is the adapter's operation kind (query, mutation, get_by_id, list_all, compute ...).
	Kind string `json:"kind,omitempty"`
	// Group is the sub-service or domain the operation belongs to (summaries, matching).
	Group   string  `json:"group,omitempty"`
	Path    string  `json:"path"`
	Line    int     `json:"line,omitempty"`
	Inputs  []Field `json:"inputs,omitempty"`
	Outputs []Field `json:"outputs,omitempty"`
	Rules   []Rule  `json:"rules,omitempty"`
	// Files are every file that defines the operation (history is searched there).
	Files []string `json:"files,omitempty"`
	// Partial names the parts the adapter could not read ("inputs", "outputs"): they are
	// unknown, not empty, and are not compared.
	Partial []string `json:"partial,omitempty"`
}

// Field is an input (argument, parameter) or an output (selected field, column).
type Field struct {
	Name string `json:"name"`
	// Type is normalized per adapter family (see typeKey); "" = unknown.
	Type     string `json:"type,omitempty"`
	Presence string `json:"presence,omitempty"`
	Column   string `json:"column,omitempty"`
	// Filter is how an input restricts the rows: "column op" pairs, comma-separated.
	Filter string  `json:"filter,omitempty"`
	Fields []Field `json:"fields,omitempty"`
}

// Rule is a declared, machine-readable rule: a filter (column + operator), the ordering, a
// fixed condition, a row limit, the table, a function signature, a column a function reads.
type Rule struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Value   string `json:"value,omitempty"`
}

// Rule kinds.
const (
	RuleTable      = "table"
	RuleFilter     = "filter"
	RuleOrder      = "order"
	RuleFixed      = "fixed"
	RuleLimit      = "limit"
	RuleSignature  = "signature"
	RuleSelects    = "selects"
	RuleReads      = "reads"
	RuleReadsInput = "reads_input"
)

// Normalize sorts everything that has no meaningful order, so that two surfaces of the same
// contract are equal whatever the order of their sources (a reordered SELECT list or AND
// list is not a change). Rules of kind order keep their position in Subject.
func (s *Surface) Normalize() {
	for i := range s.Operations {
		op := &s.Operations[i]
		sortFields(op.Inputs)
		sortFields(op.Outputs)
		sort.Slice(op.Rules, func(a, b int) bool { return ruleKey(op.Rules[a]) < ruleKey(op.Rules[b]) })
		op.Rules = dedupRules(op.Rules)
		sort.Strings(op.Files)
		op.Files = dedupStrings(op.Files)
	}
	sort.SliceStable(s.Operations, func(a, b int) bool {
		x, y := s.Operations[a], s.Operations[b]
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		if x.Group != y.Group {
			return x.Group < y.Group
		}
		return x.Path < y.Path
	})
	sort.Strings(s.Gaps)
	if s.Operations == nil {
		s.Operations = []Operation{}
	}
}

func sortFields(fs []Field) {
	sort.Slice(fs, func(a, b int) bool { return fs[a].Name < fs[b].Name })
	for i := range fs {
		sortFields(fs[i].Fields)
	}
}

func ruleKey(r Rule) string { return r.Kind + "\x00" + r.Subject + "\x00" + r.Value }

func dedupRules(rs []Rule) []Rule {
	var out []Rule
	for i, r := range rs {
		if i > 0 && ruleKey(r) == ruleKey(rs[i-1]) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func dedupStrings(ss []string) []string {
	var out []string
	for i, s := range ss {
		if i > 0 && s == ss[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// field finds a field by name.
func field(fs []Field, name string) (Field, bool) {
	for _, f := range fs {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// snake converts lowerCamel / UpperCamel to snake_case (TotalAmount → total_amount,
// HTTPServer → http_server, IDs → ids).
func snake(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		upper := isUpper(r)
		if upper && i > 0 && wordStart(rs, i) {
			b.WriteByte('_')
		}
		if upper {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

// wordStart: the upper-case rune at i starts a word (after a lower-case letter or digit, or
// as the last capital of an acronym followed by a lower-case letter - but not a plural "s").
func wordStart(rs []rune, i int) bool {
	prev := rs[i-1]
	if isLower(prev) || prev >= '0' && prev <= '9' {
		return true
	}
	if !isUpper(prev) || i+1 >= len(rs) || !isLower(rs[i+1]) {
		return false
	}
	return rs[i+1] != 's' || i+2 != len(rs)
}

// lowerFirst lower-cases the first letter (GetDailySalesSummary → getDailySalesSummary).
func lowerFirst(s string) string {
	if s == "" || s[0] < 'A' || s[0] > 'Z' {
		return s
	}
	return string(s[0]+'a'-'A') + s[1:]
}
