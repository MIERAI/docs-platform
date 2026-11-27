package factorypr

import (
	"fmt"
	"path"
	"regexp"
	"sort"
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

var evidenceLabel = map[string]string{
	EvidenceExecuted: "🔬 工厂实际跑过",
	EvidenceRead:     "📖 AI 读代码得出",
}

// Feedback options a person ticks in the comment.
const (
	OptionDesignStale  = "design_stale"  // the design is out of date: the draft is (or will be) applied
	OptionCodeWrong    = "code_wrong"    // the code is wrong: it will be fixed
	OptionNotAProblem  = "not_a_problem" // the design still says what the code does (a false positive)
	OptionNotThisRule  = "unrelated"     // the rule is not what this change is about (a false positive)
	feedbackMarkPrefix = "<!-- docsp:fb "
)

// Markdown renders the review as a PR comment (with Marker, so it is updated in place).
// It returns "" when the change touches no rule with hand-written code.
func (r *Report) Markdown() string { return r.MarkdownWith(nil) }

// MarkdownWith renders the comment keeping the boxes people ticked in the previous version
// (by Feedback.key: a tick on a verdict that changed is not carried over).
func (r *Report) MarkdownWith(ticked map[string]bool) string {
	if len(r.Rules) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n### 设计书 ↔ 手写代码 · docs-platform\n\n", Marker)
	n := r.Problems()
	fmt.Fprintf(&b, "这个 PR 改到了 %d 条业务规则背后的手写代码。在 %s 上重新核对:", len(r.Rules), r.commitLink())
	if n > 0 {
		fmt.Fprintf(&b, "**%d 条需要你决定**,其余仍然成立。\n\n", n)
	} else {
		b.WriteString("全部仍然成立,不需要做什么。\n\n")
	}
	b.WriteString("| 规则 | 结论 | 证据 |\n|---|---|---|\n")
	for _, x := range r.Rules {
		label := categoryLabel[x.Category]
		if label == "" {
			label = x.Category
		}
		if x.Error != "" && x.Category == "unknown" {
			label += " (判定失败)"
		}
		fmt.Fprintf(&b, "| %s %s | %s | %s |\n", r.lineLink(x.Path, x.Line), cell(firstLine(x.Text)), label, evidenceLabel[x.Evidence])
	}
	for _, x := range r.Rules {
		if problem[x.Category] {
			r.writeProblem(&b, x, ticked)
		}
	}
	r.writeAcceptance(&b)
	b.WriteString("\n<sub>🔬 = 设计书的例子在这个 PR 上由工厂实际跑过并失败:两边确实不一致。📖 = 只是 AI 读代码的判断,可能是误报。" +
		"改稿只有在工厂重新生成、编译并通过设计书的全部用例时才标为已验证(手写测试属于代码,不算在内)。" +
		"平台不会修改任何文件;勾选会被读回,只用来统计这个检查的误报率,不用来评价人。</sub>\n")
	return b.String()
}

func (r *Report) writeProblem(b *strings.Builder, x Rule, ticked map[string]bool) {
	fmt.Fprintf(b, "\n---\n\n#### %s %s — %s\n\n", categoryLabel[x.Category], r.lineLink(x.Path, x.Line), cell(firstLine(x.Text)))
	d := x.Draft
	switch {
	case d != nil && d.Summary != "":
		fmt.Fprintf(b, "**一句话**:%s\n\n", d.Summary)
	default:
		for _, f := range x.Facts {
			if f.Status != "confirmed" {
				fmt.Fprintf(b, "- %s — %s\n", f.Fact, f.Status)
			}
		}
		b.WriteString("\n")
	}
	if d != nil && d.Question != "" {
		fmt.Fprintf(b, "**只需要回答**:%s\n\n", d.Question)
	}
	fmt.Fprintf(b, "**设计书** %s\n\n```%s\n%s\n```\n\n", r.lineLink(x.Path, x.Line), fence(x.Path), strings.TrimRight(x.Text, "\n"))
	for _, c := range x.Code {
		fmt.Fprintf(b, "**代码** %s\n\n```%s\n%s\n```\n\n", r.lineLink(c.Path, c.Line), fence(c.Path), strings.TrimRight(c.Text, "\n"))
	}
	r.writeCounterexample(b, x)
	r.writeCause(b, x)
	writeDraft(b, d)
	writeChoices(b, x, ticked)
}

// writeCounterexample: executed failures of the design's examples first; the drafter's
// worked input only when nothing was run.
func (r *Report) writeCounterexample(b *strings.Builder, x Rule) {
	if f := r.Failing[x.API]; len(f) > 0 {
		b.WriteString("**反例**(🔬 工厂在这个 PR 上实际跑的设计书例子):\n\n")
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
		fmt.Fprintf(b, "**反例**(📖 AI 推出来的,没有实际跑过):%s\n\n", x.Draft.Counterexample)
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
	b.WriteString("<details><summary>引起它的改动(本 PR)</summary>\n\n")
	for _, f := range files {
		fmt.Fprintf(b, "`%s`\n\n```diff\n%s\n```\n\n", f, r.Diffs[f])
	}
	b.WriteString("</details>\n\n")
}

func writeDraft(b *strings.Builder, d *Draft) {
	switch {
	case d == nil:
		return
	case len(d.Edits) == 0:
		fmt.Fprintf(b, "设计书改稿:没有提出(%s)\n\n", d.Problem)
		return
	case d.Verified:
		b.WriteString("**设计书改稿** — ✅ 工厂验证通过(重新生成 + 编译 + 设计书的全部用例):\n\n")
	default:
		fmt.Fprintf(b, "**设计书改稿** — ⚠️ 未通过验证(%s),仅供参考:\n\n", d.Problem)
	}
	if d.Patch != "" {
		fmt.Fprintf(b, "```diff\n%s```\n\n", d.Patch)
		b.WriteString("<details><summary>怎么采用</summary>\n\n在这个 PR 的分支上,把上面的补丁存成 `design.patch`,然后 `git apply design.patch` 并提交。\n\n</details>\n\n")
	}
	if d.Rationale != "" {
		fmt.Fprintf(b, "<details><summary>改稿说明(AI)</summary>\n\n%s\n\n</details>\n\n", d.Rationale)
	}
}

func writeChoices(b *strings.Builder, x Rule, ticked map[string]bool) {
	b.WriteString("**你的判断**(勾一项即可):\n\n")
	opts := [][2]string{{OptionDesignStale, "设计书过时了:采用上面的改稿"}, {OptionCodeWrong, "代码改错了:我会修代码"}}
	if x.Evidence != EvidenceExecuted {
		opts = append(opts, [2]string{OptionNotAProblem, "不是问题:设计书的意思其实没错"}, [2]string{OptionNotThisRule, "不是问题:这条规则和这次改动无关"})
	}
	for _, o := range opts {
		f := Feedback{Rule: fmt.Sprintf("%s:%d", x.Path, x.Line), Option: o[0], Category: x.Category, Evidence: x.Evidence}
		box := " "
		if ticked[f.key()] {
			box = "x"
		}
		fmt.Fprintf(b, "- [%s] %s %s%s %s %s %s -->\n", box, o[1], feedbackMarkPrefix, f.Rule, f.Option, f.Category, f.Evidence)
	}
	if x.Evidence == EvidenceExecuted {
		b.WriteString("\n> 设计书的例子在这个 PR 上实际跑失败了,所以这里没有\"不是问题\"的选项:两边确实不一致,要决定的只是改哪一边。\n")
	}
}

func (r *Report) writeAcceptance(b *strings.Builder) {
	if len(r.Acceptance) == 0 {
		return
	}
	apis := make([]string, 0, len(r.Acceptance))
	for a := range r.Acceptance {
		apis = append(apis, a)
	}
	sort.Strings(apis)
	b.WriteString("\n<details><summary>工厂验收(这个 PR 的 head,不加改稿)</summary>\n\n")
	for _, a := range apis {
		p := r.Acceptance[a]
		if p == "" {
			p = "全部通过"
		}
		fmt.Fprintf(b, "- `%s`: %s\n", a, p)
	}
	b.WriteString("\n</details>\n")
}

func (r *Report) commitLink() string {
	if r.GitHub == "" {
		return fmt.Sprintf("`%.12s`", r.Head)
	}
	return fmt.Sprintf("[`%.12s`](https://github.com/%s/commit/%s)", r.Head, r.GitHub, r.Head)
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
