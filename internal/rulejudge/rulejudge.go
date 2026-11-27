// Package rulejudge is the deterministic judging stage that runs before the model ("deterministic
// first"): a documentation line that states one typed fact of Go code - a flag's default, an
// exported constant, an environment variable's default, a config field's constructor default,
// an HTTP route - and names exactly one such fact is decided from the code itself (go/ast, no
// model): confirmed when the values agree and the line says nothing else, contradicted when
// they differ. Its evidence is the exact source line(s), so quote validation and ledger
// invalidation work as for a model's verdict; a contradicted value gets a draft that replaces
// only that token. Anything unclear - several subjects, a value that is not a literal, a
// qualifier ("in CI", "previously"), facts that disagree, a sentinel zero default - is left
// to the model unchanged.
package rulejudge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/extract/golang"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Model is what the ledger records as the "model" of a rules verdict.
const Model = agentic.RulesModelPrefix + "go-ast"

// Version of the rules; PromptHash (the ledger's prompt_hash) changes with it.
const Version = "docsp-rules-go/1"

// rulesText describes the rules (hashed into PromptHash: a change of the rules is visible in
// the ledger like a change of a prompt).
const rulesText = `one subject per line (flag | exported const | env var | config field | route);
value after "default"/"defaults to"/"(default: X)", or "is"/"=" for constants; literals only;
strings as code or quoted; zero code defaults are sentinels; facts must agree; qualifiers and
history documents are skipped; confirmed only when every other word is covered by the fact's
own usage/doc words.`

// PromptHash identifies the rule version in the ledger.
func PromptHash() string {
	h := sha256.Sum256([]byte(Version + "\x00" + rulesText))
	return hex.EncodeToString(h[:])[:16]
}

// maxQuoted bounds the facts quoted as evidence.
const maxQuoted = 3

// Stage decides documentation lines from the typed facts of a repository's Go code.
type Stage struct {
	byFlag map[string][]golang.Fact
	byName map[string][]golang.Fact
	routes map[string][]golang.Fact
	lines  map[string][]string
	facts  int
}

// New builds a stage from facts; lines are the source files the facts were read from.
func New(facts []golang.Fact, lines map[string][]string) *Stage {
	s := &Stage{
		byFlag: map[string][]golang.Fact{}, byName: map[string][]golang.Fact{}, routes: map[string][]golang.Fact{},
		lines: lines, facts: len(facts),
	}
	for _, f := range facts {
		switch f.Kind {
		case golang.FactFlag:
			s.byFlag[f.Name] = append(s.byFlag[f.Name], f)
			for _, a := range f.Aliases {
				s.byFlag[a] = append(s.byFlag[a], f)
			}
		case golang.FactRoute:
			s.routes[f.Name] = append(s.routes[f.Name], f)
		case golang.FactConst, golang.FactEnv, golang.FactField:
			if f.Kind != golang.FactField || f.Exported { // an unexported field is no documented surface; its tags may be
				s.byName[f.Name] = append(s.byName[f.Name], f)
			}
			for _, a := range f.Aliases {
				s.byName[a] = append(s.byName[a], f)
			}
		}
	}
	return s
}

// Load reads the Go files among paths (test files and testdata are skipped) through read.
func Load(read func(string) ([]byte, error), paths []string) *Stage {
	var srcs []golang.Source
	lines := map[string][]string{}
	for _, p := range paths {
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains("/"+p, "/testdata/") {
			continue
		}
		b, err := read(p)
		if err != nil {
			continue
		}
		srcs = append(srcs, golang.Source{Path: p, Src: b})
		lines[p] = strings.Split(string(b), "\n")
	}
	return New(golang.ExtractFacts(srcs), lines)
}

// LoadDir loads the stage of a worktree (exclude: glob patterns of skipped paths).
func LoadDir(dir string, exclude []string) (*Stage, error) {
	r, err := repo.Open(dir, exclude)
	if err != nil {
		return nil, fmt.Errorf("rules: %w", err)
	}
	return Load(r.Read, r.Files()), nil
}

// Facts is the number of typed facts the stage knows.
func (s *Stage) Facts() int { return s.facts }

// Decision is the deterministic verdict on one documentation line.
type Decision struct {
	// Category is agentic.Confirmed or agentic.Contradicted.
	Category string
	Kind     golang.FactKind
	// Subject as the line writes it; Doc and Code are the two values.
	Subject   string
	Doc, Code string
	// Quotes are the source lines the verdict rests on (verbatim).
	Quotes []agentic.QuoteRef
	// Draft is the line with only the wrong token replaced ("" = no draft).
	Draft  string
	Reason string
}

func (s *Stage) known(name string, flag bool) bool {
	if flag {
		return len(s.byFlag[name]) > 0
	}
	for _, f := range s.byName[name] {
		if f.Kind != golang.FactConst || f.Exported {
			return true
		}
	}
	return false
}

// Decide judges one documentation line; false leaves it to the model.
func (s *Stage) Decide(docPath string, text string) (Decision, bool) {
	if s == nil || historyDoc(docPath) || reQualifier.MatchString(text) {
		return Decision{}, false
	}
	if rt, ok := parseRoute(text); ok {
		return s.decideRoute(text, rt)
	}
	st, ok := parseStatement(text, s.known)
	if !ok {
		return Decision{}, false
	}
	facts := s.lookup(st)
	code, ok := agreed(facts)
	if !ok {
		return Decision{}, false
	}
	doc, ok := docValue(st, code.Kind)
	if !ok {
		return Decision{}, false
	}
	same := sameValue(doc, code)
	if !same && code.IsZero() && code.Kind != golang.ValBool {
		return Decision{}, false // a zero default is usually a sentinel ("0 = automatic")
	}
	d := Decision{Kind: facts[0].Kind, Subject: subjectText(text[st.subj.start+1 : st.subj.end-1]), Doc: describe(doc), Code: describe(code)}
	if d.Quotes = s.quotes(facts); len(d.Quotes) == 0 {
		return Decision{}, false
	}
	if same {
		if !covered(text, []span{st.subj, {st.kw, st.end}}, coverWords(facts)) {
			return Decision{}, false // the line says more than the fact settles
		}
		d.Category = agentic.Confirmed
	} else {
		d.Category = agentic.Contradicted
		if r := render(code, st); r != "" {
			d.Draft = text[:st.val.start] + r + text[st.val.end:]
		}
	}
	d.Reason = reason(d, facts[0])
	return d, true
}

// subjectText is the subject as written, without an argument placeholder ("-workers N").
func subjectText(span string) string {
	f := strings.Fields(span)
	if len(f) == 0 {
		return span
	}
	s, _, _ := strings.Cut(f[0], "=")
	return s
}

// lookup returns the facts a statement's subject names (the "is" form: constants only).
func (s *Stage) lookup(st statement) []golang.Fact {
	if st.flag {
		return s.byFlag[st.subject]
	}
	var out []golang.Fact
	for _, f := range s.byName[st.subject] {
		switch {
		case f.Kind == golang.FactConst && !f.Exported:
		case st.form == formIs && f.Kind != golang.FactConst:
			return nil
		default:
			out = append(out, f)
		}
	}
	return out
}

// quotes are the verbatim source lines of the facts (and of a constant a default refers to).
func (s *Stage) quotes(facts []golang.Fact) []agentic.QuoteRef {
	var out []agentic.QuoteRef
	seen := map[string]bool{}
	add := func(p string, n int) {
		k := fmt.Sprintf("%s:%d", p, n)
		ls := s.lines[p]
		if seen[k] || n < 1 || n > len(ls) {
			return
		}
		q := strings.TrimSpace(ls[n-1])
		if len([]rune(q)) < 6 {
			return // too short to verify
		}
		seen[k] = true
		out = append(out, agentic.QuoteRef{Path: p, Line: n, Quote: q})
	}
	for i, f := range facts {
		if i == maxQuoted {
			break
		}
		add(f.Path, f.Line)
		if f.Value.Path != "" {
			add(f.Value.Path, f.Value.Line)
		}
	}
	return out
}

var kindLabel = map[golang.FactKind]string{
	golang.FactFlag: "flag", golang.FactConst: "constant", golang.FactEnv: "environment variable",
	golang.FactField: "config field", golang.FactRoute: "route",
}

func reason(d Decision, f golang.Fact) string {
	q := d.Quotes[0]
	if d.Kind == golang.FactRoute && d.Category == agentic.Contradicted {
		return fmt.Sprintf("the line says %s, the code registers %s only for %s (%s:%d)", d.Subject, f.Name, d.Code, q.Path, q.Line)
	}
	if d.Category == agentic.Confirmed {
		return fmt.Sprintf("%s %s is %s in the code (%s:%d), as the line says", kindLabel[d.Kind], d.Subject, d.Code, q.Path, q.Line)
	}
	what := "is"
	if f.Kind != golang.FactConst && f.Kind != golang.FactRoute {
		what = "defaults to"
	}
	return fmt.Sprintf("the line says %s %s %s, the code says %s (%s:%d)", d.Subject, what, d.Doc, d.Code, q.Path, q.Line)
}
