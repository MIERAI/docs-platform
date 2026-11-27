package draftverify

import (
	"fmt"
	"regexp"
	"strings"
)

// checkTOML is a structural TOML check (the module keeps a single dependency, so there is
// no TOML library): every line is blank, a comment, a [table] / [[array]] header or a
// key = value pair whose strings are closed and whose brackets balance (arrays and
// multi-line strings may span lines). It catches what a one-line draft can break; it is
// not a full TOML parser.
func checkTOML(text string) error {
	t := tomlState{}
	for i, l := range splitLines(text) {
		if err := t.line(l); err != nil {
			return fmt.Errorf("toml line %d: %w", i+1, err)
		}
	}
	switch {
	case t.multi != "":
		return fmt.Errorf("toml: multi-line string %s is never closed", t.multi)
	case t.depth > 0:
		return fmt.Errorf("toml: %d bracket(s) never closed", t.depth)
	}
	return nil
}

var (
	reTOMLTable = regexp.MustCompile(`^\[\[?\s*[A-Za-z0-9_\-."' ]+?\s*\]\]?$`)
	reTOMLKey   = regexp.MustCompile(`^[A-Za-z0-9_\-."' ]+$`)
	reTOMLBare  = regexp.MustCompile(`^[+\-]?[0-9A-Za-z_.:+\- ]+$`)
)

type tomlState struct {
	multi string // the open multi-line string delimiter
	depth int    // open brackets of a multi-line array or inline table
}

func (t *tomlState) line(l string) error {
	if t.multi != "" {
		if i := strings.Index(l, t.multi); i >= 0 {
			rest := l[i+len(t.multi):]
			t.multi = ""
			return t.value(rest, true)
		}
		return nil
	}
	if t.depth > 0 {
		return t.value(l, true)
	}
	s := strings.TrimSpace(stripTOMLComment(l))
	switch {
	case s == "":
		return nil
	case strings.HasPrefix(s, "["):
		if !reTOMLTable.MatchString(s) {
			return fmt.Errorf("malformed table header %q", s)
		}
		return nil
	}
	k, v, ok := strings.Cut(s, "=")
	if !ok || !reTOMLKey.MatchString(strings.TrimSpace(k)) || strings.TrimSpace(k) == "" {
		return fmt.Errorf("expected key = value or [table], got %q", s)
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("key %s has no value", strings.TrimSpace(k))
	}
	return t.value(v, false)
}

// value scans a value (or the continuation of one): strings must close, brackets balance.
func (t *tomlState) value(v string, cont bool) error {
	v = strings.TrimSpace(v)
	if !cont && !strings.ContainsAny(v[:1], `"'[{`) {
		if !reTOMLBare.MatchString(stripTOMLComment(v)) {
			return fmt.Errorf("malformed value %q", v)
		}
		return nil
	}
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '#':
			return nil
		case '[', '{':
			t.depth++
		case ']', '}':
			t.depth--
			if t.depth < 0 {
				return fmt.Errorf("unbalanced %q", c)
			}
		case '"', '\'':
			n, err := t.str(v[i:])
			if err != nil || t.multi != "" {
				return err
			}
			i += n - 1
		}
	}
	return nil
}

// str returns the length of the string starting at s (opening a multi-line one when its
// delimiter is not closed on the line).
func (t *tomlState) str(s string) (int, error) {
	q := s[:1]
	if strings.HasPrefix(s, q+q+q) {
		if j := strings.Index(s[3:], q+q+q); j >= 0 {
			return 3 + j + 3, nil
		}
		t.multi = q + q + q
		return len(s), nil
	}
	for j := 1; j < len(s); j++ {
		switch {
		case q == `"` && s[j] == '\\':
			j++
		case s[j] == q[0]:
			return j + 1, nil
		}
	}
	return 0, fmt.Errorf("string %s is not closed", s)
}

func stripTOMLComment(l string) string {
	inStr := byte(0)
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case inStr != 0 && c == '\\' && inStr == '"':
			i++
		case inStr != 0 && c == inStr:
			inStr = 0
		case inStr == 0 && (c == '"' || c == '\''):
			inStr = c
		case inStr == 0 && c == '#':
			return l[:i]
		}
	}
	return l
}
