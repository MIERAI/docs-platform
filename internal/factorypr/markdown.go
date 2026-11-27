package factorypr

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factorytry"
	"github.com/MIERAI/docs-platform/internal/prtext"
)

func categoryLabel(l prtext.Lang, cat string) string {
	switch cat {
	case "confirmed":
		return l.T("✅ 仍然成立", "✅ still holds", "✅ 引き続き成立")
	case "contradicted":
		return l.T("❌ 代码和规则矛盾", "❌ the code contradicts the rule", "❌ コードとルールが矛盾")
	case "outdated_wording":
		return l.T("⚠️ 写法已过时", "⚠️ outdated wording", "⚠️ 表現が古い")
	case "incomplete_list":
		return l.T("⚠️ 列举不完整", "⚠️ incomplete list", "⚠️ 列挙が不完全")
	case "dangling_reference":
		return l.T("❌ 引用不存在", "❌ refers to something that does not exist", "❌ 参照先が存在しない")
	case "unverifiable_in_repo":
		return l.T("❔ 无法从代码确认", "❔ cannot be confirmed from the code", "❔ コードから確認できない")
	case "unknown":
		return l.T("❔ 未判定", "❔ not judged", "❔ 未判定")
	}
	return cat
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
// (by Feedback.Key: a tick on a verdict that changed is not carried over). Only what deciding
// takes is shown: per problem one sentence, one question answered by ticking; the evidence
// and the draft are folded.
func (r *Report) MarkdownWith(ticked map[string]bool) string {
	if len(r.Rules) == 0 {
		return ""
	}
	l := r.Lang
	var b strings.Builder
	n := r.Problems()
	inc := prtext.Incomplete(l, r.incomplete())
	if n == 0 {
		fmt.Fprintf(&b, "%s\n%s%s -->\n**docs-platform**:%s\n", Marker, sigPrefix, r.Signature(), l.T(
			fmt.Sprintf("这个 PR 改到的 %d 条设计书规则都和代码一致,不需要做什么。", len(r.Rules)),
			fmt.Sprintf(" the %d design rules this PR touches all match the code: nothing to do.", len(r.Rules)),
			fmt.Sprintf("この PR が触れた %d 件の設計書ルールはすべてコードと一致しています。対応は不要です。", len(r.Rules))))
		if inc != "" {
			b.WriteString("\n" + inc)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%s\n%s%s -->\n**docs-platform**:%s\n", Marker, sigPrefix, r.Signature(), l.T(
		fmt.Sprintf("这个 PR 让 **%d 条设计书规则**和代码不一致了。", n),
		fmt.Sprintf(" this PR makes **%d design rules** disagree with the code.", n),
		fmt.Sprintf("この PR で **%d 件の設計書ルール**がコードと一致しなくなりました。", n)))
	if inc != "" {
		b.WriteString("\n" + inc)
	}
	shown := map[string]string{} // patch -> the rule it was shown under
	limit := r.MaxProblems
	if limit <= 0 {
		limit = prtext.DefaultMaxProblems
	}
	k := 0
	for _, x := range r.Rules {
		if problem[x.Category] {
			if k < limit {
				r.writeProblem(&b, x, ticked, shown)
			}
			k++
		}
	}
	b.WriteString(prtext.More(l, k-limit, r.SummaryURL))
	r.writeRest(&b)
	b.WriteString("\n<sub>" + l.T("🔬 = 工厂在这个 PR 上实际跑了设计书的例子,确认不一致;📖 = AI 读代码的判断,可能是误报。勾选只用来统计这个检查的误报率。平台不会改任何文件。",
		"🔬 = the factory ran the design's examples on this PR and confirmed the mismatch; 📖 = the AI's reading of the code, it can be wrong. Ticks are only used to measure this check's false-positive rate. The platform never changes a file.",
		"🔬 = ファクトリーがこの PR で設計書の例を実際に実行し不一致を確認;📖 = AI がコードを読んだ判断で、誤検知の可能性があります。チェックは誤検知率の集計にだけ使います。プラットフォームはファイルを一切変更しません。") + "</sub>\n")
	return b.String()
}

// incomplete: the stated reasons, and judge failures on touched rules.
func (r *Report) incomplete() []string {
	out := append([]string(nil), r.Incomplete...)
	for _, x := range r.Rules {
		if x.Error != "" || x.Category == "" || x.Category == agentic.Unknown {
			if !slices.Contains(out, prtext.ReasonJudgeFailures) {
				out = append(out, prtext.ReasonJudgeFailures)
			}
			break
		}
	}
	return out
}

// Summary renders the job summary: every touched rule's verdict and, for an incomplete
// run, the same notice as the comment.
func (r *Report) Summary() string {
	l := r.Lang
	var b strings.Builder
	fmt.Fprintf(&b, "### docs-platform: %s\n\n", l.T(fmt.Sprintf("%d 条设计书规则和代码不一致(核对了 %d 条)", r.Problems(), len(r.Rules)),
		fmt.Sprintf("%d design rules disagree with the code (%d checked)", r.Problems(), len(r.Rules)),
		fmt.Sprintf("設計書ルール %d 件がコードと不一致(確認 %d 件)", r.Problems(), len(r.Rules))))
	if s := prtext.Incomplete(l, r.incomplete()); s != "" {
		b.WriteString(s + "\n")
	}
	if len(r.Rules) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "| %s | %s | %s |\n|---|---|---|\n", l.T("位置", "Line", "位置"), l.T("判定", "Verdict", "判定"), l.T("规则", "Rule", "ルール"))
	for _, x := range r.Rules {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.lineLink(x.Path, x.Line), categoryLabel(l, x.Category), prtext.Cell(firstLine(x.Text), 160))
	}
	return b.String()
}

// Annotations are GitHub workflow commands that put a warning on each problem rule's line.
func (r *Report) Annotations() []string {
	var out []string
	for _, x := range r.Rules {
		if !problem[x.Category] {
			continue
		}
		msg := strings.TrimSpace(firstLine(x.Text))
		if x.Draft != nil && x.Draft.Summary != "" {
			msg = x.Draft.Summary
		}
		out = append(out, prtext.Annotation("warning", x.Path, x.Line, "docs-platform: "+categoryLabel(r.Lang, x.Category), msg))
	}
	return out
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
	l := r.Lang
	q := l.T("设计书该改,还是代码该改?", "Should the design change, or the code?", "設計書を直すべきですか、それともコードですか?")
	if d != nil && d.Question != "" {
		q = d.Question
	}
	fmt.Fprintf(b, "**%s**\n\n", q)
	writeChoices(b, l, x, ticked)
	fmt.Fprintf(b, "\n<details><summary>%s</summary>\n\n", l.T("证据和改稿", "Evidence and draft", "根拠と修正案"))
	block := x.Block
	if block == "" {
		block = x.Text
	}
	fmt.Fprintf(b, "%s %s\n\n```%s\n%s\n```\n\n", l.T("设计书", "Design", "設計書"), r.lineLink(x.Path, x.Line), fence(x.Path), strings.TrimRight(block, "\n"))
	for _, c := range x.Code {
		fmt.Fprintf(b, "%s %s\n\n```%s\n%s\n```\n\n", l.T("代码", "Code", "コード"), r.lineLink(c.Path, c.Line), fence(c.Path), strings.TrimRight(c.Text, "\n"))
	}
	r.writeCounterexample(b, x)
	r.writeCause(b, x)
	switch {
	case d != nil && d.Patch != "" && shown[d.Patch] != "":
		fmt.Fprintf(b, l.T("改稿:和 `%s` 的相同。\n\n", "Draft: the same as for `%s`.\n\n", "修正案:`%s` と同じです。\n\n"), shown[d.Patch])
	default:
		writeDraft(b, l, d)
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
	fmt.Fprintf(b, r.Lang.T("\n<details><summary>另外 %d 条规则也核对过</summary>\n\n", "\n<details><summary>%d more rules were checked</summary>\n\n", "\n<details><summary>ほかに %d 件のルールも確認済み</summary>\n\n"), len(rest))
	for _, x := range rest {
		label := categoryLabel(r.Lang, x.Category)
		fmt.Fprintf(b, "- %s %s — %s\n", r.lineLink(x.Path, x.Line), cell(strings.TrimPrefix(strings.TrimSpace(firstLine(x.Text)), "- ")), label)
	}
	b.WriteString("\n</details>\n")
}

// writeCounterexample: executed failures of the design's examples first; the drafter's
// worked input only when nothing was run.
func (r *Report) writeCounterexample(b *strings.Builder, x Rule) {
	l := r.Lang
	if f := r.Failing[x.API]; len(f) > 0 {
		if x.Evidence == EvidenceExecuted {
			b.WriteString(l.T("反例(工厂在这个 PR 上实际跑的设计书例子):\n\n", "Counterexamples (the design's examples the factory ran on this PR):\n\n", "反例(ファクトリーがこの PR で実際に実行した設計書の例):\n\n"))
		} else {
			b.WriteString(l.T("这个 API 失败的设计书例子(实际跑过,但改稿没有改这条规则,不能证明是这条的问题):\n\n",
				"The design's examples that fail for this API (run, but the draft does not change this rule, so they do not prove this rule is the problem):\n\n",
				"この API で失敗する設計書の例(実行済み。ただし修正案はこのルールを変えていないため、このルールの問題だとは証明できません):\n\n"))
		}
		for _, c := range f[:min(4, len(f))] {
			fmt.Fprintf(b, "- `%s`:%s\n", c.Name, caseDetail(l, c))
		}
		if len(f) > 4 {
			fmt.Fprintf(b, l.T("- 另外还有 %d 个\n", "- and %d more\n", "- ほか %d 件\n"), len(f)-4)
		}
		b.WriteString("\n")
		return
	}
	if x.Draft != nil && x.Draft.Counterexample != "" {
		fmt.Fprintf(b, l.T("反例(AI 推出来的,没有实际跑过):%s\n\n", "Counterexample (inferred by the AI, not run): %s\n\n", "反例(AI の推論で、実行はしていません):%s\n\n"), x.Draft.Counterexample)
	}
}

var wantGot = regexp.MustCompile(`^(\S+): want (.+?), got (.+?)(?: \(body .*\))?$`)

// caseDetail turns the factory's "response.x: want A, got B (body …)" into plain words.
func caseDetail(l prtext.Lang, c factorytry.Case) string {
	if m := wantGot.FindStringSubmatch(c.Detail); m != nil {
		got := m[3]
		if got == "<nil>" {
			got = l.T("没有这个值(0 / 空)", "no value (0 / empty)", "値なし(0 / 空)")
		}
		return fmt.Sprintf(l.T("`%s` 设计书预期 **%s**,代码实际是 **%s**", "`%s` the design expects **%s**, the code gives **%s**", "`%s` 設計書の期待値は **%s**、コードの実際は **%s**"), m[1], m[2], got)
	}
	d := c.Detail
	if len([]rune(d)) > 160 {
		d = string([]rune(d)[:160]) + "…"
	}
	return l.T("失败 — ", "failed — ", "失敗 — ") + d
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
	b.WriteString(r.Lang.T("引起它的改动:\n\n", "The change behind it:\n\n", "原因となった変更:\n\n"))
	for _, f := range files {
		fmt.Fprintf(b, "`%s`\n\n```diff\n%s\n```\n\n", f, r.Diffs[f])
	}
}

func writeDraft(b *strings.Builder, l prtext.Lang, d *Draft) {
	switch {
	case d == nil:
		return
	case len(d.Edits) == 0:
		fmt.Fprintf(b, l.T("改稿:没有提出(%s)\n\n", "Draft: none proposed (%s)\n\n", "修正案:なし(%s)\n\n"), d.Problem)
		return
	case d.Verified:
		b.WriteString(l.T("改稿(✅ 工厂验证通过;在 PR 分支上存成 `design.patch` 后 `git apply design.patch`):\n\n",
			"Draft (✅ verified by the factory; save it as `design.patch` on the PR branch, then `git apply design.patch`):\n\n",
			"修正案(✅ ファクトリーで検証済み。PR ブランチで `design.patch` として保存し `git apply design.patch`):\n\n"))
	default:
		fmt.Fprintf(b, l.T("改稿(⚠️ 未通过验证:%s,仅供参考):\n\n", "Draft (⚠️ not verified: %s; for reference only):\n\n", "修正案(⚠️ 未検証:%s。参考のみ):\n\n"), d.Problem)
	}
	if d.Patch != "" {
		fmt.Fprintf(b, "```diff\n%s```\n\n", d.Patch)
	}
}

// writeChoices: the answers to the question, as boxes. A 🔬 problem has no "not a problem":
// the design's examples fail on this rule.
func writeChoices(b *strings.Builder, l prtext.Lang, x Rule, ticked map[string]bool) {
	opts := [][2]string{
		{OptionDesignStale, l.T("是:设计书过时了(采用折叠里的改稿)", "Yes: the design is out of date (take the folded draft)", "はい:設計書が古い(折りたたみ内の修正案を採用)")},
		{OptionCodeWrong, l.T("不是:代码改错了", "No: the code is wrong", "いいえ:コードの誤り")},
	}
	if x.Evidence != EvidenceExecuted {
		opts = append(opts, [2]string{OptionNotAProblem, l.T("都不是:这里没有不一致(误报)", "Neither: nothing is inconsistent here (false positive)", "どちらでもない:不一致はない(誤検知)")})
	}
	for _, o := range opts {
		f := Feedback{Rule: fmt.Sprintf("%s:%d", x.Path, x.Line), Option: o[0], Category: x.Category, Evidence: x.Evidence}
		box := " "
		if ticked[f.Key()] {
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

const sigPrefix = "<!-- docsp:sig "

// Signature identifies the findings - each rule's verdict and evidence - and not the commit
// they were judged at or the wording of a draft (which varies from run to run): a new commit
// with the same findings leaves the posted comment alone (an update while someone has the
// page open makes their tick fail).
func (r *Report) Signature() string {
	type v struct {
		Rule, Category, Evidence string
	}
	vs := make([]v, 0, len(r.Rules))
	for _, x := range r.Rules {
		vs = append(vs, v{fmt.Sprintf("%s:%d", x.Path, x.Line), x.Category, x.Evidence})
	}
	// an incomplete run and a language other than the default are part of it (a complete
	// zh run signs as before)
	for _, x := range r.Incomplete {
		vs = append(vs, v{"incomplete", x, ""})
	}
	if r.Lang != "" && r.Lang != prtext.ZH {
		vs = append(vs, v{"lang", string(r.Lang), ""})
	}
	b, _ := json.Marshal(vs)
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}

// Unchanged says whether a posted comment already says what this report would.
func (r *Report) Unchanged(body string) bool {
	if len(r.Rules) == 0 {
		return strings.Contains(body, noRulesText(r.Lang))
	}
	return strings.Contains(body, sigPrefix+r.Signature()+" -->")
}

// noRulesText replaces a posted comment once the change touches no rule any more.
func noRulesText(l prtext.Lang) string {
	return l.T("这个 PR 目前没有动到有手写代码的业务规则。", "This PR currently touches no business rule with hand-written code.", "この PR は現在、手書きコードを持つ業務ルールに触れていません。")
}

// NoRulesComment is that replacement in the report's language.
func (r *Report) NoRulesComment() string { return Marker + "\n" + noRulesText(r.Lang) + "\n" }

// NoRulesComment is the zh replacement (kept for callers of the first version).
func NoRulesComment() string { return (&Report{}).NoRulesComment() }

// Feedback is one box ticked in a posted comment.
type Feedback struct {
	Rule     string `json:"rule"` // path:line
	Option   string `json:"option"`
	Category string `json:"category"` // the verdict the box was ticked on
	Evidence string `json:"evidence"`
}

// Key identifies a box: the rule, the option and the verdict it was ticked on.
func (f Feedback) Key() string { return f.Rule + " " + f.Option + " " + f.Category }

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
		m[f.Key()] = true
	}
	return m
}
