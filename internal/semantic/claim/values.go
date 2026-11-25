package claim

import (
	"regexp"
	"strconv"
	"strings"
)

// ValueKind is the dimension of a checkable value.
type ValueKind string

// Value kinds.
const (
	Duration ValueKind = "duration" // Num in seconds
	Size     ValueKind = "size"     // Num in bytes
	Percent  ValueKind = "percent"  // Num as a fraction (2% = 0.02)
	Count    ValueKind = "count"    // Num as an integer, Noun is what is counted
	Clock    ValueKind = "clock"    // Num in minutes after midnight (UTC when TZ is known)
	Cron     ValueKind = "cron"     // Text is the 5-field expression
	Every    ValueKind = "every"    // Num is the interval in seconds ("every 2 minutes", "daily")
	Version  ValueKind = "version"  // Text is the version without a leading v
)

// Value is one checkable value in a sentence.
type Value struct {
	Kind ValueKind `json:"kind"`
	Raw  string    `json:"raw"`
	Num  float64   `json:"num,omitempty"`
	Text string    `json:"text,omitempty"`
	// TZ is the time zone written next to a clock time ("UTC", "JST", ... or "").
	TZ string `json:"tz,omitempty"`
	// Noun is the counted thing (counts) or the word written before a version (its product).
	Noun string `json:"noun,omitempty"`
	// Col is the byte offset in the sentence.
	Col int `json:"col"`
}

var durUnits = map[string]float64{
	"ms": 0.001, "millisecond": 0.001, "milliseconds": 0.001, "毫秒": 0.001,
	"s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1, "秒": 1,
	"m": 60, "min": 60, "mins": 60, "minute": 60, "minutes": 60, "分钟": 60, "分": 60,
	"h": 3600, "hr": 3600, "hrs": 3600, "hour": 3600, "hours": 3600, "小时": 3600, "時間": 3600,
	"d": 86400, "day": 86400, "days": 86400, "天": 86400, "日": 86400,
	"w": 604800, "week": 604800, "weeks": 604800, "周": 604800, "週": 604800, "星期": 604800,
	"month": 2592000, "months": 2592000, "个月": 2592000, "ヶ月": 2592000,
	"y": 31536000, "year": 31536000, "years": 31536000, "年": 31536000,
}

var sizeUnits = map[string]float64{
	"b": 1, "byte": 1, "bytes": 1,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"ki": 1 << 10, "kib": 1 << 10, "mi": 1 << 20, "mib": 1 << 20, "gi": 1 << 30, "gib": 1 << 30, "ti": 1 << 40, "tib": 1 << 40,
}

var numberWords = map[string]float64{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
	"nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	"一": 1, "两": 2, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6, "七": 7, "八": 8, "九": 9, "十": 10,
}

const numPat = `(\d+(?:\.\d+)?|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|[一两二三四五六七八九十])`

var (
	reDuration = regexp.MustCompile(`(?i)(?:^|[^\w.])` + numPat + `[ -]?(milliseconds?|ms|seconds?|secs?|sec|minutes?|mins?|min|hours?|hrs?|hr|days?|weeks?|months?|years?|毫秒|秒|分钟|小时|時間|天|周|週|星期|个月|ヶ月|年|[smhdwy])(?:$|[^\w])`)
	reSize     = regexp.MustCompile(`(?i)(?:^|[^\w.])(\d+(?:\.\d+)?)\s?(KiB|MiB|GiB|TiB|Ki|Mi|Gi|Ti|KB|MB|GB|TB|bytes?)(?:$|[^\w])`)
	rePercent  = regexp.MustCompile(`(?:^|[^\w.])(\d+(?:\.\d+)?)\s?%`)
	reClock    = regexp.MustCompile(`(?i)(?:^|[^\w:.])([01]?\d|2[0-3]):([0-5]\d)(?::[0-5]\d)?(?:\s?(UTC|GMT|JST|KST|CST|PST|PDT|EST|EDT|CET|CEST|Z))?(?:$|[^\w:])`)
	reCron     = regexp.MustCompile(`^\s*((?:[\d*/,\-?LW#A-Za-z]+\s+){4}[\d*/,\-?LW#A-Za-z]+)\s*$`)
	reEvery    = regexp.MustCompile(`(?i)\b(?:every|each)\s+` + numPat + `?\s*(seconds?|minutes?|mins?|hours?|days?|weeks?)\b|每\s*` + numPat + `?\s*(秒|分钟|小时|天|周|个?星期)`)
	reCadence  = regexp.MustCompile(`(?i)\b(hourly|daily|nightly|weekly|monthly)\b|(每天|每日|每晚|每周|每小时|每月|毎日|毎週)`)
	reVersion  = regexp.MustCompile(`(?:^|[^\w.\-/])(v?)(\d+\.\d+(?:\.\d+)?(?:-[0-9A-Za-z.]+)?)(?:$|[^\w.])`)
	reCount    = regexp.MustCompile(`(?i)(?:^|[^\w.])(\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\s?(?:个|台|条|项|种|路|本|つ|件)?\s?([A-Za-z][A-Za-z-]{2,}|` + "`" + `[^` + "`" + `]{2,40}` + "`" + `|[\p{Han}]{1,3})`)
	reCountZh  = regexp.MustCompile(`([两二三四五六七八九十])(?:个|种|条|项|台|路|件)([\p{Han}]{1,3})`)
)

var cadenceSeconds = map[string]float64{
	"hourly": 3600, "daily": 86400, "nightly": 86400, "weekly": 604800, "monthly": 2592000,
	"每天": 86400, "每日": 86400, "每晚": 86400, "每周": 604800, "每小时": 3600, "每月": 2592000,
	"毎日": 86400, "毎週": 604800,
}

func number(s string) (float64, bool) {
	if v, ok := numberWords[strings.ToLower(s)]; ok {
		return v, true
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// Values finds every checkable value in a sentence. Code spans that are cron expressions are
// passed separately by the caller (see ParseCron).
func Values(s string) []Value {
	var out []Value
	out = append(out, durations(s)...)
	out = append(out, sizes(s)...)
	out = append(out, percents(s)...)
	out = append(out, clocks(s)...)
	out = append(out, everies(s)...)
	out = append(out, versions(s)...)
	out = append(out, counts(s, out)...)
	return out
}

func durations(s string) []Value {
	var out []Value
	for _, m := range reDuration.FindAllStringSubmatchIndex(s, -1) {
		n, ok := number(s[m[2]:m[3]])
		unit := strings.ToLower(s[m[4]:m[5]])
		f, uok := durUnits[unit]
		if !ok || !uok || isBareSingleLetter(s, m, unit) {
			continue
		}
		out = append(out, Value{Kind: Duration, Raw: strings.TrimSpace(s[m[2]:m[5]]), Num: n * f, Col: m[2]})
	}
	return out
}

// isBareSingleLetter rejects "2 m" / "5 y" style spaced single-letter units (keeping "30d"),
// and number words glued to a unit ("ones").
func isBareSingleLetter(s string, m []int, unit string) bool {
	word := s[m[2]] > '9' && s[m[2]] < 0x80
	if word && (m[4] == m[3] || len(unit) == 1) {
		return true
	}
	return len(unit) == 1 && unit[0] < 0x80 && m[4] > m[3]
}

func sizes(s string) []Value {
	var out []Value
	for _, m := range reSize.FindAllStringSubmatchIndex(s, -1) {
		n, _ := strconv.ParseFloat(s[m[2]:m[3]], 64)
		f, ok := sizeUnits[strings.ToLower(s[m[4]:m[5]])]
		if !ok {
			f = 1
		}
		out = append(out, Value{Kind: Size, Raw: s[m[2]:m[5]], Num: n * f, Col: m[2]})
	}
	return out
}

func percents(s string) []Value {
	var out []Value
	for _, m := range rePercent.FindAllStringSubmatchIndex(s, -1) {
		n, _ := strconv.ParseFloat(s[m[2]:m[3]], 64)
		out = append(out, Value{Kind: Percent, Raw: s[m[2]:m[3]] + "%", Num: n / 100, Col: m[2]})
	}
	return out
}

func clocks(s string) []Value {
	var out []Value
	for _, m := range reClock.FindAllStringSubmatchIndex(s, -1) {
		h, _ := strconv.Atoi(s[m[2]:m[3]])
		mi, _ := strconv.Atoi(s[m[4]:m[5]])
		v := Value{Kind: Clock, Raw: s[m[2]:m[5]], Num: float64(h*60 + mi), Col: m[2]}
		if m[6] >= 0 {
			v.TZ = strings.ToUpper(s[m[6]:m[7]])
		}
		out = append(out, v)
	}
	return out
}

func everies(s string) []Value {
	var out []Value
	for _, m := range reEvery.FindAllStringSubmatchIndex(s, -1) {
		numI, unitI := 2, 4
		if m[4] < 0 {
			numI, unitI = 6, 8
		}
		n := 1.0
		if m[numI] >= 0 {
			n, _ = number(s[m[numI]:m[numI+1]])
		}
		u := strings.ToLower(s[m[unitI]:m[unitI+1]])
		f, ok := durUnits[u]
		if !ok {
			f = durUnits[strings.TrimPrefix(u, "个")]
		}
		out = append(out, Value{Kind: Every, Raw: s[m[0]:m[1]], Num: n * f, Col: m[0]})
	}
	for _, m := range reCadence.FindAllStringSubmatchIndex(s, -1) {
		w := strings.ToLower(s[m[0]:m[1]])
		out = append(out, Value{Kind: Every, Raw: w, Num: cadenceSeconds[w], Col: m[0]})
	}
	return out
}

func versions(s string) []Value {
	var out []Value
	for _, m := range reVersion.FindAllStringSubmatchIndex(s, -1) {
		txt := s[m[4]:m[5]]
		hasV := m[3] > m[2]
		if !hasV && strings.Count(txt, ".") < 2 || strings.Count(txt, ".") > 2 && !strings.Contains(txt, "-") {
			continue // "1.5" is a number; "1.2.3.4" is an address
		}
		start := m[2]
		out = append(out, Value{Kind: Version, Raw: s[start:m[5]], Text: txt, Noun: wordBefore(s, start), Col: start})
	}
	return out
}

// counts finds "<n> <noun>" not already taken by another value.
func counts(s string, taken []Value) []Value {
	var out []Value
	ms := reCount.FindAllStringSubmatchIndex(s, -1)
	ms = append(ms, reCountZh.FindAllStringSubmatchIndex(s, -1)...)
	for _, m := range ms {
		if overlaps(taken, m[2]) {
			continue
		}
		n, ok := number(s[m[2]:m[3]])
		if !ok || n != float64(int(n)) || n > 1000 {
			continue
		}
		noun := strings.Trim(s[m[4]:m[5]], "`")
		if stopNouns[strings.ToLower(noun)] {
			continue
		}
		out = append(out, Value{Kind: Count, Raw: s[m[2]:m[5]], Num: n, Noun: noun, Col: m[2]})
	}
	return out
}

var stopNouns = map[string]bool{"and": true, "or": true, "the": true, "of": true, "to": true, "in": true, "is": true, "are": true, "per": true, "times": true, "more": true, "less": true}

func overlaps(vs []Value, col int) bool {
	for _, v := range vs {
		if col >= v.Col && col < v.Col+len(v.Raw)+1 {
			return true
		}
	}
	return false
}

// wordBefore returns the identifier-like word right before offset i (skipping spaces, colons
// and backticks), or "".
func wordBefore(s string, i int) string {
	j := i
	for j > 0 && strings.ContainsRune(" \t`:@=", rune(s[j-1])) {
		j--
	}
	k := j
	for k > 0 && isWordByte(s[k-1]) {
		k--
	}
	return s[k:j]
}

func isWordByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c == '/' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// ParseCron returns the normalized 5-field cron expression in s, if s is one.
func ParseCron(s string) (string, bool) {
	m := reCron.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	f := strings.Fields(m[1])
	digits := 0
	for _, x := range f {
		if strings.ContainsAny(x, "0123456789*") {
			digits++
		}
	}
	if digits < 5 || !strings.ContainsAny(f[0]+f[1], "0123456789*") {
		return "", false
	}
	return strings.Join(f, " "), true
}

// ParseVersion reports whether s is a version ("v1.2", "1.2.3", "1.2.3-rc.1") and returns it
// without the leading v.
func ParseVersion(s string) (string, bool) {
	vs := versions(" " + strings.TrimSpace(s) + " ")
	if len(vs) != 1 {
		return "", false
	}
	return vs[0].Text, true
}
