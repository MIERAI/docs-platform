// Package prreview turns a pull request's recheck into a comment (docs-platform/pr-review/v1).
// The ledger of the default branch is copied, the claims the change made stale are judged
// again at the PR's head (docsp stale, schedule, recheck on the copy), and the two ledgers are
// compared: a claim is reported when it was judged at the head and is now a problem while it
// was not one before - the problems this change introduces, not the backlog. A claim the
// code disagrees with itself about (sources_conflict) is reported apart: the document was
// not judged against either side.
package prreview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/prtext"
)

// SchemaVersion of docs-platform/pr-review/v1 (1.1: sources_conflict, incomplete).
const SchemaVersion = "1.1"

// Marker identifies the review's PR comment (updated in place, never duplicated).
const Marker = "<!-- docsp:pr -->"

// SourcesConflict is the category of a claim the repository disagrees with itself about.
const SourcesConflict = "sources_conflict"

// Reported categories: the document problems, and sources_conflict.
var problem = map[string]bool{"contradicted": true, "outdated_wording": true, "incomplete_list": true, "dangling_reference": true, SourcesConflict: true}

// Line is one line of a file at the head.
type Line struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Problem is a claim the change turned into a problem.
type Problem struct {
	ID       string        `json:"id"`
	Path     string        `json:"path"`
	Line     int           `json:"line"`
	Text     string        `json:"text"`
	Category string        `json:"category"`
	Before   string        `json:"before"` // category before the change ("" = not in the ledger)
	Facts    []ledger.Fact `json:"facts,omitempty"`
	Code     []Line        `json:"code,omitempty"`
}

// Report is docs-platform/pr-review/v1.
type Report struct {
	SchemaVersion string               `json:"schema_version"`
	Repo          string               `json:"repo"`
	Head          string               `json:"head"`
	GitHub        string               `json:"github,omitempty"`
	Rechecked     int                  `json:"rechecked"` // claims judged at the head
	Problems      []Problem            `json:"problems"`
	Feedback      []factorypr.Feedback `json:"feedback,omitempty"`
	// Incomplete lists why the run did not finish (prtext.Reason*); empty = it did. An
	// incomplete run's comment says so: no problem listed is not "everything is fine".
	Incomplete []string `json:"incomplete,omitempty"`

	// Rendering options (not part of the report).
	Lang        prtext.Lang `json:"-"` // "" = zh
	MaxProblems int         `json:"-"` // problems one comment shows (0 = prtext.DefaultMaxProblems)
	SummaryURL  string      `json:"-"` // where the full list is (the job summary)
}

// Lines reads a line of a file at the head.
type Lines interface {
	LineText(ctx context.Context, path string, n int) (string, bool)
}

// Compare lists the claims judged at head (after) that are problems and were not before.
func Compare(ctx context.Context, before, after *ledger.Ledger, head string, lines Lines) *Report {
	rep := &Report{SchemaVersion: SchemaVersion, Head: head, Problems: []Problem{}}
	for i := range after.Claims {
		c := &after.Claims[i]
		if c.VerifiedRef != head {
			continue
		}
		rep.Rechecked++
		rep.Repo = c.Repo
		was := ""
		if o := before.Find(c.ID); o != nil {
			was = o.Category
		}
		if !problem[c.Category] || problem[was] {
			continue
		}
		p := Problem{ID: c.ID, Path: c.Path, Line: c.Line, Text: c.Text, Category: c.Category, Before: was, Facts: c.Facts}
		for _, q := range c.Evidence.Quotes[:min(3, len(c.Evidence.Quotes))] {
			if t, ok := lines.LineText(ctx, q.Path, q.Line); ok {
				p.Code = append(p.Code, Line{Path: q.Path, Line: q.Line, Text: t})
			}
		}
		rep.Problems = append(rep.Problems, p)
	}
	sort.Slice(rep.Problems, func(i, j int) bool {
		a, b := rep.Problems[i], rep.Problems[j]
		if (a.Category == SourcesConflict) != (b.Category == SourcesConflict) {
			return b.Category == SourcesConflict // document problems first
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return rep
}

// counts are the document problems and the source conflicts.
func (r *Report) counts() (int, int) {
	n, c := 0, 0
	for _, p := range r.Problems {
		if p.Category == SourcesConflict {
			c++
		} else {
			n++
		}
	}
	return n, c
}

func categoryLabel(l prtext.Lang, cat string) string {
	switch cat {
	case "contradicted":
		return l.T("和代码矛盾", "contradicts the code", "コードと矛盾")
	case "outdated_wording":
		return l.T("写法已过时", "outdated wording", "表現が古い")
	case "incomplete_list":
		return l.T("列举不完整", "incomplete list", "列挙が不完全")
	case "dangling_reference":
		return l.T("引用的东西不存在", "refers to something that does not exist", "参照先が存在しない")
	case SourcesConflict:
		return l.T("代码内部说法不一", "the code disagrees with itself", "コード内で食い違い")
	}
	return cat
}

// Markdown renders the comment ("" when the change introduces no problem and the run
// finished), keeping the boxes people ticked in the previous version. At most MaxProblems
// problems are shown; the rest are counted, with a link to the job summary.
func (r *Report) Markdown(ticked map[string]bool) string {
	if len(r.Problems) == 0 && len(r.Incomplete) == 0 {
		return ""
	}
	l := r.Lang
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s%s -->\n", Marker, sigPrefix, r.Signature())
	n, c := r.counts()
	switch {
	case n > 0:
		b.WriteString(l.T(fmt.Sprintf("**docs-platform**:这个 PR 改了代码,让 **%d 处文档**和代码不一致了。\n", n),
			fmt.Sprintf("**docs-platform**: this PR changed the code so that **%d documentation lines** no longer match it.\n", n),
			fmt.Sprintf("**docs-platform**:この PR のコード変更で **%d 箇所のドキュメント**がコードと合わなくなりました。\n", n)))
	case c == 0:
		b.WriteString(l.T("**docs-platform**:目前没有发现这个 PR 让文档和代码不一致。\n",
			"**docs-platform**: no documentation line this PR makes inconsistent was found so far.\n",
			"**docs-platform**:この PR でコードと合わなくなったドキュメントは今のところ見つかっていません。\n"))
	}
	if c > 0 {
		b.WriteString(l.T(fmt.Sprintf("**docs-platform**:有 **%d 处文档**依据的代码自己说法不一(比如两处代码设了不同的默认值),没有拿文档去和其中一方比。\n", c),
			fmt.Sprintf("**docs-platform**: for **%d documentation lines** the code disagrees with itself (e.g. two places set different defaults); they were not judged against either side.\n", c),
			fmt.Sprintf("**docs-platform**:**%d 箇所のドキュメント**について、根拠となるコード同士が食い違っています(例:2 箇所で異なるデフォルト値)。どちらか一方とは比較していません。\n", c)))
	}
	if s := prtext.Incomplete(l, r.Incomplete); s != "" {
		b.WriteString("\n" + s)
	}
	limit := r.MaxProblems
	if limit <= 0 {
		limit = prtext.DefaultMaxProblems
	}
	for i, p := range r.Problems {
		if i == limit {
			break
		}
		r.writeProblem(&b, p, ticked)
	}
	b.WriteString(prtext.More(l, len(r.Problems)-limit, r.SummaryURL))
	b.WriteString("\n<sub>" + l.T("📖 = AI 读代码的判断,可能是误报。勾选只用来统计这个检查的误报率。平台不会改任何文件。",
		"📖 = the AI's reading of the code; it can be wrong. Ticks are only used to measure this check's false-positive rate. The platform never changes a file.",
		"📖 = AI がコードを読んだ判断で、誤検知の可能性があります。チェックは誤検知率の集計にだけ使います。プラットフォームはファイルを一切変更しません。") + "</sub>\n")
	return b.String()
}

func (r *Report) writeProblem(b *strings.Builder, p Problem, ticked map[string]bool) {
	l := r.Lang
	fmt.Fprintf(b, "\n#### %s · %s · 📖\n\n", r.lineLink(p.Path, p.Line), categoryLabel(l, p.Category))
	for _, f := range p.Facts {
		if f.Status != "confirmed" && f.Valid {
			fmt.Fprintf(b, "%s\n\n", f.Fact)
			break
		}
	}
	q := l.T("文档该改,还是代码改错了?", "Should the document change, or is the code wrong?", "ドキュメントを直すべきですか、それともコードの誤りですか?")
	opts := [][2]string{
		{factorypr.OptionDesignStale, l.T("文档过时了:我会改文档", "The document is out of date: I will fix it", "ドキュメントが古い:ドキュメントを直します")},
		{factorypr.OptionCodeWrong, l.T("代码改错了:我会修代码", "The code is wrong: I will fix it", "コードの誤り:コードを直します")},
		{factorypr.OptionNotAProblem, l.T("都不是:这里没有不一致(误报)", "Neither: nothing is inconsistent here (false positive)", "どちらでもない:不一致はない(誤検知)")},
	}
	if p.Category == SourcesConflict {
		q = l.T("代码里哪一处是对的?", "Which place in the code is right?", "コードのどちらが正しいですか?")
		opts = [][2]string{
			{factorypr.OptionCodeWrong, l.T("有一处代码错了:我会修代码(再看文档)", "One of them is wrong: I will fix the code (then the document)", "どちらかが誤り:コードを直します(その後ドキュメントも)")},
			{factorypr.OptionNotAProblem, l.T("都不是:代码并不矛盾(误报)", "Neither: the code does not disagree (false positive)", "どちらでもない:コードは食い違っていない(誤検知)")},
		}
	}
	fmt.Fprintf(b, "**%s**\n\n", q)
	for _, o := range opts {
		f := factorypr.Feedback{Rule: fmt.Sprintf("%s:%d", p.Path, p.Line), Option: o[0], Category: p.Category, Evidence: factorypr.EvidenceRead}
		box := " "
		if ticked[f.Key()] {
			box = "x"
		}
		fmt.Fprintf(b, "- [%s] %s <!-- docsp:fb %s %s %s %s -->\n", box, o[1], f.Rule, f.Option, f.Category, f.Evidence)
	}
	fmt.Fprintf(b, "\n<details><summary>%s</summary>\n\n", l.T("证据", "Evidence", "根拠"))
	fmt.Fprintf(b, "%s %s\n\n```%s\n%s\n```\n\n", l.T("文档", "Document", "ドキュメント"), r.lineLink(p.Path, p.Line), fence(p.Path), strings.TrimSpace(p.Text))
	for _, c := range p.Code {
		fmt.Fprintf(b, "%s %s\n\n```%s\n%s\n```\n\n", l.T("代码", "Code", "コード"), r.lineLink(c.Path, c.Line), fence(c.Path), strings.TrimRight(c.Text, "\n"))
	}
	for _, f := range p.Facts {
		if f.Valid {
			fmt.Fprintf(b, "- %s — %s\n", f.Fact, f.Status)
		}
	}
	b.WriteString("\n</details>\n")
}

// Summary renders the job summary: every problem (the comment shows at most MaxProblems)
// and, for an incomplete run, the same notice as the comment.
func (r *Report) Summary() string {
	l := r.Lang
	var b strings.Builder
	n, c := r.counts()
	fmt.Fprintf(&b, "### docs-platform: %s\n\n", l.T(fmt.Sprintf("%d 处文档不一致,%d 处代码自相矛盾(判定了 %d 条)", n, c, r.Rechecked),
		fmt.Sprintf("%d documentation problems, %d source conflicts (%d lines judged)", n, c, r.Rechecked),
		fmt.Sprintf("ドキュメントの不一致 %d 件、コード内の食い違い %d 件(判定 %d 行)", n, c, r.Rechecked)))
	if s := prtext.Incomplete(l, r.Incomplete); s != "" {
		b.WriteString(s + "\n")
	}
	if len(r.Problems) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "| %s | %s | %s |\n|---|---|---|\n", l.T("位置", "Line", "位置"), l.T("类别", "Category", "分類"), l.T("文档", "Document", "ドキュメント"))
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.lineLink(p.Path, p.Line), categoryLabel(l, p.Category), prtext.Cell(p.Text, 160))
	}
	return b.String()
}

// Annotations are GitHub workflow commands that put a warning on each problem's line.
func (r *Report) Annotations() []string {
	out := make([]string, 0, len(r.Problems))
	for _, p := range r.Problems {
		msg := strings.TrimSpace(p.Text)
		for _, f := range p.Facts {
			if f.Status != "confirmed" && f.Valid {
				msg = f.Fact
				break
			}
		}
		out = append(out, prtext.Annotation("warning", p.Path, p.Line, "docs-platform: "+categoryLabel(r.Lang, p.Category), msg))
	}
	return out
}

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
	case ".md":
		return "markdown"
	case ".tf", ".hcl":
		return "hcl"
	case ".py":
		return "python"
	case ".sh":
		return "sh"
	}
	return ""
}

const sigPrefix = "<!-- docsp:sig "

// Signature identifies the findings (claim, verdict), not the commit: a new commit with the
// same findings leaves the posted comment alone. An incomplete run and a language other
// than the default are part of it (a complete zh run signs as before).
func (r *Report) Signature() string {
	keys := make([]string, 0, len(r.Problems)+len(r.Incomplete)+1)
	for _, p := range r.Problems {
		keys = append(keys, p.ID+" "+p.Category)
	}
	for _, x := range r.Incomplete {
		keys = append(keys, "incomplete "+x)
	}
	if r.Lang != "" && r.Lang != prtext.ZH {
		keys = append(keys, "lang "+string(r.Lang))
	}
	b, _ := json.Marshal(keys)
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}

// Unchanged says whether a posted comment already says what this report would.
func (r *Report) Unchanged(body string) bool {
	if len(r.Problems) == 0 && len(r.Incomplete) == 0 {
		return strings.Contains(body, noProblemsText(r.Lang))
	}
	return strings.Contains(body, sigPrefix+r.Signature()+" -->")
}

func noProblemsText(l prtext.Lang) string {
	return l.T("这个 PR 目前没有让文档和代码不一致。", "This PR currently makes no documentation line inconsistent with the code.", "この PR で現在コードと合わないドキュメントはありません。")
}

// NoProblemsComment replaces a posted comment once the change introduces no problem any more.
func (r *Report) NoProblemsComment() string { return Marker + "\n" + noProblemsText(r.Lang) + "\n" }

// NoProblemsComment is the zh replacement (kept for callers of the first version).
func NoProblemsComment() string { return (&Report{}).NoProblemsComment() }
