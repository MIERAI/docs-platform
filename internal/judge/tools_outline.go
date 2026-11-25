package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
)

const (
	maxOutlineEntries = 200
	maxConfigValues   = 40
)

var (
	reMDHeading   = regexp.MustCompile(`^#{1,6}\s`)
	reMDTableSep  = regexp.MustCompile(`^\|[\s:|-]+\|\s*$`)
	reYAMLKey     = regexp.MustCompile(`^(\s*)(-\s+)?([\w./-]+):`)
	reMakeTarget  = regexp.MustCompile(`^[\w.-]+\s*:|^[A-Z_]+\s*[?:]?=`)
	reGoDecl      = regexp.MustCompile(`^(func|type|const|var)\b|^\t\w+\s*=`)
	rePyDecl      = regexp.MustCompile(`^\s*(def|class)\s|^[A-Z_]+\s*=`)
	reTFBlock     = regexp.MustCompile(`^(resource|module|variable|output|data|locals|provider|terraform)\b`)
	reShellDecl   = regexp.MustCompile(`^\s*[\w-]+\(\)\s*\{|^\s*[\w|*-]+\)\s|^[A-Z_]+=`)
	reYAMLKeyLine = regexp.MustCompile(`^\s*-?\s*([\w./-]+)\s*:\s*(.*)$`)
)

func init() {
	moreTools["outline"] = func(_ context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Path string `json:"path"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.outline(a.Path)
	}
	moreTools["config_value"] = func(ctx context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Key string `json:"key"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.configValue(ctx, a.Key)
	}
}

// outline is the outline tool: a file's structure with line numbers (Markdown headings and
// tables, YAML keys, Makefile targets, Go/Python/Terraform/shell declarations); a directory
// is listed instead.
func (t *Tools) outline(p string) (any, error) {
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	if st, err := fs.Stat(t.root.FS(), c); err == nil && st.IsDir() {
		return t.listDir(c)
	}
	lines, err := t.fileLines(c)
	if err != nil {
		return nil, err
	}
	match := outlineMatcher(c)
	var out []string
	for i, l := range lines {
		if len(out) == maxOutlineEntries {
			break
		}
		if s := strings.TrimRight(l, " \t\r"); s != "" && match(s, lines, i) {
			out = append(out, fmt.Sprintf("%d: %s", i+1, cutStr(s, 160)))
		}
	}
	if len(out) == 0 {
		return &textResult{Text: fmt.Sprintf("(no structure recognised; %d lines) - use read_file", len(lines))}, nil
	}
	return &textResult{Text: fmt.Sprintf("outline of %s (%d lines, %d entries):\n%s", c, len(lines), len(out), strings.Join(out, "\n"))}, nil
}

// outlineByExt picks the structural lines of a file by its extension.
var outlineByExt = map[string]func(s string, lines []string, i int) bool{
	".md": mdOutline, ".markdown": mdOutline, ".yaml": yamlOutline, ".yml": yamlOutline, ".mk": lineRe(reMakeTarget),
	".go": lineRe(reGoDecl), ".py": lineRe(rePyDecl), ".tf": lineRe(reTFBlock), ".hcl": lineRe(reTFBlock), ".sh": lineRe(reShellDecl),
}

func outlineMatcher(p string) func(s string, lines []string, i int) bool {
	if path.Base(p) == "Makefile" {
		return lineRe(reMakeTarget)
	}
	if m, ok := outlineByExt[path.Ext(p)]; ok {
		return m
	}
	return func(s string, _ []string, _ int) bool {
		return !strings.HasPrefix(s, " ") && !strings.HasPrefix(s, "\t")
	}
}

func mdOutline(s string, lines []string, i int) bool {
	return reMDHeading.MatchString(s) || i+1 < len(lines) && strings.HasPrefix(s, "|") && reMDTableSep.MatchString(lines[i+1])
}

func yamlOutline(s string, _ []string, _ int) bool {
	m := reYAMLKey.FindStringSubmatch(s)
	return m != nil && len(m[1]) <= 6 && !strings.HasPrefix(strings.TrimSpace(s), "#")
}

func lineRe(re *regexp.Regexp) func(string, []string, int) bool {
	return func(s string, _ []string, _ int) bool { return re.MatchString(s) }
}

// configValue is the config_value tool: the values of a configuration key - YAML key paths
// (a.b.c, or a last segment), and assignments, flag defaults and "key: value" lines in code
// and config - each with path:line.
func (t *Tools) configValue(ctx context.Context, key string) (any, error) {
	key = strings.Trim(strings.TrimSpace(key), "`")
	if key == "" || len(key) > maxPatternBytes {
		return nil, errors.New("config_value: key is required")
	}
	last := key
	if i := strings.LastIndexAny(key, ".:/"); i >= 0 && i < len(key)-1 {
		last = key[i+1:]
	}
	var out []string
	for _, f := range t.TrackedFiles(ctx) {
		if len(out) >= maxConfigValues {
			break
		}
		if !t.allow(f) || fileKind(f) == "doc" {
			continue
		}
		lines, err := t.fileLines(f)
		if err != nil {
			continue
		}
		if ext := path.Ext(f); ext == ".yaml" || ext == ".yml" {
			out = append(out, yamlValues(f, lines, key)...)
			continue
		}
		out = append(out, assignedValues(f, lines, last)...)
	}
	if len(out) == 0 {
		return &textResult{Text: fmt.Sprintf("no definition/value found for `%s` (searched YAML key paths, assignments, flag/env defaults)", key)}, nil
	}
	return &textResult{Text: fmt.Sprintf("values of `%s` (%d):\n%s", key, len(out), strings.Join(out[:min(len(out), maxConfigValues)], "\n"))}, nil
}

// yamlValues finds key paths (dotted, by indentation) equal to or ending in key.
func yamlValues(f string, lines []string, key string) []string {
	type level struct {
		indent int
		key    string
	}
	var stack []level
	var out []string
	for i, raw := range lines {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		m := reYAMLKeyLine.FindStringSubmatch(raw)
		if m == nil {
			continue
		}
		ind := len(raw) - len(strings.TrimLeft(raw, " -"))
		for len(stack) > 0 && stack[len(stack)-1].indent >= ind {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, level{ind, m[1]})
		keys := make([]string, 0, len(stack))
		for _, s := range stack {
			keys = append(keys, s.key)
		}
		p := strings.Join(keys, ".")
		if p == key || strings.HasSuffix(p, "."+key) {
			v := strings.TrimSpace(m[2])
			if v == "" {
				v = "(mapping/list below)"
			}
			out = append(out, fmt.Sprintf("%s:%d: %s = %s", f, i+1, p, cutStr(v, 150)))
		}
	}
	return out
}

// assignedValues finds "key = v", "key := v", "key ?= v", `"key", default` and "key: v".
func assignedValues(f string, lines []string, key string) []string {
	q := regexp.QuoteMeta(key)
	res := []*regexp.Regexp{
		regexp.MustCompile(`\b` + q + `\s*(:?=|\?=)\s*\S`), regexp.MustCompile(`"` + q + `"\s*,\s*[^,)]+`),
		regexp.MustCompile(`\b` + q + `\s*:\s*\S`),
	}
	var out []string
	for i, l := range lines {
		if strings.Contains(l, key) && matchesAny(res, l) {
			out = append(out, fmt.Sprintf("%s:%d: %s", f, i+1, cutStr(strings.TrimSpace(l), 170)))
		}
	}
	return out
}
