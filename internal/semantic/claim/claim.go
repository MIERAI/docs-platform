// Package claim extracts atomic, checkable claims from documentation: sentences that carry a
// number with a unit, a schedule, a version, a list of names, a state word ("planned", "not
// yet", "实现中") or a file role ("generated", "人写"), together with the identifiers written
// next to them. It is a pure function of the text (no I/O) and never fails; it is fuzzed.
package claim

import (
	"regexp"
	"strings"
)

// State classes.
const (
	StatePlanned    = "planned"
	StateInProgress = "in_progress"
	StateNotYet     = "not_yet"
	StateTodo       = "todo"
	StateRemoved    = "removed"
	StateOnly       = "only"
)

// Role classes.
const (
	RoleHuman     = "human"
	RoleGenerated = "generated"
)

// Claim is one sentence (or table row) with checkable content.
type Claim struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// Section is the nearest heading above the claim ("" at the top of a file).
	Section string `json:"section,omitempty"`
	// Status marks a document status line ("状态: 实现中", "Status: draft").
	Status bool     `json:"status,omitempty"`
	Values []Value  `json:"values,omitempty"`
	States []string `json:"states,omitempty"`
	Roles  []string `json:"roles,omitempty"`
	Lists  []List   `json:"lists,omitempty"`
	// Idents are identifiers written in the claim: code spans, paths, env vars, flags.
	Idents []string `json:"idents,omitempty"`
	// Comment is true for claims taken from source-code comments.
	Comment bool `json:"comment,omitempty"`
}

// List is an enumeration of names written in one claim.
type List struct {
	Items []string `json:"items"`
	// Open is true when the text marks the list as non-exhaustive ("e.g.", "等", "such as").
	Open bool `json:"open,omitempty"`
	// Closed is true when the text marks it as exhaustive ("only", "all", "全部", "只").
	Closed bool `json:"closed,omitempty"`
}

// Checkable reports whether the claim carries anything a check can compare.
func (c *Claim) Checkable() bool {
	return len(c.Values) > 0 || len(c.States) > 0 || len(c.Roles) > 0 || len(c.Lists) > 0 || c.Status
}

var stateWords = []struct {
	class string
	re    *regexp.Regexp
}{
	{StateInProgress, regexp.MustCompile(`(?i)\bin[ -]progress\b|\bWIP\b|\bunder review\b|\bwork in progress\b|\bdraft\b|\bbeing (?:implemented|built|written)\b|实现中|进行中|开发中|评审中|审查中|草案|草稿|検討中|実装中`)},
	{StatePlanned, regexp.MustCompile(`(?i)\bplanned\b|\bwill be (?:added|implemented|supported)\b|\bin (?:the )?future\b|\broadmap\b|\bcoming soon\b|\bto be (?:added|implemented|decided)\b|计划中|计划做|计划支持|将来|以后|待定|待决定|未来|予定`)},
	{StateNotYet, regexp.MustCompile(`(?i)\bnot (?:yet|implemented|supported)\b|\bno [\w ]{1,30} yet\b|\bunsupported\b|\bunimplemented\b|还没有|还没|尚未|未实现|未做|还不是|还不|暂不|未対応|未実装`)},
	{StateTodo, regexp.MustCompile(`\bTODO\b|\bFIXME\b|\bTBD\b`)},
	{StateRemoved, regexp.MustCompile(`(?i)\b(?:was|were|has been|have been|is) (?:removed|deleted|dropped|retired)\b|\bno longer (?:exists?|used|supported)\b|\bdeprecated\b|已移除|已删除|已废弃|废弃|不再使用|廃止`)},
	{StateOnly, regexp.MustCompile(`(?i)\bonly\b|只有|仅|只在|唯一|のみ`)},
}

var (
	reStatus   = regexp.MustCompile(`(?i)(?:^\s*(?:[-*>]\s*)?|[·|]\s*)(?:\*\*)?(?:status|状态|状態|ステータス)(?:\*\*)?\s*[:：]`)
	reHuman    = regexp.MustCompile(`(?i)\b(?:hand[- ]?written|written by hand|humans? (?:write|edit)s?|edit(?:ed)? by hand|maintained by hand|manually (?:written|edited|maintained))\b|人写|手写|手工|人が書く|手書き`)
	reGenRole  = regexp.MustCompile(`(?i)\b(?:auto[- ]?generated|generated|codegen)\b|生成物|自动生成|由.{0,20}生成|生成`)
	reOpen     = regexp.MustCompile(`(?i)\be\.g\.|\bfor example\b|\bsuch as\b|\bincluding\b|\betc\b|…|\.\.\.|例如|比如|等|など|包括|之类`)
	reClosed   = regexp.MustCompile(`(?i)\bonly\b|\ball\b|\bexactly\b|\bthe following\b|全部|所有|只有|仅|只|以下|のみ|すべて`)
	reSentence = regexp.MustCompile(`[。；！？]|[.;!?](?:\s|$)`)
)

// fromText builds a claim from one sentence of plain text plus its code spans.
func fromText(text string, spans []string) Claim {
	c := Claim{Text: strings.TrimSpace(text), Status: reStatus.MatchString(text)}
	plain := text
	for _, sp := range spans {
		if cr, ok := ParseCron(sp); ok {
			c.Values = append(c.Values, Value{Kind: Cron, Raw: sp, Text: cr, Col: strings.Index(text, sp)})
		}
	}
	c.Values = append(c.Values, Values(plain)...)
	for _, w := range stateWords {
		if w.re.MatchString(stripSpans(plain)) {
			c.States = append(c.States, w.class)
		}
	}
	c.Roles = roles(plain)
	c.Idents = idents(plain, spans)
	if l, ok := listIn(plain, spans); ok {
		c.Lists = append(c.Lists, l)
	}
	return c
}

func roles(s string) []string {
	var out []string
	if reHuman.MatchString(s) {
		out = append(out, RoleHuman)
	}
	if reGenRole.MatchString(stripSpans(s)) {
		out = append(out, RoleGenerated)
	}
	return out
}

var reSpan = regexp.MustCompile("`[^`]*`")

// stripSpans blanks code spans so words inside them ("draft" as a flag value) are not read as
// prose.
func stripSpans(s string) string {
	return reSpan.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
}

// Sentences splits a line of prose into sentences, keeping each sentence's text.
func Sentences(line string) []string {
	if strings.Count(line, "|") >= 2 && strings.HasPrefix(strings.TrimSpace(line), "|") {
		return []string{line} // a table row is one claim
	}
	var out []string
	masked := stripSpans(line)
	start := 0
	for _, m := range reSentence.FindAllStringIndex(masked, -1) {
		if s := strings.TrimSpace(line[start:m[1]]); s != "" {
			out = append(out, s)
		}
		start = m[1]
	}
	if s := strings.TrimSpace(line[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// HumanRoleIndex returns the byte range of the "written by people" phrase in s, or nil.
func HumanRoleIndex(s string) []int { return reHuman.FindStringIndex(s) }
