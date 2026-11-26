package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MIERAI/docs-platform/internal/repo"
)

// Limits of the judge-tools/v1 1.1 search tools.
const (
	DefaultSearchResults = 40
	maxSearchResults     = 200
	maxGlobResults       = 200
	maxPatternBytes      = 500
	// MinQuoteChars is the shortest quote a fact's evidence may use (after whitespace
	// normalisation).
	MinQuoteChars = 6
	// docSelfRadius: a quote from the documentation line under test, or within this many lines
	// of it, is the statement itself, not evidence.
	docSelfRadius = 3
)

// Doc is the documentation under test of one agentic request: its path and the line(s) under
// test. Quotes from those lines (±3) are not evidence.
type Doc struct {
	Path  string
	Lines []int
}

// SetDoc records the documentation under test (check_quote and quote verification use it).
func (t *Tools) SetDoc(d Doc) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.doc = d
}

func (t *Tools) docRef() Doc {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.doc
}

// TrackedFiles lists the files git tracks in the worktree (falling back to a walk of the tree
// when it is not a git checkout), sorted, without anything under a .git directory.
func (t *Tools) TrackedFiles(ctx context.Context) []string {
	t.filesOnce.Do(func() {
		out, err := repo.Git(ctx, t.dir, "ls-files", "-z", "--cached", "--", ".")
		if err == nil {
			for _, f := range strings.Split(out, "\x00") {
				if f != "" && !hasGitSegment(f) {
					t.files = append(t.files, f)
				}
			}
		} else {
			_ = fs.WalkDir(t.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
				switch {
				case err != nil:
					return nil //nolint:nilerr // unreadable entries are skipped
				case d.IsDir() && d.Name() == ".git":
					return fs.SkipDir
				case d.Type().IsRegular():
					t.files = append(t.files, p)
				}
				return nil
			})
		}
		sort.Strings(t.files)
	})
	return t.files
}

func hasGitSegment(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == ".git" {
			return true
		}
	}
	return false
}

// Exists reports whether the repository-relative path names a file or a non-empty directory
// in the worktree (never following a symlink out of it). An empty directory does not count:
// git does not track one, and a fresh checkout would not have it.
func (t *Tools) Exists(p string) bool {
	c, err := CleanPath(p)
	if err != nil || c == "." {
		return c == "."
	}
	st, err := fs.Stat(t.root.FS(), c)
	if err != nil {
		return false
	}
	if !st.IsDir() {
		return true
	}
	ents, err := fs.ReadDir(t.root.FS(), c)
	return err == nil && len(ents) > 0
}

// SearchQuery is one search: a case-insensitive fixed string unless Regex (RE2, case
// sensitive), over the tracked files the scope glob selects.
type SearchQuery struct {
	Pattern string
	Scope   string
	Regex   bool
	Max     int
	// SkipFile excludes one file (the document under test, for absence evidence).
	SkipFile string
	// All searches private paths too (the harness re-checking absence; the result never
	// reaches the provider).
	All bool
	// PerFile caps the matches of one file (0 = no cap), so one large file cannot crowd out
	// the rest.
	PerFile int
}

type searchResult struct {
	Matches       []grepMatch `json:"matches"`
	FilesSearched int         `json:"files_searched"`
}

func (r *searchResult) shrink() bool {
	if len(r.Matches) == 0 {
		return false
	}
	r.Matches = r.Matches[:len(r.Matches)/2]
	return true
}

// Search runs q. The same function serves the search tool and the re-run of absence
// evidence, so an absence is accepted only if exactly the search the model ran finds nothing.
func (t *Tools) Search(ctx context.Context, q SearchQuery) (*searchResult, error) { //nolint:revive // callers read Matches only; the type stays internal
	match, err := lineMatcher(q.Pattern, q.Regex)
	if err != nil {
		return nil, err
	}
	scope, err := CompileScope(q.Scope)
	if err != nil {
		return nil, err
	}
	if q.Max <= 0 {
		q.Max = DefaultSearchResults
	}
	q.Max = min(q.Max, maxSearchResults)
	res := &searchResult{Matches: []grepMatch{}}
	scanned := 0
	for _, f := range t.TrackedFiles(ctx) {
		if ctx.Err() != nil || len(res.Matches) >= q.Max || scanned > maxGrepScanBytes {
			break
		}
		if f != q.SkipFile && scope(f) && (q.All || t.allow(f)) {
			scanned += t.searchFile(f, match, q, res)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return res, nil
}

// searchFile adds the matches of one file to res and returns the bytes it scanned.
func (t *Tools) searchFile(f string, match func(string) bool, q SearchQuery, res *searchResult) int {
	lines, err := t.fileLines(f)
	if err != nil {
		return 0 // binary, too large or unreadable
	}
	res.FilesSearched++
	scanned, inFile := 0, 0
	for i, l := range lines {
		scanned += len(l) + 1
		if len(res.Matches) >= q.Max || q.PerFile > 0 && inFile >= q.PerFile {
			break
		}
		if match(l) {
			inFile++
			res.Matches = append(res.Matches, grepMatch{Path: f, Line: i + 1, Text: cutStr(l, maxLineChars)})
		}
	}
	return scanned
}

func lineMatcher(pattern string, regex bool) (func(string) bool, error) {
	if strings.TrimSpace(pattern) == "" || len(pattern) > maxPatternBytes || !utf8.ValidString(pattern) {
		return nil, fmt.Errorf("search: pattern is required (at most %d bytes)", maxPatternBytes)
	}
	if regex {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		return re.MatchString, nil
	}
	low := strings.ToLower(pattern)
	return func(l string) bool { return strings.Contains(strings.ToLower(l), low) }, nil
}

// CompileScope turns a scope glob into a path predicate. The matching is deliberately broad
// (an absence is then harder to claim): "", ".", "*", "**", "**/*" select every file; a
// plain path selects that file or everything below that directory; otherwise "*" matches
// any characters including "/", "?" one character, "[...]" a class, and the glob is tried
// against the full path, against the base name (when it has no "/"), against the rest after
// a leading "**/", and its literal directory part (before the first wildcard) as a prefix.
func CompileScope(scope string) (func(string) bool, error) {
	g := strings.TrimPrefix(strings.TrimSpace(scope), "./")
	switch g {
	case "", ".", "*", "**", "**/*", "/":
		return func(string) bool { return true }, nil
	}
	if !strings.ContainsAny(g, "*?[") {
		d := strings.TrimSuffix(g, "/")
		return func(p string) bool { return p == d || strings.HasPrefix(p, d+"/") }, nil
	}
	pats := []string{g}
	if rest, ok := strings.CutPrefix(g, "**/"); ok {
		pats = append(pats, rest)
	}
	var res []*regexp.Regexp
	for _, p := range pats {
		re, err := globRegexp(p)
		if err != nil {
			return nil, err
		}
		res = append(res, re)
	}
	base := !strings.Contains(g, "/")
	dir := strings.TrimSuffix(strings.TrimRight(g, "*"), "/")
	if strings.ContainsAny(dir, "*?[") {
		dir = ""
	}
	return func(p string) bool {
		for _, re := range res {
			if re.MatchString(p) || base && re.MatchString(path.Base(p)) {
				return true
			}
		}
		return dir != "" && strings.HasPrefix(p, dir+"/")
	}, nil
}

func globRegexp(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := strings.IndexByte(g[i:], ']')
			if j < 2 {
				b.WriteString(`\[`)
				continue
			}
			cls := g[i+1 : i+j]
			if strings.HasPrefix(cls, "!") {
				cls = "^" + cls[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(cls, `\`, `\\`) + "]")
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("scope %q: %w", g, err)
	}
	return re, nil
}

type globResult struct {
	Paths []string `json:"paths"`
}

func (r *globResult) shrink() bool {
	if len(r.Paths) == 0 {
		return false
	}
	r.Paths = r.Paths[:len(r.Paths)/2]
	return true
}

type textResult struct {
	Text string `json:"text"`
}

func (r *textResult) shrink() bool {
	if len(r.Text) < 200 {
		return false
	}
	r.Text = strings.ToValidUTF8(r.Text[:len(r.Text)/2], "") + "\n… (truncated)"
	return true
}

// moreTools are the judge-tools/v1 1.1 tools.
var moreTools = map[string]func(context.Context, *Tools, json.RawMessage) (any, error){
	"search": func(ctx context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Pattern    string `json:"pattern"`
			Scope      string `json:"scope"`
			Regex      bool   `json:"regex"`
			MaxResults int    `json:"max_results"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.Search(ctx, SearchQuery{Pattern: a.Pattern, Scope: a.Scope, Regex: a.Regex, Max: a.MaxResults})
	},
	"glob": func(ctx context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Pattern    string `json:"pattern"`
			MaxResults int    `json:"max_results"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.glob(ctx, a.Pattern, a.MaxResults)
	},
	"git_changes": func(ctx context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Kind     string `json:"kind"`
			Path     string `json:"path"`
			MaxCount int    `json:"max_count"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.gitChanges(ctx, a.Kind, a.Path, a.MaxCount)
	},
	"changes_since": func(ctx context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.changesSince(ctx, a.Path, a.Line)
	},
	"find_references": func(ctx context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Name string `json:"name"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.findReferences(ctx, a.Name)
	},
	"check_quote": func(_ context.Context, t *Tools, raw json.RawMessage) (any, error) {
		var a struct {
			Path string `json:"path"`
			Line int    `json:"line"`
			Text string `json:"text"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return &textResult{Text: t.checkQuote(a.Path, a.Line, a.Text)}, nil
	},
}

func (t *Tools) glob(ctx context.Context, pattern string, n int) (any, error) {
	if strings.TrimSpace(pattern) == "" || len(pattern) > maxPatternBytes {
		return nil, errors.New("glob: pattern is required")
	}
	m, err := CompileScope(pattern)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > maxGlobResults {
		n = maxGlobResults
	}
	res := &globResult{Paths: []string{}}
	for _, f := range t.TrackedFiles(ctx) {
		if len(res.Paths) == n {
			break
		}
		if m(f) && t.allow(f) {
			res.Paths = append(res.Paths, f)
		}
	}
	return res, nil
}

// QuoteVerdict is the outcome of verifying one quoted line.
type QuoteVerdict struct {
	OK     bool
	Reason string
}

// VerifyNear checks a fact's quote: after whitespace normalisation (and optionally without
// leading/trailing Markdown or comment markers) it must occur within path:line±2 of the
// worktree file, be at least MinQuoteChars long, and not come from the documentation under
// test (its line ±3). Paths the provider may not see are refused.
func (t *Tools) VerifyNear(p string, line int, quote string) QuoteVerdict {
	q := strings.Join(strings.Fields(quote), " ")
	if utf8.RuneCountInString(q) < MinQuoteChars {
		return QuoteVerdict{Reason: "quote too short"}
	}
	c, err := t.checked(p)
	if err != nil {
		return QuoteVerdict{Reason: err.Error()}
	}
	if d := t.docRef(); c == d.Path {
		for _, l := range d.Lines {
			if abs(line-l) <= docSelfRadius {
				return QuoteVerdict{Reason: "that is the statement under test itself, not evidence"}
			}
		}
	}
	lines, err := t.fileLines(c)
	if err != nil {
		return QuoteVerdict{Reason: err.Error()}
	}
	lo, hi := max(1, line-verifySlack), min(len(lines), line+verifySlack)
	if lo > hi {
		return QuoteVerdict{Reason: fmt.Sprintf("%s has %d lines", c, len(lines))}
	}
	parts := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		parts = append(parts, strings.Join(strings.Fields(lines[i-1]), " "))
	}
	win := strings.Join(parts, " ")
	if strings.Contains(win, q) {
		return QuoteVerdict{OK: true}
	}
	if s := strings.Trim(q, "`#/-* "); s != "" && strings.Contains(win, s) {
		return QuoteVerdict{OK: true}
	}
	return QuoteVerdict{Reason: fmt.Sprintf("quote not found at %s:%d±%d", c, line, verifySlack)}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// checkQuote is the check_quote tool: VerifyNear with hints where the text does occur.
func (t *Tools) checkQuote(p string, line int, text string) string {
	v := t.VerifyNear(p, line, text)
	if v.OK {
		return fmt.Sprintf("OK: quote found at %s:%d±%d", p, line, verifySlack)
	}
	c, err := t.checked(p)
	if err != nil {
		return "NOT ACCEPTED: " + err.Error()
	}
	lines, err := t.fileLines(c)
	if err != nil {
		return "NOT FOUND: no such file " + c
	}
	if strings.Contains(v.Reason, "statement under test") || strings.Contains(v.Reason, "too short") {
		return "NOT ACCEPTED: " + v.Reason
	}
	q := strings.Join(strings.Fields(text), " ")
	q = string([]rune(q)[:min(40, utf8.RuneCountInString(q))])
	var near []string
	for i, l := range lines {
		if q != "" && strings.Contains(strings.Join(strings.Fields(l), " "), q) && len(near) < 5 {
			near = append(near, strconv.Itoa(i+1))
		}
	}
	if len(near) > 0 {
		return "NOT FOUND at that line; the text occurs at line(s) " + strings.Join(near, ", ") + " - use one of those"
	}
	return "NOT FOUND at that line; copy the text exactly from a read_file result"
}
