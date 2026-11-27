package rulejudge

import (
	"path"
	"regexp"
	"strings"
)

// span is a byte range of the documentation line.
type span struct{ start, end int }

// Statement forms.
const (
	formDefault = "default" // `-workers` ... default 8 / defaults to 8 / (default: `json`)
	formIs      = "is"      // `MaxQuestions` is 150 / `MaxQuestions` = 150 (constants only)
)

// statement is one checkable statement of a documentation line: a subject with a value.
type statement struct {
	form    string
	subject string // lookup name (a flag without its dashes)
	flag    bool
	subj    span // the subject's code span, backticks included
	value   string
	val     span // the value without its delimiters (what a draft replaces)
	delim   byte // '`', '"' or 0 (bare)
	kw      int  // start of the phrase introducing the value ("default", "is")
	end     int  // end of the value with its delimiter
}

var (
	reCodeSpan  = regexp.MustCompile("`[^`]+`")
	reFlagSubj  = regexp.MustCompile(`^--?([a-zA-Z0-9][a-zA-Z0-9_.-]*)(?:[ =\[<].*)?$`)
	reIdentSubj = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?$`)
	reDefaultKw = regexp.MustCompile(`(?i)\bdefault(?:s|ing)?\b`)
	// reDefaultTail is what may sit between the keyword and the value.
	reDefaultTail = regexp.MustCompile(`^(?:\s+(?:value\s+)?(?:is|to|of|=))?\s*[:=]?\s*`)
	reIsTail      = regexp.MustCompile(`^\s*(?:is|=|:)\s+`)
	// reBare: a bare value must look like a literal (a number, a duration, a boolean); a
	// word is never taken as a value ("default from config", "default: the directory name").
	reBare = regexp.MustCompile(`^(?:[+-]?\d+(?:\.\d+)?(?:\s(?:milliseconds?|seconds?|minutes?|hours?)\b|[a-zµ]{1,2}(?:\d+[a-zµ]{1,2})*)?|true\b|false\b)`)
	reEnd  = regexp.MustCompile(`^(?:$|[\s,;)|]|\.(?:\s|$))`)
	// reQualifier: the line restricts or dates the statement (a CI override, an old release,
	// an example): no deterministic verdict.
	reQualifier = regexp.MustCompile(`(?i)\b(?:if|when|unless|except|otherwise|but|while|instead|previously|formerly|was|were|used to|older|deprecated|removed|renamed|changed|until|since|before|after|example|recommended|suggested|override[sn]?|overridden|ci|production|staging)\b|e\.g\.|~~|<!--`)
	// reHistoryDoc: documents that record the past, not the present.
	reHistoryDoc = regexp.MustCompile(`(?i)^(?:changelog|changes|history|news|release[-_ ]?notes|releases)(?:\.|$)`)
)

// historyDoc reports whether a document records history (its lines describe old releases).
func historyDoc(p string) bool { return reHistoryDoc.MatchString(path.Base(p)) }

// subjectOf reads the subject of a code span: a flag (with its dashes, maybe followed by an
// argument placeholder) or an identifier.
func subjectOf(content string) (name string, flag bool, ok bool) {
	c := strings.TrimSpace(content)
	if m := reFlagSubj.FindStringSubmatch(c); m != nil {
		return m[1], true, true
	}
	if reIdentSubj.MatchString(c) {
		return c, false, true
	}
	return "", false, false
}

// parseStatement finds the single statement of a line. known reports whether a subject names
// a fact. Lines with several known subjects, several default keywords, or a value that is not
// unambiguously a literal give no statement.
func parseStatement(text string, known func(name string, flag bool) bool) (statement, bool) {
	var st statement
	n := 0
	for _, m := range reCodeSpan.FindAllStringIndex(text, -1) {
		name, flag, ok := subjectOf(text[m[0]+1 : m[1]-1])
		if ok && known(name, flag) {
			st.subject, st.flag, st.subj = name, flag, span{m[0], m[1]}
			n++
		}
	}
	if n != 1 {
		return statement{}, false
	}
	kws := reDefaultKw.FindAllStringIndex(text, -1)
	switch {
	case len(kws) == 1 && kws[0][0] >= st.subj.end:
		st.form, st.kw = formDefault, kws[0][0]
		tail := reDefaultTail.FindStringIndex(text[kws[0][1]:])
		return withValue(text, st, kws[0][1]+tail[1])
	case len(kws) == 0 && !st.flag:
		tail := reIsTail.FindStringIndex(text[st.subj.end:])
		if tail == nil {
			return statement{}, false
		}
		st.form, st.kw = formIs, st.subj.end
		return withValue(text, st, st.subj.end+tail[1])
	}
	return statement{}, false
}

// withValue reads the value starting at byte i: `code`, "quoted" or a bare literal.
func withValue(text string, st statement, i int) (statement, bool) {
	if i >= len(text) {
		return statement{}, false
	}
	switch c := text[i]; c {
	case '`', '"':
		j := strings.IndexByte(text[i+1:], c)
		if j < 0 {
			return statement{}, false
		}
		st.delim, st.value, st.val, st.end = c, text[i+1:i+1+j], span{i + 1, i + 1 + j}, i+2+j
	default:
		m := reBare.FindString(text[i:])
		if m == "" {
			return statement{}, false
		}
		st.value, st.val, st.end = m, span{i, i + len(m)}, i+len(m)
	}
	if !reEnd.MatchString(text[st.end:]) {
		return statement{}, false // "default 8 per CPU" style values are not literals
	}
	if st.delim == 0 && st.end < len(text) && text[st.end] == ' ' && reBare.MatchString(text[st.end+1:]) {
		return statement{}, false // two literals in a row: unclear which is the value
	}
	return st, true
}

// routeStmt is an HTTP route written in a line ("GET /items/{id}").
type routeStmt struct {
	method string
	path   string
	meth   span
	whole  span
}

var reRoute = regexp.MustCompile(`\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+(/[^\s` + "`" + `|),;]*)`)

// parseRoute finds the single route a line names.
func parseRoute(text string) (routeStmt, bool) {
	ms := reRoute.FindAllStringSubmatchIndex(text, -1)
	if len(ms) != 1 {
		return routeStmt{}, false
	}
	m := ms[0]
	p := strings.TrimRight(text[m[4]:m[5]], ".:")
	return routeStmt{method: text[m[2]:m[3]], path: p, meth: span{m[2], m[3]}, whole: span{m[0], m[4] + len(p)}}, true
}
