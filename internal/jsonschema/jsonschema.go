// Package jsonschema is a small JSON Schema validator for the subset the contracts use: type,
// required, properties, additionalProperties (bool or schema), enum, pattern, maxLength, items
// and local $ref (#/$defs/...). Stdlib only, so contract tests need nothing else.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Schema is a compiled schema document.
type Schema struct {
	root map[string]any
	re   map[string]*regexp.Regexp
}

// Compile parses a schema document.
func Compile(b []byte) (*Schema, error) {
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	return &Schema{root: root, re: map[string]*regexp.Regexp{}}, nil
}

// ValidateJSON validates a JSON document.
func (s *Schema) ValidateJSON(doc []byte) []string {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return []string{"$: invalid JSON: " + err.Error()}
	}
	return s.Validate(v)
}

// Validate validates a decoded JSON value (from encoding/json into any).
func (s *Schema) Validate(v any) []string { return s.check(v, s.root, "$", 0) }

func (s *Schema) check(v any, sch map[string]any, at string, depth int) []string {
	if depth > 64 {
		return []string{at + ": schema too deep"}
	}
	if ref, ok := sch["$ref"].(string); ok {
		return s.checkRef(v, ref, at, depth)
	}
	if t, ok := sch["type"].(string); ok && !typeOK(v, t) {
		return []string{fmt.Sprintf("%s: expected %s", at, t)}
	}
	var errs []string
	if enum, ok := sch["enum"].([]any); ok && !inEnum(v, enum) {
		errs = append(errs, fmt.Sprintf("%s: %v not in %v", at, v, enum))
	}
	switch x := v.(type) {
	case string:
		errs = append(errs, s.checkString(x, sch, at)...)
	case map[string]any:
		errs = append(errs, s.checkObject(x, sch, at, depth)...)
	case []any:
		if items, ok := sch["items"].(map[string]any); ok {
			for i, it := range x {
				errs = append(errs, s.check(it, items, fmt.Sprintf("%s[%d]", at, i), depth+1)...)
			}
		}
	}
	return errs
}

func (s *Schema) checkRef(v any, ref, at string, depth int) []string {
	name, ok := strings.CutPrefix(ref, "#/$defs/")
	defs, _ := s.root["$defs"].(map[string]any)
	sub, found := defs[name].(map[string]any)
	if !ok || !found {
		return []string{fmt.Sprintf("%s: unresolvable $ref %q", at, ref)}
	}
	return s.check(v, sub, at, depth+1)
}

func (s *Schema) checkString(x string, sch map[string]any, at string) []string {
	var errs []string
	if p, ok := sch["pattern"].(string); ok {
		re, err := s.regexp(p)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: bad pattern %q", at, p))
		} else if !re.MatchString(x) {
			errs = append(errs, fmt.Sprintf("%s: %q does not match %s", at, x, p))
		}
	}
	if m, ok := sch["maxLength"].(float64); ok && float64(len([]rune(x))) > m {
		errs = append(errs, fmt.Sprintf("%s: longer than %v", at, m))
	}
	return errs
}

func (s *Schema) regexp(p string) (*regexp.Regexp, error) {
	if re, ok := s.re[p]; ok {
		return re, nil
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("compile %q: %w", p, err)
	}
	s.re[p] = re
	return re, nil
}

func (s *Schema) checkObject(x map[string]any, sch map[string]any, at string, depth int) []string {
	var errs []string
	if req, ok := sch["required"].([]any); ok {
		for _, r := range req {
			if _, present := x[fmt.Sprint(r)]; !present {
				errs = append(errs, fmt.Sprintf("%s: missing %v", at, r))
			}
		}
	}
	props, _ := sch["properties"].(map[string]any)
	keys := make([]string, 0, len(x))
	for k := range x {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if p, ok := props[k].(map[string]any); ok {
			errs = append(errs, s.check(x[k], p, at+"."+k, depth+1)...)
			continue
		}
		switch ap := sch["additionalProperties"].(type) {
		case bool:
			if !ap {
				errs = append(errs, fmt.Sprintf("%s: unexpected property %q", at, k))
			}
		case map[string]any:
			errs = append(errs, s.check(x[k], ap, at+"."+k, depth+1)...)
		}
	}
	return errs
}

func typeOK(v any, t string) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	}
	return true
}

func inEnum(v any, enum []any) bool {
	for _, e := range enum {
		if reflect.DeepEqual(v, e) {
			return true
		}
	}
	return false
}
