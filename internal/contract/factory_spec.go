package contract

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// specOp reads a hand-written specs/<domain>/<api>/spec.yaml (spec_version 0.2). A spec
// with a generated header is the design compiler's output, not a design: it is skipped.
// The escape hatch datasource.sql is parsed like a query file: its parameters, SELECT list,
// tables and conditions are what the design declares.
func (p *factoryProject) specOp(f string, b []byte) (*Operation, error) {
	if head := string(b[:min(len(b), 400)]); reGeneratedHeader.MatchString(head) {
		return nil, nil //nolint:nilnil // generated from a design file: the design file is the source
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", f, err)
	}
	rpc := str(mapOf(m, "api"), "rpc")
	kind := factoryQueryKinds[str(mapOf(m, "pattern"), "kind")]
	if rpc == "" || kind == "" {
		return nil, nil //nolint:nilnil // not a query API
	}
	name := rpc[strings.LastIndex(rpc, "/")+1:]
	group := path.Base(path.Dir(path.Dir(f)))
	op := &Operation{Name: name, Kind: kind, Group: group, Path: f, Line: 1, Files: []string{f}, Partial: []string{"outputs"}}
	ds := mapOf(m, "datasource")
	if sql := str(ds, "sql"); sql != "" {
		specSQL(op, sql, kind, p.schema)
	} else {
		specDeclarative(op, ds)
	}
	if kind == "compute" {
		op.Rules = append(op.Rules, Rule{Kind: RuleSignature, Subject: lowerFirst(name) + "Logic", Value: computeSignature(name)})
	}
	defDir := firstNonEmpty(strings.ToLower(str(mapOf(m, "pattern"), "direction")), "asc")
	for i, o := range listOf(mapOf(m, "pattern"), "order_by") {
		w := strings.Fields(scalar(o))
		if len(w) > 0 {
			dir := defDir
			if len(w) > 1 {
				dir = strings.ToLower(w[1])
			}
			op.Rules = append(op.Rules, Rule{Kind: RuleOrder, Subject: strconv.Itoa(i + 1), Value: w[0] + " " + dir})
		}
	}
	return op, nil
}

// specDeclarative: table + filters + columns.
func specDeclarative(op *Operation, ds map[string]any) {
	if t := str(ds, "table"); t != "" {
		op.Rules = append(op.Rules, Rule{Kind: RuleTable, Subject: t})
	}
	for name, v := range mapOf(ds, "filters") {
		vm := asMap(v)
		op.Inputs = append(op.Inputs, Field{Name: name, Filter: str(vm, "column") + " " + firstNonEmpty(filterUnits[str(vm, "op")], str(vm, "op"))})
	}
	for _, c := range listOf(ds, "columns") {
		op.Rules = append(op.Rules, Rule{Kind: RuleSelects, Subject: scalar(c)})
		if op.Kind == "compute" {
			op.Rules = append(op.Rules, Rule{Kind: RuleReads, Subject: scalar(c)})
		}
	}
	// inputs follow the proto by naming convention; only overrides are written here
	op.Partial = append(op.Partial, "inputs")
}

// specSQL: the hand-written SQL is the declaration.
func specSQL(op *Operation, sql, kind string, schema tableTypes) {
	q := parseSQLQuery(sql, factoryMachinery)
	applySQL(op, q, schema, kind)
}
