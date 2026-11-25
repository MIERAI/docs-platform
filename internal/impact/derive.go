package impact

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Section statuses.
const (
	// SectionReread: code the section mentions changed and the section did not; a human (or
	// the judge) should re-read it.
	SectionReread = "reread"
	// SectionUpdated: the section itself changed in the same change.
	SectionUpdated = "updated"
)

// Reason kinds.
const (
	ReasonPath       = "path"       // the section mentions a changed file or directory
	ReasonIdentifier = "identifier" // the section mentions an identifier on a changed line
	ReasonRule       = "rule"       // an explicit .docsp.yaml impact rule maps the code to this doc
)

// Section is one unit of documentation to re-read: a Markdown section (from a heading to the
// next heading of any level) or a contiguous block of code comments.
type Section struct {
	Path    string `json:"path"`
	Heading string `json:"heading"`
	From    int    `json:"from"`
	To      int    `json:"to"`
	refs    []model.Reference
}

// Contains reports whether line n of the file is inside the section.
func (s Section) Contains(n int) bool { return n >= s.From && n <= s.To }

// Reason explains why a section is affected.
type Reason struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`           // the mentioned path or identifier (as written in the doc)
	Changed string `json:"changed"`           // the changed file, or file:line of the changed line
	DocLine int    `json:"doc_line,omitzero"` // where the section mentions it
}

// SectionResult is one affected section.
type SectionResult struct {
	Section
	Status  string   `json:"status"`
	Reasons []Reason `json:"reasons"`
	// Judgement and Draft are filled only when a model is configured (proposals).
	Judgement *Judgement `json:"judgement,omitempty"`
	Draft     string     `json:"draft,omitempty"`
}

// Judgement is the judge's answer for one section.
type Judgement struct {
	Verdict       string `json:"verdict"`
	Quote         string `json:"quote,omitempty"`
	QuoteRejected bool   `json:"quote_rejected,omitempty"`
	Model         string `json:"model"`
	PromptHash    string `json:"prompt_hash"`
	Error         string `json:"error,omitempty"`
}

var reHeading = regexp.MustCompile(`^ {0,3}(#{1,6})\s+(.*?)\s*#*\s*$`)

// MarkdownSections splits a Markdown file into sections at every ATX heading outside code
// fences. Text before the first heading is a section with an empty heading.
func MarkdownSections(p string, lines []string) []Section {
	var out []Section
	cur := Section{Path: p, From: 1}
	fence := ""
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if f := fenceMarker(t); f != "" {
			switch {
			case fence == "":
				fence = f
			case strings.HasPrefix(t, fence):
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if m := reHeading.FindStringSubmatch(l); m != nil {
			if i > 0 {
				cur.To = i
				out = append(out, cur)
			}
			cur = Section{Path: p, Heading: m[2], From: i + 1}
		}
	}
	cur.To = max(len(lines), cur.From)
	return append(out, cur)
}

func fenceMarker(t string) string {
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, f) {
			return f
		}
	}
	return ""
}

// CommentSections groups comment lines (line numbers, ascending) into blocks of consecutive
// lines; each block is a section headed by its first line.
func CommentSections(p string, lines []int, text map[int]string) []Section {
	var out []Section
	for _, n := range lines {
		if len(out) > 0 && out[len(out)-1].To == n-1 {
			out[len(out)-1].To = n
			continue
		}
		out = append(out, Section{Path: p, Heading: cut(strings.TrimSpace(text[n]), 80), From: n, To: n})
	}
	return out
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Attach assigns each reference to the section of its file that contains its line.
func Attach(secs []Section, refs []model.Reference) []Section {
	byPath := map[string][]int{}
	for i, s := range secs {
		byPath[s.Path] = append(byPath[s.Path], i)
	}
	for _, r := range refs {
		for _, i := range byPath[r.Path] {
			if secs[i].Contains(r.Line) {
				secs[i].refs = append(secs[i].refs, r)
				break
			}
		}
	}
	return secs
}

// identifierKinds are the reference kinds matched against changed lines.
var identifierKinds = map[model.Kind]bool{
	model.KindEnvVar: true, model.KindCLIFlag: true, model.KindCLICommand: true, model.KindConfigKey: true,
	model.KindMetric: true, model.KindMakeTarget: true, model.KindWorkflowInput: true, model.KindProgram: true,
	model.KindTerm: true,
}

// Derive returns the sections affected by a change: those that mention a changed file or
// directory, or an identifier that appears on a changed line of another file. A section
// whose own lines changed is "updated"; otherwise "reread". The result is deterministic.
func Derive(secs []Section, diffs []repo.FileDiff) []SectionResult {
	ch := indexChange(diffs)
	var out []SectionResult
	for _, s := range secs {
		reasons := sectionReasons(s, ch)
		if len(reasons) == 0 {
			continue
		}
		st := SectionReread
		if ch.touches(s) {
			st = SectionUpdated
		}
		out = append(out, SectionResult{Section: s, Status: st, Reasons: reasons})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].From < out[j].From
	})
	return out
}

type change struct {
	files []string
	// tokens maps an identifier token to the first changed line that has it ("file:line").
	tokens map[string]string
	// docLines are the new-file line numbers changed per file (to detect updated sections).
	lines map[string][]int
}

var reToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.:/-]*[A-Za-z0-9_]`)

func indexChange(diffs []repo.FileDiff) *change {
	ch := &change{tokens: map[string]string{}, lines: map[string][]int{}}
	for _, d := range diffs {
		name := d.Name()
		ch.files = append(ch.files, name)
		if d.OldPath != "" && d.OldPath != name {
			ch.files = append(ch.files, d.OldPath)
		}
		for _, l := range d.Added {
			ch.lines[name] = append(ch.lines[name], l.N)
			ch.addTokens(name, l)
		}
		for _, l := range d.Removed {
			ch.lines[name] = append(ch.lines[name], l.N)
			ch.addTokens(name, l)
		}
	}
	return ch
}

func (ch *change) addTokens(file string, l repo.DiffLine) {
	for _, t := range reToken.FindAllString(l.Text, -1) {
		for _, v := range tokenVariants(t) {
			if _, ok := ch.tokens[v]; !ok {
				ch.tokens[v] = fmt.Sprintf("%s:%d", file, l.N)
			}
		}
	}
}

// tokenVariants are the forms an identifier on a code line can take: the token itself and
// its parts split at '.', '/', ':' and '=' (flag.Int("port") → port; os.Getenv → Getenv).
func tokenVariants(t string) []string {
	out := []string{t}
	for _, p := range strings.FieldsFunc(t, func(r rune) bool { return r == '.' || r == '/' || r == ':' || r == '=' }) {
		if p != t {
			out = append(out, p)
		}
	}
	return out
}

func (ch *change) touches(s Section) bool {
	for _, n := range ch.lines[s.Path] {
		if s.Contains(n) {
			return true
		}
	}
	return false
}

func sectionReasons(s Section, ch *change) []Reason {
	var out []Reason
	seen := map[string]bool{}
	add := func(r Reason) {
		k := r.Kind + "\x00" + r.Subject
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	for _, r := range s.refs {
		switch {
		case r.Kind == model.KindFilePath || r.Kind == model.KindLink:
			if f := ch.pathMatch(s.Path, r.Text); f != "" {
				add(Reason{Kind: ReasonPath, Subject: r.Text, Changed: f, DocLine: r.Line})
			}
		case identifierKinds[r.Kind] && identLike(r.Kind, r.Text):
			if at, ok := ch.tokens[identKey(r.Text)]; ok && !strings.HasPrefix(at, s.Path+":") {
				add(Reason{Kind: ReasonIdentifier, Subject: r.Text, Changed: at, DocLine: r.Line})
			}
		}
	}
	return out
}

// identKey normalizes a mentioned identifier to the token looked up on changed lines.
func identKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "-$")
	s = strings.Trim(s, "{}`'\"")
	if i := strings.IndexAny(s, "= ["); i > 0 {
		s = s[:i]
	}
	return s
}

// identLike filters terms to identifier-shaped words (snake/camel/dotted, or with digits), so
// plain English words in code spans do not match every change.
func identLike(k model.Kind, s string) bool {
	key := identKey(s)
	if len(key) < 3 {
		return false
	}
	if k != model.KindTerm {
		return true
	}
	return len(key) >= 5 && shapedLikeIdent(key)
}

// shapedLikeIdent: has '_', '.', '-' or a digit, or is camelCase (an upper-case letter after
// the first character, and lower-case letters).
func shapedLikeIdent(key string) bool {
	hasLower, innerUpper := false, false
	for i, r := range key {
		switch {
		case r == '_' || r == '.' || r == '-' || r >= '0' && r <= '9':
			return true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z' && i > 0:
			innerUpper = true
		}
	}
	return hasLower && innerUpper
}

// pathMatch returns the changed file a path mention refers to, or "". A mention matches a
// changed file exactly, relative to the document's directory, as a path suffix, as a
// directory containing it, or by file name (only for names with an extension).
func (ch *change) pathMatch(doc, mention string) string {
	m := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(mention), "./"), "/")
	m, _, _ = strings.Cut(m, "#")
	if m == "" || m == "." || strings.ContainsAny(m, "*?<>{}$ ") {
		return ""
	}
	rel := path.Clean(path.Join(path.Dir(doc), m))
	base := path.Base(m)
	for _, f := range ch.files {
		switch {
		case f == doc:
		case f == m || f == rel,
			strings.HasSuffix(f, "/"+m),
			strings.HasPrefix(f, m+"/") || strings.HasPrefix(f, rel+"/") || strings.Contains(f, "/"+m+"/"),
			!strings.Contains(m, "/") && strings.Contains(base, ".") && path.Base(f) == base:
			return f
		}
	}
	return ""
}
