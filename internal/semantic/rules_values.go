package semantic

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/semantic/evidence"
)

// stopKeywords are words too generic to anchor evidence retrieval.
var stopKeywords = map[string]bool{
	"time": true, "size": true, "count": true, "max": true, "min": true, "limit": true, "default": true,
	"value": true, "number": true, "total": true, "after": true, "before": true, "every": true, "with": true,
	"from": true, "than": true, "that": true, "this": true, "each": true, "about": true, "most": true,
	"least": true, "only": true, "more": true, "less": true, "under": true, "over": true, "within": true,
	"the": true, "and": true, "for": true, "are": true, "not": true, "was": true, "has": true, "can": true,
}

// sameDim returns the fact's values comparable with v (same dimension).
func sameDim(f evidence.Fact, v claim.Value) []claim.Value {
	var out []claim.Value
	for _, fv := range f.Vals {
		if fv.Kind == v.Kind {
			out = append(out, fv)
		}
	}
	if len(out) == 0 && f.IsNum {
		if n, ok := implied(f, v.Kind); ok {
			out = append(out, claim.Value{Kind: v.Kind, Raw: f.Value, Num: n})
		}
	}
	return out
}

var unitHints = []struct {
	suffix string
	factor float64
}{
	{"ms", 0.001},
	{"millis", 0.001},
	{"seconds", 1},
	{"secs", 1},
	{"sec", 1},
	{"minutes", 60},
	{"mins", 60},
	{"hours", 3600},
	{"days", 86400},
	{"weeks", 604800},
}

// implied reads a bare number using the unit named in its key (timeout_seconds: 30).
func implied(f evidence.Fact, k claim.ValueKind) (float64, bool) {
	ws := evidence.Words(f.Leaf)
	switch k {
	case claim.Duration:
		for _, h := range unitHints {
			if len(ws) > 0 && ws[len(ws)-1] == h.suffix {
				return f.Num * h.factor, true
			}
		}
	case claim.Percent:
		if f.Num >= 0 && f.Num <= 1 {
			return f.Num, true
		}
	case claim.Size, claim.Count, claim.Clock, claim.Cron, claim.Every, claim.Version:
	}
	return 0, false
}

func equalNum(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// checkValues compares durations, sizes and percentages with configuration values of the same
// key: an identifier written in the claim (exact), or a specific keyword next to the number.
func (c *checker) checkValues(cl *claim.Claim) {
	for _, v := range cl.Values {
		if v.Kind != claim.Duration && v.Kind != claim.Size && v.Kind != claim.Percent {
			continue
		}
		facts, exact := c.valueFacts(cl, v)
		if len(facts) == 0 || anyEqual(facts, v) {
			continue
		}
		conf := model.ConfLow
		if exact {
			conf = model.ConfMedium
		}
		f := facts[0]
		c.add(cl, Issue{
			Rule: RuleValueMismatch, Subject: fmt.Sprintf("%s=%s", f.Leaf, v.Raw), Confidence: conf,
			Detail:     fmt.Sprintf("the docs say %s, %s sets %s = %s", v.Raw, f.Path, f.Key, f.Value),
			Suggestion: fmt.Sprintf("Update the docs to %s (or the configuration, if the docs are right).", f.Value),
			Quote:      c.quoteLine(f.Path, f.Line), Locations: factLocs(facts),
		})
	}
}

// valueFacts finds the configuration facts a value is about: the code span that contains the
// value ("`retention: 30d`") or the one written right before it ("`RETENTION` is 30d") names the
// key. A value without such a key is left to the judge (it may be about anything).
func (c *checker) valueFacts(cl *claim.Claim, v claim.Value) ([]evidence.Fact, bool) {
	key := keyFor(cl.Text, v)
	if key == "" {
		return nil, false
	}
	var out []evidence.Fact
	for _, f := range c.ix.FactsNamed(key) {
		if len(sameDim(f, v)) > 0 && !f.Commented {
			out = append(out, f)
		}
	}
	if distinctFiles(out) > 2 && !anyEqual(out, v) {
		return nil, false // the same key means different things in many files
	}
	return out, true
}

var reKeyIn = regexp.MustCompile(`^\s*([A-Za-z_][\w.-]*|[\p{Han}]{1,8})\s*[:=]`)

// keyFor returns the key named by the code span containing the value, or by the span ending at
// most 30 bytes before it.
func keyFor(text string, v claim.Value) string {
	best := ""
	for _, m := range reSpanIdx.FindAllStringIndex(text, -1) {
		span := text[m[0]+1 : m[1]-1]
		switch {
		case m[0] <= v.Col && v.Col < m[1]:
			if k := reKeyIn.FindStringSubmatch(span); k != nil {
				return k[1]
			}
			return ""
		case m[1] <= v.Col && v.Col-m[1] <= 30 && !strings.ContainsAny(span, " ="):
			best = span
		}
	}
	return symbolName(best)
}

var reSpanIdx = regexp.MustCompile("`[^`]+`")

func anyEqual(facts []evidence.Fact, v claim.Value) bool {
	for _, f := range facts {
		for _, fv := range sameDim(f, v) {
			if equalNum(fv.Num, v.Num) {
				return true
			}
		}
	}
	return false
}

func distinctFiles(fs []evidence.Fact) int {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Path] = true
	}
	return len(m)
}

func factLocs(fs []evidence.Fact) []Location {
	var out []Location
	seen := map[string]bool{}
	for _, f := range fs {
		k := fmt.Sprintf("%s:%d", f.Path, f.Line)
		if !seen[k] && len(out) < 5 {
			seen[k] = true
			out = append(out, Location{Path: f.Path, Line: f.Line, Role: "code"})
		}
	}
	return out
}
