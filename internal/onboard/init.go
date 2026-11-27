// Package onboard lowers the adoption barrier: `docsp init` inspects a repository and proposes
// a .docsp.yaml whose every guess carries the reason it was made, and `docsp doctor` checks
// offline that the first CI run will be green. Neither runs a model or anything from the
// inspected repository.
package onboard

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// AIInstructionFiles are the instruction files of AI coding assistants. They describe the
// code to an agent the way a README describes it to a person, and rot the same way, so the
// proposed configuration always checks them (a pattern matching nothing yet costs nothing).
var AIInstructionFiles = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"GEMINI.md",
	".github/copilot-instructions.md",
	".github/instructions/*.instructions.md",
	".cursorrules",
	".cursor/rules/*.mdc",
	".windsurfrules",
	".clinerules",
}

// Guess is one proposed value with the reason it was proposed.
type Guess struct {
	Value string
	Why   string
}

// Rule is a proposed impact rule.
type Rule struct {
	Name       string
	Code, Docs []string
	Why        string
}

// Proposal is the configuration docsp init proposes.
type Proposal struct {
	Docs    []Guess
	Exclude []Guess
	Rules   []Rule
	// DocFiles is how many files the proposed docs globs match (after the excludes).
	DocFiles int
}

// excludeCandidates are directories and files that are rarely documentation to keep true.
var excludeCandidates = []struct{ pattern, why string }{
	{"testdata/", "test fixtures are not documentation"},
	{"third_party/", "vendored third-party code keeps its own docs"},
	{"CHANGELOG*.md", "a changelog records the past; it is not expected to match the code"},
	{"LICENSE*", "license texts are not about the code"},
	{"**/testdata/", "nested test fixtures are not documentation"},
	{"**/fixtures/", "test fixtures are not documentation"},
}

// Inspect proposes a configuration for the repository at root. It only lists files.
func Inspect(root string) (*Proposal, error) {
	r, err := repo.Open(root, nil)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	files := r.Files()
	p := &Proposal{}
	covered := 0 // files the top-level testdata/ already excludes
	for _, c := range excludeCandidates {
		n := countMatching([]string{c.pattern}, files)
		if c.pattern == "testdata/" {
			covered = n
		}
		if c.pattern == "**/testdata/" && n == covered {
			continue
		}
		if n > 0 {
			p.Exclude = append(p.Exclude, Guess{Value: c.pattern, Why: fmt.Sprintf("%s (%s)", c.why, plural(n, "file"))})
		}
	}
	excl := values(p.Exclude)
	kept := make([]string, 0, len(files))
	for _, f := range files {
		if !repo.MatchAny(excl, f) && !underExcluded(excl, f) {
			kept = append(kept, f)
		}
	}
	p.Docs = docGuesses(kept)
	p.DocFiles = countMatching(values(p.Docs), kept)
	p.Rules = ruleGuesses(r, kept)
	return p, nil
}

func underExcluded(excl []string, f string) bool {
	for d := path.Dir(f); d != "."; d = path.Dir(d) {
		if repo.MatchAny(excl, d+"/") || repo.MatchAny(excl, d) {
			return true
		}
	}
	return false
}

// docGuesses: the Markdown files at the root, one docs/** glob per top-level directory that
// holds Markdown, and the AI-instruction files (always).
func docGuesses(files []string) []Guess {
	var rootMD []string
	dirs := map[string]int{}
	for _, f := range files {
		if !markdown(f) || aiFile(f) {
			continue
		}
		top, _, nested := strings.Cut(f, "/")
		if !nested {
			rootMD = append(rootMD, f)
			continue
		}
		if !strings.HasPrefix(top, ".") {
			dirs[top]++
		}
	}
	var out []Guess
	if len(rootMD) > 0 {
		out = append(out, Guess{Value: "/*.md", Why: fmt.Sprintf("%s at the root: %s", plural(len(rootMD), "Markdown file"), listShort(rootMD))})
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, d := range names {
		out = append(out, Guess{Value: d + "/**/*.md", Why: fmt.Sprintf("%s/ contains %s", d, plural(dirs[d], ".md file"))})
	}
	for _, a := range AIInstructionFiles {
		n := countMatching([]string{a}, files)
		why := "AI-assistant instructions describe the code too; none yet, checked once added"
		if n > 0 {
			why = "found " + listShort(matching(a, files)) + ": AI-assistant instructions describe the code too"
		}
		out = append(out, Guess{Value: a, Why: why})
	}
	return out
}

func markdown(f string) bool {
	e := strings.ToLower(path.Ext(f))
	return e == ".md" || e == ".markdown"
}

func aiFile(f string) bool { return repo.MatchAny(AIInstructionFiles, f) }

// ruleGuesses maps command-line entry points to the README (an explicit impact rule).
func ruleGuesses(r *repo.Repo, files []string) []Rule {
	if !r.IsDir("cmd") || !r.IsFile("README.md") {
		return nil
	}
	n := countMatching([]string{"cmd/**"}, files)
	if n == 0 {
		return nil
	}
	return []Rule{{
		Name: "cli", Code: []string{"cmd/**"}, Docs: []string{"README.md"},
		Why: fmt.Sprintf("cmd/ holds the command-line entry points (%s); README.md is where users read about them", plural(n, "file")),
	}}
}

func matching(pattern string, files []string) []string {
	var out []string
	for _, f := range files {
		if repo.Match(pattern, f) {
			out = append(out, f)
		}
	}
	return out
}

func countMatching(patterns, files []string) int {
	n := 0
	for _, f := range files {
		if repo.MatchAny(patterns, f) {
			n++
		}
	}
	return n
}

func values(gs []Guess) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Value)
	}
	return out
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func listShort(fs []string) string {
	if len(fs) <= 3 {
		return strings.Join(fs, ", ")
	}
	return strings.Join(fs[:3], ", ") + fmt.Sprintf(" and %d more", len(fs)-3)
}

// YAML renders the proposal as a .docsp.yaml with a comment on every guess.
func (p *Proposal) YAML() string {
	var b strings.Builder
	b.WriteString("# .docsp.yaml proposed by `docsp init`; every guess says why it was made. Review and edit.\n")
	b.WriteString("# Contract: docsp/config/v1 (all fields optional). Check it with `docsp doctor`.\n")
	b.WriteString("version: 1\n")
	fmt.Fprintf(&b, "# these globs match %s now\n", plural(p.DocFiles, "documentation file"))
	b.WriteString("docs:\n")
	for _, g := range p.Docs {
		fmt.Fprintf(&b, "  - %s # %s\n", quote(g.Value), g.Why)
	}
	if len(p.Exclude) > 0 {
		b.WriteString("exclude:\n")
		for _, g := range p.Exclude {
			fmt.Fprintf(&b, "  - %s # %s\n", quote(g.Value), g.Why)
		}
	}
	b.WriteString("min_confidence: medium # report what the reference check is reasonably sure of\n")
	if len(p.Rules) > 0 {
		b.WriteString("impact:\n  rules:\n")
		for _, r := range p.Rules {
			fmt.Fprintf(&b, "    # %s\n", r.Why)
			fmt.Fprintf(&b, "    - name: %s\n      code: [%s]\n      docs: [%s]\n", r.Name, quoteAll(r.Code), quoteAll(r.Docs))
		}
	}
	return b.String()
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

func quoteAll(ss []string) string {
	q := make([]string, 0, len(ss))
	for _, s := range ss {
		q = append(q, quote(s))
	}
	return strings.Join(q, ", ")
}

// Validate parses the rendered proposal with the real configuration loader.
func (p *Proposal) Validate() error {
	_, err := config.Parse([]byte(p.YAML()))
	return err //nolint:wrapcheck // already descriptive
}
