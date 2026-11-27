package rulejudge

import (
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/extract/golang"
)

// stopWords are words that state nothing checkable on their own.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the a an and or of to in on at by for from with is are be as it its this that
		these those per any all each one default defaults defaulting value values set sets setting settings
		flag flags option options env environment variable variables var constant const field config
		configuration parameter parameters param argument arguments arg name names use uses used using
		can may must should will which what how see also via here there when number amount`) {
		stopWords[w] = true
	}
}

var (
	reWord     = regexp.MustCompile(`[A-Za-z]+`)
	reCamel    = regexp.MustCompile(`[A-Z]?[a-z]+|[A-Z]+(?:[^a-z]|$)|\d+`)
	reNotProse = regexp.MustCompile("[`\\d]")
)

// words splits text into lowercase words (camelCase, snake_case and kebab-case split).
func words(s string) []string {
	var out []string
	for _, w := range reWord.FindAllString(s, -1) {
		for _, p := range reCamel.FindAllString(w, -1) {
			out = append(out, strings.ToLower(strings.TrimRight(p, "0123456789")))
		}
	}
	return out
}

func stem(w string) string {
	for _, suf := range []string{"ies", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w) > len(suf)+2 {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

// coverWords are the words a confirmed line may use besides the subject and the value: the
// facts' names, usage strings and doc comments (the code's own description of the fact).
func coverWords(facts []golang.Fact) map[string]bool {
	out := map[string]bool{}
	for _, f := range facts {
		for _, s := range append([]string{f.Name, f.Usage}, f.Aliases...) {
			for _, w := range words(s) {
				out[stem(w)] = true
			}
		}
	}
	return out
}

// covered reports whether the line, without the given spans, says nothing the fact does not
// settle: no other code span, no other number, and every remaining word is a stop word or one
// of the fact's own words.
func covered(text string, cut []span, allowed map[string]bool) bool {
	b := []byte(text)
	for _, c := range cut {
		for i := max(0, c.start); i < min(len(b), c.end); i++ {
			b[i] = ' '
		}
	}
	rest := string(b)
	if reNotProse.MatchString(rest) {
		return false
	}
	for _, w := range words(rest) {
		if len(w) < 3 || stopWords[w] || allowed[stem(w)] {
			continue
		}
		return false
	}
	return true
}

// decideRoute: a route the code registers with its full path under the written method
// confirms a line that says nothing else; a path the code registers only under other methods
// contradicts the method. Routes under router groups or mounted prefixes are never decided.
// routeMethods are the methods the facts register; match: one serves the method; false when
// a fact is not anchored.
func routeMethods(facts []golang.Fact, method string) (map[string]bool, bool, bool) {
	methods := map[string]bool{}
	match := false
	for _, f := range facts {
		if !f.Anchored {
			return nil, false, false
		}
		methods[f.Method] = true
		match = match || f.Method == "" || f.Method == method || method == http.MethodHead && f.Method == http.MethodGet
	}
	return methods, match, true
}

func (s *Stage) decideRoute(text string, rt routeStmt) (Decision, bool) {
	facts := s.routes[golang.NormalizeRoute(rt.path)]
	if len(facts) == 0 {
		return Decision{}, false
	}
	methods, match, ok := routeMethods(facts, rt.method)
	if !ok {
		return Decision{}, false
	}
	d := Decision{Kind: golang.FactRoute, Subject: rt.method + " " + rt.path, Doc: rt.method}
	if d.Quotes = s.quotes(facts); len(d.Quotes) == 0 {
		return Decision{}, false
	}
	if match {
		cut := rt.whole
		if cut.start > 0 && cut.end < len(text) && text[cut.start-1] == '`' && text[cut.end] == '`' {
			cut = span{cut.start - 1, cut.end + 1}
		}
		if !covered(text, []span{cut}, coverWords(facts)) {
			return Decision{}, false
		}
		d.Category, d.Code = agentic.Confirmed, rt.method
		d.Reason = reason(d, facts[0])
		return d, true
	}
	d.Category = agentic.Contradicted
	if len(methods) == 1 {
		for m := range methods {
			d.Code = m
			d.Draft = text[:rt.meth.start] + m + text[rt.meth.end:]
		}
	} else {
		d.Code = strings.Join(slices.Sorted(maps.Keys(methods)), "|")
	}
	d.Reason = reason(d, facts[0])
	return d, true
}
