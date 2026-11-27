// Package prtext holds what the PR comments of docs-platform share (internal/prreview and
// internal/factorypr): the comment language, the notice of an incomplete run, the cap on the
// problems one comment shows, and GitHub workflow-command annotations. Only the words change
// with the language: the markers people's ticks are read back by (<!-- docsp:fb ... -->,
// <!-- docsp:sig ... -->) are identical in every language.
package prtext

import (
	"fmt"
	"strings"
)

// Lang is the language of a comment.
type Lang string

// Languages. Chinese is the default (the comments were Chinese before the option existed).
const (
	ZH Lang = "zh"
	EN Lang = "en"
	JA Lang = "ja"
)

// DefaultMaxProblems is how many problems one comment shows; the rest are counted and left
// to the job summary.
const DefaultMaxProblems = 10

// Parse reads a language name ("" = zh).
func Parse(s string) (Lang, error) {
	switch l := Lang(strings.ToLower(strings.TrimSpace(s))); l {
	case "":
		return ZH, nil
	case ZH, EN, JA:
		return l, nil
	}
	return "", fmt.Errorf("language %q: want zh, en or ja", s)
}

// T picks the text of the language (zh for an unknown one).
func (l Lang) T(zh, en, ja string) string {
	switch l {
	case EN:
		return en
	case JA:
		return ja
	case ZH:
	}
	return zh
}

// Reasons a run did not finish (Report.Incomplete).
const (
	// ReasonBudget: the run's budget cap left claims this change made stale unjudged.
	ReasonBudget = "budget"
	// ReasonJudgeFailures: the judge failed on some claims (errors, refusals, timeouts).
	ReasonJudgeFailures = "judge_failures"
	// ReasonPartial: fewer claims were judged than were picked.
	ReasonPartial = "partial"
	// ReasonAborted: a step of the run stopped with an error.
	ReasonAborted = "aborted"
)

// IncompleteHeadline is the sentence every incomplete run's comment and summary carry.
func IncompleteHeadline(l Lang) string {
	return l.T("**未完成:这不代表一切正常。**", "**Incomplete: this does not mean everything is fine.**", "**未完了:問題がないという意味ではありません。**")
}

// Incomplete renders the notice of an incomplete run ("" when complete).
func Incomplete(l Lang, reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "> ⚠️ %s %s\n", IncompleteHeadline(l),
		l.T("这次检查没有跑完,下面列的不是全部:", "This check did not finish; what is listed below is not everything:", "このチェックは最後まで実行されませんでした。以下がすべてではありません:"))
	for _, r := range reasons {
		fmt.Fprintf(&b, "> - %s\n", reasonText(l, r))
	}
	return b.String()
}

func reasonText(l Lang, r string) string {
	switch r {
	case ReasonBudget:
		return l.T("这次运行的 AI 预算用完了,有些被这个 PR 影响的文档行没有判定", "the run's AI budget ran out before every affected documentation line was judged", "実行の AI 予算が尽き、影響を受けたドキュメント行の一部が未判定です")
	case ReasonJudgeFailures:
		return l.T("AI 判定对部分文档行失败了(错误、超时或拒绝)", "the AI judge failed on some lines (errors, timeouts or refusals)", "AI 判定が一部の行で失敗しました(エラー・タイムアウト・拒否)")
	case ReasonPartial:
		return l.T("判定的文档行少于计划", "fewer lines were judged than were planned", "判定した行が予定より少なくなりました")
	case ReasonAborted:
		return l.T("检查的某一步出错中断了", "a step of the check stopped with an error", "チェックの途中のステップがエラーで止まりました")
	}
	return r
}

// More renders the line that counts the problems a comment leaves out.
func More(l Lang, n int, summaryURL string) string {
	if n <= 0 {
		return ""
	}
	where := l.T("任务摘要(job summary)", "the job summary", "ジョブサマリー")
	if summaryURL != "" {
		where = fmt.Sprintf("[%s](%s)", where, summaryURL)
	}
	return "\n" + l.T(fmt.Sprintf("……另外还有 **%d 处**没有在这里列出,完整列表见%s。\n", n, where),
		fmt.Sprintf("…and **%d more** not listed here: the full list is in %s.\n", n, where),
		fmt.Sprintf("…ほか **%d 件**はここに載せていません。全件は%sにあります。\n", n, where))
}

// Annotation renders a GitHub workflow command that annotates a line of a file
// (level: notice, warning or error). Properties and message are escaped, so text from the
// repository cannot start a command of its own.
func Annotation(level, file string, line int, title, msg string) string {
	return fmt.Sprintf("::%s file=%s,line=%d,title=%s::%s", level, escapeProperty(file), line, escapeProperty(title), escapeData(msg))
}

func escapeData(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

func escapeProperty(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", ":", "%3A", ",", "%2C").Replace(s)
}

// Cell makes text safe for one Markdown table cell (no pipes, no line breaks, bounded).
func Cell(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return strings.ReplaceAll(s, "|", "\\|")
}
