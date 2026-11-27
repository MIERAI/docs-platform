package factorypr

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/factorytry"
)

var categoryLabel = map[string]string{
	"confirmed":            "✅ 仍然成立",
	"contradicted":         "❌ 代码和规则矛盾",
	"outdated_wording":     "⚠️ 写法已过时",
	"incomplete_list":      "⚠️ 列举不完整",
	"dangling_reference":   "❌ 引用不存在",
	"unverifiable_in_repo": "❔ 无法从代码确认",
	"unknown":              "❔ 未判定",
}

// Feedback options a person ticks in the comment.
const (
	OptionDesignStale  = "design_stale"  // the design is out of date: the draft is (or will be) applied
	OptionCodeWrong    = "code_wrong"    // the code is wrong: it will be fixed
	OptionNotAProblem  = "not_a_problem" // the design still says what the code does (a false positive)
	feedbackMarkPrefix = "<!-- docsp:fb "
)

// Markdown renders the review as a PR comment (with Marker, so it is updated in place).
// It returns "" when the change touches no rule with hand-written code.
func (r *Report) Markdown() string { return r.MarkdownWith(nil) }

// MarkdownWith renders the comment keeping the boxes people ticked in the previous version
// (by Feedback.key: a tick on a verdict that changed is not carried over). Only what deciding
// takes is shown: per problem one sentence, one question answered by ticking; the evidence
// and the draft are folded.
func (r *Report) MarkdownWith(ticked map[string]bool) string {
	if len(r.Rules) == 0 {
		return ""
	}
	var b strings.Builder
	n := r.Problems()
	if n == 0 {
		fmt.Fprintf(&b, "%s\n**docs-platform**:这个 PR 改到的 %d 条设计书规则都和代码一致,不需要做什么。\n", Marker, len(r.Rules))
		return b.String()
	}
	fmt.Fprintf(&b, "%s\n**docs-platform**:这个 PR 让 **%d 条设计书规则**和代码不一致了。\n", Marker, n)
	shown := map[string]string{} // patch -> the rule it was shown under
	for _, x := range r.Rules {
		if problem[x.Category] {
			r.writeProblem(&b, x, ticked, shown)
		}
	}
	r.writeRest(&b)
	b.WriteString("\n<sub>🔬 = 工厂在这个 PR 上实际跑了设计书的例子,确认不一致;📖 = AI 读代码的判断,可能是误报。勾选只用来统计这个检查的误报率。平台不会改任何文件。</sub>\n")
	return b.String()
}

func (r *Report) writeProblem(b *strings.Builder, x Rule, ticked map[string]bool, shown map[string]string) {
	mark := map[string]string{EvidenceExecuted: "🔬", EvidenceRead: "📖"}[x.Evidence]
	fmt.Fprintf(b, "\n#### %s %s · %s\n\n", r.lineLink(x.Path, x.Line), cell(strings.TrimPrefix(strings.TrimSpace(firstLine(x.Text)), "- ")), mark)
	d := x.Draft
	switch {
	case d != nil && d.Summary != "":
		fmt.Fprintf(b, "%s\n\n", d.Summary)
	default:
		for _, f := range x.Facts {
			if f.Status != "confirmed" {
				fmt.Fprintf(b, "%s\n\n", f.Fact)
				break
			}
		}
	}
	q := "设计书该改,还是代码该改?"
	if d != nil && d.Question != "" {
		q = d.Question
	}
	fmt.Fprintf(b, "**%s**\n\n", q)
	writeChoices(b, x, ticked)
	b.WriteString("\n<details><summary>证据和改稿</summary>\n\n")
	block := x.Block
	if block == "" {
		block = x.Text
	}
	fmt.Fprintf(b, "设计书 %s\n\n```%s\n%s\n```\n\n", r.lineLink(x.Path, x.Line), fence(x.Path), strings.TrimRight(block, "\n"))
	for _, c := range x.Code {
		fmt.Fprintf(b, "代码 %s\n\n```%s\n%s\n```\n\n", r.lineLink(c.Path, c.Line), fence(c.Path), strings.TrimRight(c.Text, "\n"))
	}
	r.writeCounterexample(b, x)
	r.writeCause(b, x)
	switch {
	case d != nil && d.Patch != "" && shown[d.Patch] != "":
		fmt.Fprintf(b, "改稿:和 `%s` 的相同。\n\n", shown[d.Patch])
	default:
		writeDraft(b, d)
		if d != nil && d.Patch != "" {
			shown[d.Patch] = fmt.Sprintf("%s:%d", x.Path, x.Line)
		}
	}
	b.WriteString("</details>\n")
}

// writeRest folds the rules that still hold.
func (r *Report) writeRest(b *strings.Builder) {
	var rest []Rule
	for _, x := range r.Rules {
		if !problem[x.Category] {
			rest = append(rest, x)
		}
	}
	if len(rest) == 0 {
		return
	}
	fmt.Fprintf(b, "\n<details><summary>另外 %d 条规则也核对过</summary>\n\n", len(rest))
	for _, x := range rest {
		label := categoryLabel[x.Category]
		if label == "" {
			label = x.Category
		}
		fmt.Fprintf(b, "- %s %s — %s\n", r.lineLink(x.Path, x.Line), cell(strings.TrimPrefix(strings.TrimSpace(firstLine(x.Text)), "- ")), label)
	}
	b.WriteString("\n</details>\n")
}

// writeCounterexample: executed failures of the design's examples first; the drafter's
// worked input only when nothing was run.
func (r *Report) writeCounterexample(b *strings.Builder, x Rule) {
	if f := r.Failing[x.API]; len(f) > 0 {
		if x.Evidence == EvidenceExecuted {
			b.WriteString("反例(工厂在这个 PR 上实际跑的设计书例子):\n\n")
		} else {
			b.WriteString("这个 API 失败的设计书例子(实际跑过,但改稿没有改这条规则,不能证明是这条的问题):\n\n")
		}
		for _, c := range f[:min(4, len(f))] {
			fmt.Fprintf(b, "- `%s`:%s\n", c.Name, caseDetail(c))
		}
		if len(f) > 4 {
			fmt.Fprintf(b, "- 另外还有 %d 个\n", len(f)-4)
		}
		b.WriteString("\n")
		return
	}
	if x.Draft != nil && x.Draft.Counterexample != "" {
		fmt.Fprintf(b, "反例(AI 推出来的,没有实际跑过):%s\n\n", x.Draft.Counterexample)
	}
}

var wantGot = regexp.MustCompile(`^(\S+): want (.+?), got (.+?)(?: \(body .*\))?$`)

// caseDetail turns the factory's "response.x: want A, got B (body …)" into plain words.
func caseDetail(c factorytry.Case) string {
	if m := wantGot.FindStringSubmatch(c.Detail); m != nil {
		got := m[3]
		if got == "<nil>" {
			got = "没有这个值(0 / 空)"
		}
		return fmt.Sprintf("`%s` 设计书预期 **%s**,代码实际是 **%s**", m[1], m[2], got)
	}
	d := c.Detail
	if len([]rune(d)) > 160 {
		d = string([]rune(d)[:160]) + "…"
	}
	return "失败 — " + d
}

func (r *Report) writeCause(b *strings.Builder, x Rule) {
	var files []string
	for _, f := range x.Touched {
		if r.Diffs[f] != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return
	}
	b.WriteString("引起它的改动:\n\n")
	for _, f := range files {
		fmt.Fprintf(b, "`%s`\n\n```diff\n%s\n```\n\n", f, r.Diffs[f])
	}
}

func writeDraft(b *strings.Builder, d *Draft) {
	switch {
	case d == nil:
		return
	case len(d.Edits) == 0:
		fmt.Fprintf(b, "改稿:没有提出(%s)\n\n", d.Problem)
		return
	case d.Verified:
		b.WriteString("改稿(✅ 工厂验证通过;在 PR 分支上存成 `design.patch` 后 `git apply design.patch`):\n\n")
	default:
		fmt.Fprintf(b, "改稿(⚠️ 未通过验证:%s,仅供参考):\n\n", d.Problem)
	}
	if d.Patch != "" {
		fmt.Fprintf(b, "```diff\n%s```\n\n", d.Patch)
	}
}

// writeChoices: the answers to the question, as boxes. A 🔬 problem has no "not a problem":
// the design's examples fail on this rule.
func writeChoices(b *strings.Builder, x Rule, ticked map[string]bool) {
	opts := [][2]string{{OptionDesignStale, "是:设计书过时了(采用折叠里的改稿)"}, {OptionCodeWrong, "不是:代码改错了"}}
	if x.Evidence != EvidenceExecuted {
		opts = append(opts, [2]string{OptionNotAProblem, "都不是:这里没有不一致(误报)"})
	}
	for _, o := range opts {
		f := Feedback{Rule: fmt.Sprintf("%s:%d", x.Path, x.Line), Option: o[0], Category: x.Category, Evidence: x.Evidence}
		box := " "
		if ticked[f.key()] {
			box = "x"
		}
		fmt.Fprintf(b, "- [%s] %s %s%s %s %s %s -->\n", box, o[1], feedbackMarkPrefix, f.Rule, f.Option, f.Category, f.Evidence)
	}
}

// lineLink links a line at the head (a permalink: it keeps pointing at what was judged).
func (r *Report) lineLink(p string, line int) string {
	if r.GitHub == "" {
		return fmt.Sprintf("`%s:%d`", p, line)
	}
	return fmt.Sprintf("[`%s:%d`](https://github.com/%s/blob/%s/%s#L%d)", p, line, r.GitHub, r.Head, p, line)
}

func fence(p string) string {
	switch path.Ext(p) {
	case ".go":
		return "go"
	case ".yaml", ".yml":
		return "yaml"
	case ".sql":
		return "sql"
	case ".md":
		return "markdown"
	}
	return ""
}

func cell(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "|", "\\|") }

// Feedback is one box ticked in a posted comment.
type Feedback struct {
	Rule     string `json:"rule"` // path:line
	Option   string `json:"option"`
	Category string `json:"category"` // the verdict the box was ticked on
	Evidence string `json:"evidence"`
}

func (f Feedback) key() string { return f.Rule + " " + f.Option + " " + f.Category }

var tickedBox = regexp.MustCompile(`(?m)^\s*[-*] \[[xX]\] .*<!-- docsp:fb (\S+) (\S+) (\S+) (\S*) ?-->`)

// Ticked reads the boxes ticked in a posted comment.
func Ticked(body string) []Feedback {
	out := []Feedback{}
	for _, m := range tickedBox.FindAllStringSubmatch(body, -1) {
		out = append(out, Feedback{Rule: m[1], Option: m[2], Category: m[3], Evidence: m[4]})
	}
	return out
}

// TickedKeys indexes feedback for MarkdownWith.
func TickedKeys(fb []Feedback) map[string]bool {
	m := map[string]bool{}
	for _, f := range fb {
		m[f.key()] = true
	}
	return m
}
