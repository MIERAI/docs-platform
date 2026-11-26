package contract

import (
	"fmt"
	"strings"
)

// Helpers over yaml.v3's generic decoding (map[string]any, []any, scalars).

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func mapOf(m map[string]any, key string) map[string]any { return asMap(m[key]) }

func listOf(m map[string]any, key string) []any {
	l, _ := m[key].([]any)
	return l
}

func str(m map[string]any, key string) string { return scalar(m[key]) }

// scalar renders a scalar as text ("" for nil, maps and lists).
func scalar(v any) string {
	switch x := v.(type) {
	case nil, map[string]any, []any:
		return ""
	case string:
		return strings.TrimSpace(x)
	default:
		return fmt.Sprint(x)
	}
}

// yes reads the design language's booleans (是 / 否, true / false).
func yes(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "是" || s == "true" || s == "yes"
	}
	return false
}
