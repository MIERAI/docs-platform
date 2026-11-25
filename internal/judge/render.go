package judge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MIERAI/docs-platform/judgev1"
)

// truncNote tells the model a result was cut.
const truncNote = "[truncated to fit the size limit: narrow the request (from_line/to_line, a narrower path or scope, max_results)]"

// Render turns a tool result into the plain text a model reads, the way a model's own file
// tools show it: file lines numbered ("12: text", the number is the line in the file),
// matches as "path:line: text", directory entries one per line ("dir/" for directories),
// commits as "sha date subject". Nothing is escaped. The number shown is the number to cite:
// a model never has to count lines.
func Render(tool string, content json.RawMessage, truncated bool) string {
	var b strings.Builder
	switch tool {
	case judgev1.ToolReadFile:
		renderLines(&b, content)
	case judgev1.ToolGitShow:
		renderShow(&b, content)
	case judgev1.ToolGrep, judgev1.ToolSearch:
		renderMatches(&b, content)
	case judgev1.ToolListDir:
		renderDir(&b, content)
	case judgev1.ToolGlob:
		var r globResult
		_ = json.Unmarshal(content, &r)
		writeList(&b, r.Paths, "no files match")
	case judgev1.ToolGitLog:
		renderLog(&b, content)
	case judgev1.ToolGitChanges:
		renderChanges(&b, content)
	default: // text results (changes_since, find_references, check_quote, outline, config_value)
		var r textResult
		if json.Unmarshal(content, &r) == nil && r.Text != "" {
			b.WriteString(strings.TrimRight(r.Text, "\n"))
		} else {
			b.Write(content)
		}
	}
	if truncated {
		b.WriteString("\n" + truncNote)
	}
	return b.String()
}

func renderLines(b *strings.Builder, content json.RawMessage) {
	var r linesResult
	_ = json.Unmarshal(content, &r)
	at := r.Path
	if r.Rev != "" {
		at += " @ " + r.Rev
	}
	fmt.Fprintf(b, "%s (lines %d-%d of %d)", at, r.FromLine, r.ToLine, r.TotalLines)
	for i, l := range r.Lines {
		fmt.Fprintf(b, "\n%d: %s", r.FromLine+i, l)
	}
	if r.ToLine < r.TotalLines {
		fmt.Fprintf(b, "\n[more: read from_line %d]", r.ToLine+1)
	}
}

func renderShow(b *strings.Builder, content json.RawMessage) {
	var probe struct {
		Lines []string `json:"lines"`
	}
	if json.Unmarshal(content, &probe) == nil && probe.Lines != nil {
		renderLines(b, content)
		return
	}
	var r showResult
	_ = json.Unmarshal(content, &r)
	b.WriteString(strings.TrimRight(r.Text, "\n"))
}

func renderMatches(b *strings.Builder, content json.RawMessage) {
	var r searchResult
	_ = json.Unmarshal(content, &r)
	if len(r.Matches) == 0 {
		b.WriteString("no matches")
		if r.FilesSearched > 0 {
			fmt.Fprintf(b, " (%d files searched)", r.FilesSearched)
		}
		return
	}
	for i, m := range r.Matches {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "%s:%d: %s", m.Path, m.Line, m.Text)
	}
}

func renderDir(b *strings.Builder, content json.RawMessage) {
	var r listResult
	_ = json.Unmarshal(content, &r)
	names := make([]string, 0, len(r.Entries))
	for _, e := range r.Entries {
		n := e.Name
		if e.Type == "dir" {
			n += "/"
		}
		names = append(names, n)
	}
	writeList(b, names, "(empty)")
}

func renderLog(b *strings.Builder, content json.RawMessage) {
	var r logResult
	_ = json.Unmarshal(content, &r)
	lines := make([]string, 0, len(r.Commits))
	for _, c := range r.Commits {
		lines = append(lines, fmt.Sprintf("%s %s %s", c.SHA, shortDate(c.Date), c.Subject))
	}
	writeList(b, lines, "no commits")
}

func renderChanges(b *strings.Builder, content json.RawMessage) {
	var r changesResult
	_ = json.Unmarshal(content, &r)
	lines := make([]string, 0, len(r.Changes))
	for _, c := range r.Changes {
		l := fmt.Sprintf("%s %s %s | %s %s", c.SHA, c.Date, c.Subject, c.Status, c.Path)
		if c.NewPath != "" {
			l += " -> " + c.NewPath
		}
		lines = append(lines, l)
	}
	writeList(b, lines, "none")
}

func writeList(b *strings.Builder, items []string, empty string) {
	if len(items) == 0 {
		b.WriteString(empty)
		return
	}
	b.WriteString(strings.Join(items, "\n"))
}

func shortDate(d string) string {
	if len(d) >= 10 {
		return d[:10]
	}
	return d
}
