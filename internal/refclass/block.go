package refclass

import (
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

// Located is a candidate with a 0-based line offset inside its block.
type Located struct {
	Offset int
	Candidate
}

var shellLangs = map[string]bool{"sh": true, "bash": true, "shell": true, "zsh": true, "console": true, "shell-session": true}

// IsShell reports whether a fenced-block info string denotes shell commands.
func IsShell(lang string) bool { return shellLangs[strings.ToLower(lang)] }

// Block classifies the lines of a fenced code block. Shell blocks are parsed as commands;
// unlabeled blocks are parsed the same way at low confidence; other languages yield nothing.
func Block(lang string, lines []string) []Located {
	lang = strings.ToLower(lang)
	base := model.ConfMedium
	switch {
	case shellLangs[lang]:
	case lang == "" || lang == "text":
		base = model.ConfLow
	default:
		return nil
	}
	prompted := hasPrompt(lines)
	sess := &Session{}
	var out []Located
	for _, lc := range joinContinuations(lines) {
		off, line := lc.offset, strings.TrimSpace(lc.text)
		if prompted {
			var ok bool
			if line, ok = cutPrompt(line); !ok {
				continue // command output, not a command
			}
		}
		for _, c := range sess.CommandLine(line, base) {
			out = append(out, Located{Offset: off, Candidate: c})
		}
	}
	return out
}

func hasPrompt(lines []string) bool {
	for _, l := range lines {
		if _, ok := cutPrompt(strings.TrimSpace(l)); ok {
			return true
		}
	}
	return false
}

func cutPrompt(l string) (string, bool) {
	for _, p := range []string{"$ ", "% ", "> "} {
		if after, ok := strings.CutPrefix(l, p); ok {
			return after, true
		}
	}
	return l, false
}

type logicalLine struct {
	offset int
	text   string
}

// joinContinuations merges backslash-continued lines, keeping the starting line offset.
func joinContinuations(lines []string) []logicalLine {
	var out []logicalLine
	start := -1
	var cur strings.Builder
	for i, l := range lines {
		if start < 0 {
			start = i
		}
		t := strings.TrimRight(l, " \t")
		if strings.HasSuffix(t, "\\") {
			cur.WriteString(strings.TrimSuffix(t, "\\"))
			cur.WriteByte(' ')
			continue
		}
		cur.WriteString(t)
		out = append(out, logicalLine{start, cur.String()})
		cur.Reset()
		start = -1
	}
	if start >= 0 {
		out = append(out, logicalLine{start, cur.String()})
	}
	return out
}
