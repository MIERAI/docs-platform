// Package prreview turns a pull request's recheck into a comment (docs-platform/pr-review/v1).
// The ledger of the default branch is copied, the claims the change made stale are judged
// again at the PR's head (docsp stale, schedule, recheck on the copy), and the two ledgers are
// compared: a claim is reported when it was judged at the head and is now a problem while it
// was not one before - the problems this change introduces, not the backlog.
package prreview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// SchemaVersion of docs-platform/pr-review/v1.
const SchemaVersion = "1.0"

// Marker identifies the review's PR comment (updated in place, never duplicated).
const Marker = "<!-- docsp:pr -->"

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
		if !agentic.IsProblem(c.Category) || agentic.IsProblem(was) {
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
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return rep
}

var categoryLabel = map[string]string{
	"contradicted":       "和代码矛盾",
	"outdated_wording":   "写法已过时",
	"incomplete_list":    "列举不完整",
	"dangling_reference": "引用的东西不存在",
}

// Markdown renders the comment ("" when the change introduces no problem), keeping the boxes
// people ticked in the previous version.
func (r *Report) Markdown(ticked map[string]bool) string {
	if len(r.Problems) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s%s -->\n**docs-platform**:这个 PR 改了代码,让 **%d 处文档**和代码不一致了。\n", Marker, sigPrefix, r.Signature(), len(r.Problems))
	for _, p := range r.Problems {
		r.writeProblem(&b, p, ticked)
	}
	b.WriteString("\n<sub>📖 = AI 读代码的判断,可能是误报。勾选只用来统计这个检查的误报率。平台不会改任何文件。</sub>\n")
	return b.String()
}

func (r *Report) writeProblem(b *strings.Builder, p Problem, ticked map[string]bool) {
	fmt.Fprintf(b, "\n#### %s · %s · 📖\n\n", r.lineLink(p.Path, p.Line), categoryLabel[p.Category])
	for _, f := range p.Facts {
		if f.Status != "confirmed" && f.Valid {
			fmt.Fprintf(b, "%s\n\n", f.Fact)
			break
		}
	}
	b.WriteString("**文档该改,还是代码改错了?**\n\n")
	opts := [][2]string{{factorypr.OptionDesignStale, "文档过时了:我会改文档"}, {factorypr.OptionCodeWrong, "代码改错了:我会修代码"}, {factorypr.OptionNotAProblem, "都不是:这里没有不一致(误报)"}}
	for _, o := range opts {
		f := factorypr.Feedback{Rule: fmt.Sprintf("%s:%d", p.Path, p.Line), Option: o[0], Category: p.Category, Evidence: factorypr.EvidenceRead}
		box := " "
		if ticked[f.Key()] {
			box = "x"
		}
		fmt.Fprintf(b, "- [%s] %s <!-- docsp:fb %s %s %s %s -->\n", box, o[1], f.Rule, f.Option, f.Category, f.Evidence)
	}
	b.WriteString("\n<details><summary>证据</summary>\n\n")
	fmt.Fprintf(b, "文档 %s\n\n```%s\n%s\n```\n\n", r.lineLink(p.Path, p.Line), fence(p.Path), strings.TrimSpace(p.Text))
	for _, c := range p.Code {
		fmt.Fprintf(b, "代码 %s\n\n```%s\n%s\n```\n\n", r.lineLink(c.Path, c.Line), fence(c.Path), strings.TrimRight(c.Text, "\n"))
	}
	for _, f := range p.Facts {
		if f.Valid {
			fmt.Fprintf(b, "- %s — %s\n", f.Fact, f.Status)
		}
	}
	b.WriteString("\n</details>\n")
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
// same findings leaves the posted comment alone.
func (r *Report) Signature() string {
	keys := make([]string, 0, len(r.Problems))
	for _, p := range r.Problems {
		keys = append(keys, p.ID+" "+p.Category)
	}
	b, _ := json.Marshal(keys)
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}

// Unchanged says whether a posted comment already says what this report would.
func (r *Report) Unchanged(body string) bool {
	if len(r.Problems) == 0 {
		return strings.Contains(body, noProblemsText)
	}
	return strings.Contains(body, sigPrefix+r.Signature()+" -->")
}

const noProblemsText = "这个 PR 目前没有让文档和代码不一致。"

// NoProblemsComment replaces a posted comment once the change introduces no problem any more.
func NoProblemsComment() string { return Marker + "\n" + noProblemsText + "\n" }
