package draftverify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/MIERAI/docs-platform/internal/extract/markdown"
)

// formatProblem is one parse error; key identifies the construct across the old and the new
// text, so that an error the document already had is not blamed on the draft.
type formatProblem struct {
	key    string
	line   int
	detail string
}

// formatGate: the edited document still parses. Markdown is read with the repository's own
// Markdown parser (unclosed fences, front matter, unbalanced inline code or links on changed
// lines), fenced blocks of known languages are parsed or lexed (Go with go/parser, YAML, JSON,
// TOML, shell with sh -n), and YAML/JSON/TOML documents are parsed whole. Only errors the old
// text did not have fail the gate.
func formatGate(ctx context.Context, p, oldText, newText string, changed map[int]bool) Gate {
	g := Gate{Name: GateFormat}
	kind := docKind(p)
	if kind == "" {
		g.Status, g.Detail = NotApplicable, "no parser for "+path.Ext(p)+" files"
		return g
	}
	before := map[string]bool{}
	for _, f := range parseDoc(ctx, kind, oldText, nil) {
		before[f.key] = true
	}
	for _, f := range parseDoc(ctx, kind, newText, changed) {
		it := Item{Line: f.line, Kind: f.key, Status: Fail, Detail: f.detail}
		if before[f.key] {
			it.Status, it.Detail = Pass, "already broken before the draft: "+f.detail
		}
		g.Items = append(g.Items, it)
	}
	g.Status = worst(g.Items, Pass)
	g.Detail = kind + " parses"
	if g.Status == Fail {
		g.Detail = "the draft breaks the " + kind + " syntax"
	}
	return g
}

func docKind(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	}
	return ""
}

func parseDoc(ctx context.Context, kind, text string, changed map[int]bool) []formatProblem {
	if kind != "markdown" {
		if err := parseData(kind, text); err != nil {
			return []formatProblem{{key: kind, detail: err.Error()}}
		}
		return nil
	}
	return parseMarkdown(ctx, text, changed)
}

// parseData parses a whole YAML, JSON or TOML text.
func parseData(kind, text string) error {
	switch kind {
	case "yaml":
		d := yaml.NewDecoder(strings.NewReader(text))
		for {
			var v yaml.Node
			err := d.Decode(&v)
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("yaml: %w", err)
			}
		}
	case "json":
		return parseJSON(text)
	case "toml":
		return checkTOML(text)
	}
	return nil
}

func parseJSON(text string) error {
	var v any
	err := json.Unmarshal([]byte(text), &v)
	if err == nil {
		return nil
	}
	lines := splitLines(text)
	if len(lines) < 2 {
		return fmt.Errorf("json: %w", err)
	}
	for _, l := range lines { // JSON Lines
		if strings.TrimSpace(l) != "" && !json.Valid([]byte(l)) {
			return fmt.Errorf("json: %w", err)
		}
	}
	return nil
}

// Fence is a fenced code block with its info string.
type Fence struct {
	Line  int // the opening fence line (1-based)
	End   int // the closing fence line (0 = unclosed)
	Lang  string
	Info  []string // words after the language
	Lines []string
}

// fences lists the fenced code blocks of a Markdown text (the repository's parser).
func fences(text string) []Fence {
	all := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []Fence
	for _, b := range markdown.Parse([]byte(text)).Blocks {
		f := Fence{Line: b.Line - 1, Lang: strings.ToLower(b.Lang), Lines: b.Lines}
		if end := b.Line + len(b.Lines); end <= len(all) {
			f.End = end
		}
		if f.Line >= 1 {
			open := strings.TrimLeft(all[f.Line-1], " ")
			info := strings.TrimLeft(open, "`~")
			if ws := strings.Fields(strings.NewReplacer("{", " ", "}", " ", ",", " ").Replace(info)); len(ws) > 1 {
				f.Info = ws[1:]
			}
		}
		out = append(out, f)
	}
	return out
}

func (f Fence) has(word string) bool {
	for _, w := range f.Info {
		if strings.EqualFold(strings.TrimPrefix(w, "."), word) {
			return true
		}
	}
	return false
}

func (f Fence) touches(changed map[int]bool) bool {
	end := f.End
	if end == 0 {
		end = f.Line + len(f.Lines)
	}
	for l := f.Line; l <= end; l++ {
		if changed[l] {
			return true
		}
	}
	return false
}

func parseMarkdown(ctx context.Context, text string, changed map[int]bool) []formatProblem {
	var out []formatProblem
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if fm, ok := frontMatter(lines); ok {
		var v any
		if err := yaml.Unmarshal([]byte(fm), &v); err != nil {
			out = append(out, formatProblem{key: "front-matter", line: 1, detail: "front matter: " + err.Error()})
		}
	} else if fm != "" {
		out = append(out, formatProblem{key: "front-matter", line: 1, detail: fm})
	}
	inBlock := map[int]bool{}
	for i, f := range fences(text) {
		for l := f.Line; l <= max(f.End, f.Line+len(f.Lines)); l++ {
			inBlock[l] = true
		}
		key := "block-" + strconv.Itoa(i) + "-" + f.Lang
		if f.End == 0 {
			out = append(out, formatProblem{key: key + "-unclosed", line: f.Line, detail: "code fence opened here is never closed"})
		}
		if err := lexBlock(ctx, f.Lang, strings.Join(f.Lines, "\n")); err != nil {
			out = append(out, formatProblem{key: key, line: f.Line, detail: f.Lang + " block: " + err.Error()})
		}
	}
	return append(out, inlineProblems(lines, changed, inBlock)...)
}

// frontMatter returns YAML front matter; ok false with a message when it is not closed.
func frontMatter(lines []string) (string, bool) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}
	for j := 1; j < len(lines); j++ {
		if t := strings.TrimSpace(lines[j]); t == "---" || t == "..." {
			return strings.Join(lines[1:j], "\n"), true
		}
	}
	return "front matter opened on line 1 is never closed", false
}

// inlineProblems: unbalanced inline code or an unclosed link on a changed prose line. The
// key holds the line's text so that an unchanged broken line is not the draft's fault.
func inlineProblems(lines []string, changed, inBlock map[int]bool) []formatProblem {
	var out []formatProblem
	for n, l := range lines {
		ln := n + 1
		if changed != nil && !changed[ln] || inBlock[ln] {
			continue
		}
		if strings.Count(l, "`")%2 == 1 && !strings.Contains(l, "``") {
			out = append(out, formatProblem{key: "inline-code:" + strings.TrimSpace(l), line: ln, detail: "unbalanced inline code (odd number of backticks)"})
		}
		if i := strings.LastIndex(l, "]("); i >= 0 && !strings.Contains(l[i:], ")") {
			out = append(out, formatProblem{key: "link:" + strings.TrimSpace(l), line: ln, detail: "link destination is not closed"})
		}
	}
	return out
}

// lexBlock parses (or, for shell, lexes) a fenced block of a known language.
func lexBlock(ctx context.Context, lang, src string) error {
	switch lang {
	case "go", "golang":
		return parseGo(src)
	case "yaml", "yml", "json", "toml":
		if lang == "yml" {
			lang = "yaml"
		}
		return parseData(lang, src)
	case "sh", "shell", "bash", "zsh", "console", "shell-session":
		return lexShell(ctx, lang, src)
	}
	return nil
}

// parseGo accepts a whole file, declarations without a package clause, or statements.
func parseGo(src string) error {
	fs := token.NewFileSet()
	_, err := parser.ParseFile(fs, "block.go", src, parser.SkipObjectResolution)
	if err == nil {
		return nil
	}
	for _, wrap := range []string{"package p\n%s\n", "package p\nfunc _() {\n%s\n}\n"} {
		if _, e := parser.ParseFile(token.NewFileSet(), "block.go", fmt.Sprintf(wrap, src), parser.SkipObjectResolution); e == nil {
			return nil
		}
	}
	return fmt.Errorf("go: %w", err)
}

// shellScript is the script of a shell block: for console blocks only the "$ " lines.
func shellScript(lang, src string) string {
	if lang != "console" && lang != "shell-session" {
		return src
	}
	var b strings.Builder
	for _, l := range strings.Split(src, "\n") {
		if t := strings.TrimLeft(l, " "); strings.HasPrefix(t, "$ ") {
			b.WriteString(strings.TrimPrefix(t, "$ ") + "\n")
		}
	}
	return b.String()
}

// lexShell runs "<shell> -n" (parse only, nothing is executed) on the block. A missing shell
// is not an error: the block is not checked.
func lexShell(ctx context.Context, lang, src string) error {
	name := "sh"
	if lang == "bash" || lang == "zsh" {
		name = lang
	}
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil //nolint:nilerr // no such shell on this host: the block is not lexed
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-n") //nolint:gosec // parse-only (-n) shell from PATH, the block on stdin
	cmd.Stdin = strings.NewReader(shellScript(lang, src))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(name + " -n: " + firstLine(msg))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
