package refclass

import (
	"path"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

// wrappers are commands that run another command; the program is the word after them.
var wrappers = map[string]bool{"sudo": true, "env": true, "time": true, "exec": true, "nohup": true, "command": true}

// interpreters run a script given as their first argument.
var interpreters = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "python": true, "python3": true, "node": true, "pwsh": true,
}

// CommandLine classifies one shell command line (possibly several commands joined by
// && || ; |). base is the confidence of the surrounding context.
func CommandLine(line string, base model.Confidence) []Candidate {
	return (&Session{}).CommandLine(line, base)
}

// Session carries shell state (the working directory after `cd`) across the lines of a block.
type Session struct {
	cwd string
}

// CommandLine classifies one command line within the session.
func (s *Session) CommandLine(line string, base model.Confidence) []Candidate {
	var out []Candidate
	for _, cmd := range splitCommands(Words(stripComment(line))) {
		if len(cmd) == 2 && cmd[0] == "cd" {
			s.chdir(cmd[1])
			continue
		}
		for _, c := range command(cmd, base) {
			if c.Kind == model.KindFilePath && s.cwd != "" && !strings.HasPrefix(c.Text, "/") {
				c.Text = path.Join(s.cwd, c.Text)
			}
			out = append(out, c)
		}
	}
	return out
}

func (s *Session) chdir(dir string) {
	switch {
	case strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "~") || strings.HasPrefix(dir, "$") || dir == "-":
		s.cwd = "" // left the repository (or unknown): stop rewriting
	default:
		s.cwd = path.Clean(path.Join(s.cwd, dir))
		if s.cwd == "." {
			s.cwd = ""
		}
	}
}

func command(words []string, base model.Confidence) []Candidate {
	var out []Candidate
	i := 0
	for i < len(words) {
		if m := reEnvAssign.FindStringSubmatch(words[i]); m != nil {
			out = append(out, Candidate{Kind: model.KindEnvVar, Text: m[1], Confidence: base})
		} else if !wrappers[words[i]] {
			break
		}
		i++
	}
	if i >= len(words) {
		return out
	}
	prog, rest, pc := program(words[i:], base)
	out = append(out, pc...)
	if prog == "make" {
		return append(out, makeTargets(rest)...)
	}
	return append(out, arguments(prog, rest, base)...)
}

// program identifies the program a command runs and returns the remaining words.
func program(words []string, base model.Confidence) (string, []string, []Candidate) {
	w := words[0]
	switch {
	case interpreters[w] && len(words) > 1 && looksLikeScript(words[1]):
		return scriptName(words[1]), words[2:], []Candidate{{Kind: model.KindFilePath, Text: words[1], Confidence: base}}
	case w == "go" && len(words) > 2 && words[1] == "run" && strings.HasPrefix(words[2], "./"):
		return path.Base(strings.TrimSuffix(words[2], "/")), words[3:], []Candidate{{Kind: model.KindFilePath, Text: words[2], Confidence: base}}
	case looksLikeScript(w) && strings.Contains(w, "/"):
		return scriptName(w), words[1:], []Candidate{{Kind: model.KindFilePath, Text: w, Confidence: model.ConfHigh}}
	}
	return w, words[1:], nil
}

func looksLikeScript(w string) bool {
	return rePathChars.MatchString(w) && (strings.HasPrefix(w, "./") || HasKnownExt(w) || strings.Contains(w, "/"))
}

func scriptName(w string) string {
	b := path.Base(w)
	return strings.TrimSuffix(b, path.Ext(b))
}

func makeTargets(rest []string) []Candidate {
	var out []Candidate
	for _, w := range rest {
		if strings.HasPrefix(w, "-") || strings.Contains(w, "=") || !rePathChars.MatchString(w) {
			continue
		}
		out = append(out, Candidate{Kind: model.KindMakeTarget, Text: w, Program: "make", Confidence: model.ConfHigh})
	}
	return out
}

// arguments classifies flags, the first positional word (a sub-command candidate), repo paths
// and $VARS used in the arguments.
func arguments(prog string, rest []string, base model.Confidence) []Candidate {
	var out []Candidate
	sawPositional, skipNext := false, false
	for _, w := range rest {
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case w == "--":
			return out
		case strings.HasPrefix(w, ">"):
			skipNext = w == ">" || w == ">>" // "> file": the file is created by the command
		case isOutputFlag(w):
			skipNext = true
			out = append(out, Candidate{Kind: model.KindCLIFlag, Text: w, Program: prog, Confidence: base})
		case reLongFlag.MatchString(w):
			out = append(out, Candidate{Kind: model.KindCLIFlag, Text: "--" + reLongFlag.FindStringSubmatch(w)[1], Program: prog, Confidence: base})
		case strings.HasPrefix(w, "$"):
			out = append(out, envInWord(w, base)...)
		case !sawPositional && isWord(w):
			sawPositional = true
			out = append(out, Candidate{Kind: model.KindCLICommand, Text: w, Program: prog, Confidence: base})
		case looksLikeScript(w) && strings.Contains(w, "/") && !reScheme.MatchString(w):
			sawPositional = true
			out = append(out, Candidate{Kind: model.KindFilePath, Text: w, Confidence: model.ConfLow})
		default:
			sawPositional = sawPositional || !strings.HasPrefix(w, "-")
		}
	}
	return out
}

// isOutputFlag: the argument after it names a file the command creates, which need not exist.
func isOutputFlag(w string) bool {
	if w == "-o" {
		return true
	}
	if !strings.HasPrefix(w, "--") || strings.Contains(w, "=") {
		return false
	}
	return strings.Contains(w, "out") || strings.Contains(w, "export") || strings.Contains(w, "dest")
}

func isWord(w string) bool {
	if w == "" || len(w) > 40 {
		return false
	}
	for _, r := range w {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return w[0] >= 'a' && w[0] <= 'z'
}

func envInWord(w string, base model.Confidence) []Candidate {
	m := reEnvRef.FindStringSubmatch(w)
	if m == nil || !reUpperSnk.MatchString(m[1]) && strings.ToUpper(m[1]) != m[1] {
		return nil
	}
	return []Candidate{{Kind: model.KindEnvVar, Text: m[1], Confidence: min(base, model.ConfMedium)}}
}
