package contract

import (
	"fmt"
	"strings"
	"time"
)

// Authorities: which side is right when they differ.
const (
	AuthorityDesign = "design"
	AuthorityCode   = "code"
	AuthorityAsk    = "ask"
)

// Document is a docs-platform/contract/v1 document.
type Document struct {
	SchemaVersion string  `json:"schema_version"`
	GeneratedAt   string  `json:"generated_at"`
	DocSet        string  `json:"doc_set,omitempty"`
	Authority     string  `json:"authority"`
	Generator     string  `json:"generator,omitempty"`
	History       bool    `json:"history"`
	Design        Surface `json:"design"`
	Code          Surface `json:"code"`
	Items         []Item  `json:"items"`
	Summary       Summary `json:"summary"`
}

// Summary aggregates a document.
type Summary struct {
	DesignOperations int            `json:"design_operations"`
	CodeOperations   int            `json:"code_operations"`
	Overlap          int            `json:"overlap"`
	Items            int            `json:"items"`
	ByKind           map[string]int `json:"by_kind"`
	BySeverity       map[string]int `json:"by_severity"`
	ByDirection      map[string]int `json:"by_direction"`
	RenameCandidates int            `json:"rename_candidates"`
	// GeneratorGuaranteed counts the design operations left out because an API factory's
	// manifest says the generator keeps them in step with the design.
	GeneratorGuaranteed int            `json:"generator_guaranteed,omitempty"`
	Groups              []GroupSummary `json:"groups"`
}

// GroupSummary is the summary of one sub-service / domain.
type GroupSummary struct {
	Group            string         `json:"group"`
	DesignOperations int            `json:"design_operations"`
	CodeOperations   int            `json:"code_operations"`
	Overlap          int            `json:"overlap"`
	Items            int            `json:"items"`
	ByDirection      map[string]int `json:"by_direction"`
}

// Build assembles the document: messages and recommendations follow the authority.
func Build(docSet, authority, generator string, design, code *Surface, items []Item, history bool, now time.Time) *Document {
	if authority == "" {
		authority = AuthorityAsk
	}
	SortItems(items)
	for i := range items {
		items[i].Message, items[i].Recommendation = message(items[i], authority, generator)
	}
	if items == nil {
		items = []Item{}
	}
	return &Document{
		SchemaVersion: SchemaVersion, GeneratedAt: now.UTC().Format(time.RFC3339),
		DocSet: docSet, Authority: authority, Generator: generator, History: history,
		Design: *design, Code: *code, Items: items, Summary: summarize(design, code, items),
	}
}

// detail says what differs, side-neutrally.
func detail(it Item) string {
	what := it.Operation
	if it.Subject != "" {
		what += " " + it.Subject
	}
	switch it.Kind {
	case KindOpMissingInCode, KindInMissingInCode, KindOutMissingInCode, KindRuleMissingInCode:
		return fmt.Sprintf("%s is in the design (%s) but not in the code", what, it.Design)
	case KindOpMissingInDesign, KindInMissingInDesign, KindOutMissingInDesign, KindRuleMissingInDesign:
		return fmt.Sprintf("%s is in the code (%s) but not in the design", what, it.Code)
	}
	return fmt.Sprintf("%s: the design says %q, the code %q", what, it.Design, it.Code)
}

func message(it Item, authority, generator string) (string, string) {
	d := strings.ReplaceAll(detail(it), " ()", "")
	if it.Rename != nil {
		d += fmt.Sprintf(" (renamed? candidate %q, similarity %.2f)", it.Rename.Name, it.Rename.Score)
	}
	switch authority {
	case AuthorityDesign:
		return "code deviates from design: " + d, "change the code to match the design, or change the design first through its review"
	case AuthorityCode:
		rec := "update the design to describe the code"
		if generator != "" {
			rec += "; better, generate the design from the code: " + generator
		}
		return "design needs update: " + d, rec
	}
	return d, ""
}

func summarize(design, code *Surface, items []Item) Summary {
	s := Summary{ByKind: map[string]int{}, BySeverity: map[string]int{}, ByDirection: map[string]int{}, Items: len(items)}
	dn, cn := opNames(design.Operations), opNames(code.Operations)
	s.DesignOperations, s.CodeOperations, s.Overlap = len(dn), len(cn), overlap(dn, cn)
	groups := map[string]*GroupSummary{}
	g := func(name string) *GroupSummary {
		if groups[name] == nil {
			groups[name] = &GroupSummary{Group: name, ByDirection: map[string]int{}}
		}
		return groups[name]
	}
	for _, op := range design.Operations {
		g(op.Group).DesignOperations++
	}
	for _, op := range code.Operations {
		g(op.Group).CodeOperations++
	}
	for _, op := range design.Operations {
		if cn[op.Name] {
			g(op.Group).Overlap++
		}
	}
	for _, it := range items {
		s.ByKind[it.Kind]++
		s.BySeverity[it.Severity]++
		gs := g(it.Group)
		gs.Items++
		if it.Direction != nil {
			s.ByDirection[it.Direction.Class]++
			gs.ByDirection[it.Direction.Class]++
		}
		if it.Rename != nil {
			s.RenameCandidates++
		}
	}
	for _, k := range sortedKeys(groups) {
		s.Groups = append(s.Groups, *groups[k])
	}
	if s.Groups == nil {
		s.Groups = []GroupSummary{}
	}
	return s
}

func opNames(ops []Operation) map[string]bool {
	m := map[string]bool{}
	for _, op := range ops {
		m[op.Name] = true
	}
	return m
}

func overlap(a, b map[string]bool) int {
	n := 0
	for k := range a {
		if b[k] {
			n++
		}
	}
	return n
}
