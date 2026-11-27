package rulejudge

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/extract/golang"
)

var (
	reUsageDefault = regexp.MustCompile("(?i)\\bdefault:?\\s+(?:is\\s+)?(\"[^\"]*\"|`[^`]*`|[^\\s,;)]+)")
	reDurWords     = regexp.MustCompile(`^([+-]?\d+(?:\.\d+)?)\s(milliseconds?|seconds?|minutes?|hours?)$`)
	durWordSeconds = map[string]float64{"millisecond": 1e-3, "second": 1, "minute": 60, "hour": 3600}
)

// sameValue compares two values of one kind.
func sameValue(a, b golang.Value) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == golang.ValString {
		return a.Str == b.Str
	}
	return math.Abs(a.Num-b.Num) <= 1e-9*math.Max(1, math.Abs(a.Num))
}

// effective is the value a fact stands for. A flag whose usage names a default ("claims put
// to the judge (default 150)") stands for that default when its code default is the zero
// value (a sentinel resolved later); a usage that disagrees with a non-zero default is unclear.
func effective(f golang.Fact) (golang.Value, bool) {
	v := f.Value
	if v.Kind == golang.ValNone {
		return v, false
	}
	if f.Kind != golang.FactFlag {
		return v, true
	}
	m := reUsageDefault.FindStringSubmatch(f.Usage)
	if m == nil {
		return v, true
	}
	u, ok := parseAs(strings.Trim(m[1], "\"`"), v.Kind, true)
	switch {
	case !ok:
		return v, false
	case v.IsZero():
		return u, true
	case !sameValue(u, v):
		return v, false
	}
	return v, true
}

// agreed is the one value every fact stands for (false when they disagree or one is unknown).
func agreed(facts []golang.Fact) (golang.Value, bool) {
	var out golang.Value
	for i, f := range facts {
		v, ok := effective(f)
		if !ok || i > 0 && !sameValue(v, out) {
			return golang.Value{}, false
		}
		out = v
	}
	return out, len(facts) > 0
}

// parseAs reads a documentation value as a value of kind k. Strings must be written as code
// or quoted (unless lenient: a usage string's own words).
func parseAs(s string, k golang.ValueKind, lenient bool) (golang.Value, bool) {
	switch k {
	case golang.ValInt, golang.ValFloat:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || k == golang.ValInt && n != math.Trunc(n) {
			return golang.Value{}, false
		}
		return golang.Value{Kind: k, Num: n}, true
	case golang.ValBool:
		b, err := strconv.ParseBool(s)
		if err != nil || s != "true" && s != "false" {
			return golang.Value{}, false
		}
		v := golang.Value{Kind: k}
		if b {
			v.Num = 1
		}
		return v, true
	case golang.ValDuration:
		return parseDuration(s)
	case golang.ValString:
		return golang.Value{Kind: k, Str: s}, lenient
	case golang.ValNone:
	}
	return golang.Value{}, false
}

func parseDuration(s string) (golang.Value, bool) {
	if m := reDurWords.FindStringSubmatch(s); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		return golang.Value{Kind: golang.ValDuration, Num: n * durWordSeconds[strings.TrimSuffix(m[2], "s")]}, true
	}
	d, err := time.ParseDuration(s)
	if err != nil || s == "0" {
		return golang.Value{}, false
	}
	return golang.Value{Kind: golang.ValDuration, Num: d.Seconds()}, true
}

// docValue reads a statement's value as kind k (strings only when written as code or quoted).
func docValue(st statement, k golang.ValueKind) (golang.Value, bool) {
	if k == golang.ValString {
		return golang.Value{Kind: k, Str: st.value}, st.delim != 0
	}
	return parseAs(st.value, k, false)
}

// render writes a code value the way the documentation wrote the old one ("" = no draft).
func render(v golang.Value, st statement) string {
	switch v.Kind {
	case golang.ValInt:
		return strconv.FormatInt(int64(v.Num), 10)
	case golang.ValFloat:
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	case golang.ValBool:
		return strconv.FormatBool(v.Num != 0)
	case golang.ValString:
		if strings.ContainsAny(v.Str, "\n\r") || st.delim != 0 && strings.IndexByte(v.Str, st.delim) >= 0 {
			return ""
		}
		return v.Str
	case golang.ValDuration:
		return renderDuration(v.Num, st.value)
	case golang.ValNone:
	}
	return ""
}

// renderDuration keeps the style of the old value: "90s" -> "1m30s", "30 seconds" -> "45 seconds".
func renderDuration(secs float64, old string) string {
	if m := reDurWords.FindStringSubmatch(old); m != nil {
		unit := strings.TrimSuffix(m[2], "s")
		n := secs / durWordSeconds[unit]
		if n != math.Trunc(n) {
			return ""
		}
		w := unit
		if n != 1 {
			w += "s"
		}
		return strconv.FormatFloat(n, 'f', -1, 64) + " " + w
	}
	s := time.Duration(secs * float64(time.Second)).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// describe writes a value for a reason text.
func describe(v golang.Value) string {
	switch v.Kind {
	case golang.ValString:
		return strconv.Quote(v.Str)
	case golang.ValDuration:
		return renderDuration(v.Num, "1s")
	case golang.ValInt, golang.ValFloat, golang.ValBool:
		return render(v, statement{})
	case golang.ValNone:
	}
	return "?"
}
