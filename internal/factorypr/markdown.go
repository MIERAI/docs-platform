package factorypr

import (
	"fmt"
	"strings"
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

// Markdown renders the review as a PR comment (with Marker, so it is updated in place).
// It returns "" when the change touches no rule with hand-written code.
func (r *Report) Markdown() string {
	if len(r.Rules) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n### 设计书 ↔ 手写代码 (docs-platform)\n\n", Marker)
	fmt.Fprintf(&b, "这个 PR 动到了 %d 条有手写代码的业务规则(逃生层)。在 `%.12s` 上重新核对的结果:\n\n", len(r.Rules), r.Head)
	b.WriteString("| 规则 | 结论 | 相关的改动 |\n|---|---|---|\n")
	for _, x := range r.Rules {
		label := categoryLabel[x.Category]
		if label == "" {
			label = x.Category
		}
		if x.Error != "" && x.Category == "unknown" {
			label += " (判定失败)"
		}
		fmt.Fprintf(&b, "| `%s:%d` %s | %s | %s |\n", x.Path, x.Line, cell(firstLine(x.Text)), label, cell("`"+strings.Join(x.Touched, "`, `")+"`"))
	}
	for _, x := range r.Rules {
		if problem[x.Category] {
			writeProblem(&b, x)
		}
	}
	if len(r.Acceptance) > 0 {
		b.WriteString("\n<details><summary>工厂验收(这个 PR 的 head,不加改稿)</summary>\n\n")
		for a, p := range r.Acceptance {
			if p == "" {
				p = "全部通过"
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", a, p)
		}
		b.WriteString("\n</details>\n")
	}
	b.WriteString("\n<sub>判定由 AI 做。改稿只有在工厂重新生成、编译并通过设计书的全部用例(例子、逻辑样例、自动用例、差分用例)时才标为已验证;手写测试属于代码,不算在内。不会自动修改任何文件。</sub>\n")
	return b.String()
}

func writeProblem(b *strings.Builder, x Rule) {
	fmt.Fprintf(b, "\n#### `%s:%d`\n\n", x.Path, x.Line)
	for _, f := range x.Facts {
		if f.Status != "confirmed" {
			fmt.Fprintf(b, "- %s — %s\n", f.Fact, f.Status)
		}
	}
	for _, q := range x.Quotes[:min(3, len(x.Quotes))] {
		fmt.Fprintf(b, "- 依据: `%s:%d`\n", q.Path, q.Line)
	}
	b.WriteString("\n二选一由人来定: **设计书过时了**(采用下面的改稿),还是 **代码改错了**(修代码)。\n")
	writeDraft(b, x.Draft)
}

func writeDraft(b *strings.Builder, d *Draft) {
	switch {
	case d == nil:
		return
	case len(d.Edits) == 0:
		fmt.Fprintf(b, "\n设计书改稿: 没有提出 (%s)\n", d.Problem)
		return
	case d.Verified:
		b.WriteString("\n设计书改稿 — ✅ 工厂验证通过(重新生成 + 编译 + 设计书的全部用例):\n")
	default:
		fmt.Fprintf(b, "\n设计书改稿 — ⚠️ 未通过验证 (%s),仅供参考:\n", d.Problem)
	}
	b.WriteString("\n```diff\n")
	for _, e := range d.Edits {
		fmt.Fprintf(b, "# %s\n", e.Path)
		for _, l := range strings.Split(strings.TrimSuffix(e.Old, "\n"), "\n") {
			b.WriteString("- " + l + "\n")
		}
		for _, l := range strings.Split(strings.TrimSuffix(e.New, "\n"), "\n") {
			b.WriteString("+ " + l + "\n")
		}
	}
	b.WriteString("```\n")
	if d.Rationale != "" {
		fmt.Fprintf(b, "\n理由: %s\n", d.Rationale)
	}
}

func cell(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "|", "\\|") }
