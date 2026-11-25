package judge

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/repo"
)

const (
	defaultChanges = 40
	maxChanges     = 150
	maxTokens      = 25
	maxSincePaths  = 15
	maxDiffChars   = 700
	refsPerFile    = 10
)

type change struct {
	SHA     string `json:"sha"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Path    string `json:"path"`
	NewPath string `json:"new_path,omitempty"`
}

type changesResult struct {
	Changes []change `json:"changes"`
}

func (r *changesResult) shrink() bool {
	if len(r.Changes) == 0 {
		return false
	}
	r.Changes = r.Changes[:len(r.Changes)/2]
	return true
}

// gitChanges lists paths added (A), deleted (D), renamed (R) or modified (M) in the history
// of HEAD, newest first, with the commit that did it. Private paths are left out.
func (t *Tools) gitChanges(ctx context.Context, kind, p string, n int) (any, error) {
	if n <= 0 || n > maxChanges {
		n = defaultChanges
	}
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	out, err := repo.Git(ctx, t.dir, "log", "--date=short", "--format=@@%h%x09%ad%x09%s", "-M", "--diff-filter="+changeKinds(kind),
		"--name-status", "HEAD", "--", c)
	if err != nil {
		return nil, fmt.Errorf("git_changes: %w", err)
	}
	res := &changesResult{Changes: []change{}}
	for _, ch := range parseChanges(out) {
		if len(res.Changes) == n {
			break
		}
		if t.allow(ch.Path) && (ch.NewPath == "" || t.allow(ch.NewPath)) {
			res.Changes = append(res.Changes, ch)
		}
	}
	return res, nil
}

// changeKinds keeps the letters A, D, R, M of kind (default DR).
func changeKinds(kind string) string {
	k := ""
	for _, r := range strings.ToUpper(kind) {
		if strings.ContainsRune("ADRM", r) && !strings.ContainsRune(k, r) {
			k += string(r)
		}
	}
	if k == "" {
		return "DR"
	}
	return k
}

// parseChanges reads `git log --format=@@%h\t%ad\t%s --name-status` output.
func parseChanges(out string) []change {
	var res []change
	var cur change
	for _, l := range strings.Split(out, "\n") {
		if h, ok := strings.CutPrefix(l, "@@"); ok {
			if f := strings.SplitN(h, "\t", 3); len(f) == 3 {
				cur = change{SHA: f[0], Date: f[1], Subject: cutStr(f[2], 200)}
			}
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 2 || cur.SHA == "" {
			continue
		}
		ch := cur
		ch.Status, ch.Path = f[0], f[1]
		if len(f) > 2 {
			ch.NewPath = f[2]
		}
		res = append(res, ch)
	}
	return res
}

var (
	reTokBacktick = regexp.MustCompile("`([^`]{2,80})`")
	reTokPath     = regexp.MustCompile(`(?:^|[^\w/.-])((?:\.{0,2}/)?[\w.-]+(?:/[\w.<>*-]+)+/?|[\w-]+\.(?:go|py|sh|ya?ml|json|tf|md|sql|js|toml|proto))`)
	reTokEnv      = regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]{2,}\b`)
	reTokFlag     = regexp.MustCompile(`(?:^|[^\w])(--[a-z][\w-]+)`)
	reTokMake     = regexp.MustCompile(`\bmake ([\w-]+)`)
	reTokCamel    = regexp.MustCompile(`\b[a-z]+(?:[A-Z][a-z0-9]+)+\b|\b[A-Z][a-z0-9]+(?:[A-Z][a-z0-9]+)+\b`)
	reTokSnake    = regexp.MustCompile(`\b[a-z][a-z0-9]+(?:_[a-z0-9]+)+\b`)
	reLooksPath   = regexp.MustCompile(`\.\w{1,5}$`)
	reAngle       = regexp.MustCompile(`<[^>]+>`)
)

// lineTokens are the identifiers, paths, environment variables, flags and make targets a
// documentation line names.
func lineTokens(text string) []string {
	set := map[string]bool{}
	add := func(s string) {
		s = strings.Trim(s, ` .,;:()"'`)
		if len(s) >= 3 && !strings.HasPrefix(s, "http") {
			set[s] = true
		}
	}
	for _, m := range reTokBacktick.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, re := range []*regexp.Regexp{reTokPath, reTokFlag, reTokMake} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
	}
	for _, re := range []*regexp.Regexp{reTokEnv, reTokCamel, reTokSnake} {
		for _, m := range re.FindAllString(text, -1) {
			add(m)
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out[:min(len(out), maxTokens)]
}

// resolve returns the tracked files a token refers to: an existing path, a directory prefix,
// a path suffix or a base name (isPath true); otherwise files containing the token.
func (t *Tools) resolve(ctx context.Context, tok string) ([]string, bool) {
	if hits := t.pathHits(ctx, tok); len(hits) > 0 {
		return hits, true
	}
	res, err := t.Search(ctx, SearchQuery{Pattern: tok, Max: 60, PerFile: 1})
	if err != nil {
		return nil, false
	}
	var code, docs []string
	for _, m := range res.Matches { // one match per file
		if fileKind(m.Path) == "doc" {
			docs = append(docs, m.Path)
		} else {
			code = append(code, m.Path)
		}
	}
	code = append(code, docs...)
	return code[:min(len(code), 8)], false
}

// pathHits are the tracked files a path-like token names: the path or directory itself, a
// path suffix, or (a file name) the same base name.
func (t *Tools) pathHits(ctx context.Context, tok string) []string {
	s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(tok, "./"), "/"), "/")
	s = reAngle.ReplaceAllString(s, "*")
	scope, err := CompileScope(s)
	if s == "" || err != nil {
		return nil
	}
	files := t.TrackedFiles(ctx)
	var hits []string
	pick := func(ok func(string) bool) {
		for _, f := range files {
			if len(hits) < 12 && t.allow(f) && ok(f) {
				hits = append(hits, f)
			}
		}
	}
	if strings.ContainsAny(s, "/*?") {
		pick(scope)
	} else {
		pick(func(f string) bool { return f == s || strings.HasPrefix(f, s+"/") })
	}
	if len(hits) == 0 && strings.Contains(s, "/") {
		pick(func(f string) bool { return strings.HasSuffix(f, "/"+s) || strings.Contains("/"+f, "/"+s+"/") })
	}
	if len(hits) == 0 && strings.Contains(path.Base(s), ".") {
		pick(func(f string) bool { return path.Base(f) == path.Base(s) })
	}
	return hits
}

// changesSince is the changes_since tool: what changed in the code a documentation line refers
// to since the line was last written.
func (t *Tools) changesSince(ctx context.Context, p string, line int) (any, error) {
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	if line < 1 {
		return nil, errors.New("changes_since: line must be ≥ 1")
	}
	bl, err := repo.Git(ctx, t.dir, "blame", "--porcelain", "-L", fmt.Sprintf("%d,%d", line, line), "HEAD", "--", c)
	if err != nil || len(strings.Fields(bl)) == 0 {
		return nil, fmt.Errorf("changes_since: cannot blame %s:%d", c, line)
	}
	sha := strings.Fields(bl)[0]
	if !repo.ValidRef(sha) {
		return nil, errors.New("changes_since: unexpected blame output")
	}
	meta, _ := repo.Git(ctx, t.dir, "show", "-s", "--format=%h %ad %s", "--date=short", sha)
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d was last written by commit %s\n", c, line, strings.TrimSpace(meta))
	text := ""
	if ls, err := t.fileLines(c); err == nil && line <= len(ls) {
		text = ls[line-1]
	}
	paths := t.sinceTokens(ctx, &b, c, text)
	t.sinceCommits(ctx, &b, sha, c, paths)
	return &textResult{Text: b.String()}, nil
}

// sinceTokens writes the paths a line names that are gone, and returns the files it refers to.
func (t *Tools) sinceTokens(ctx context.Context, b *strings.Builder, doc, text string) []string {
	set := map[string]bool{}
	var notes []string
	for _, tok := range lineTokens(text) {
		fs, isPath := t.resolve(ctx, tok)
		looksPath := strings.Contains(tok, "/") || reLooksPath.MatchString(tok)
		if looksPath && !isPath {
			s := strings.TrimSuffix(strings.TrimPrefix(tok, "./"), "/")
			note := fmt.Sprintf("- named path `%s` does not exist at HEAD", tok)
			if c, err := CleanPath(s); err == nil && c != "." {
				if gone, err := repo.Git(ctx, t.dir, "log", "--format=%h %ad %s", "--date=short", "-n3", "--diff-filter=DR", "HEAD", "--", c, c+"/*"); err == nil && strings.TrimSpace(gone) != "" {
					note += "; deleted/renamed in: " + strings.ReplaceAll(strings.TrimSpace(gone), "\n", "; ")
				}
			}
			if len(fs) > 0 {
				note += "; files containing that name: " + strings.Join(fs[:min(4, len(fs))], ", ")
			}
			notes = append(notes, note)
			continue
		}
		for _, f := range fs {
			if f != doc {
				set[f] = true
			}
		}
	}
	if len(notes) > 0 {
		b.WriteString("Paths named in the line:\n" + strings.Join(notes, "\n") + "\n")
	}
	paths := make([]string, 0, len(set))
	for f := range set {
		paths = append(paths, f)
	}
	sort.Strings(paths)
	paths = paths[:min(len(paths), maxSincePaths)]
	fmt.Fprintf(b, "Files the line refers to (%d): %s\n", len(paths), orNone(strings.Join(paths, ", "), "none resolved"))
	return paths
}

func (t *Tools) sinceCommits(ctx context.Context, b *strings.Builder, sha, doc string, paths []string) {
	short := sha[:min(7, len(sha))]
	if len(paths) > 0 {
		args := append([]string{"log", "--format=%h %ad %s", "--date=short", "-n12", sha + "..HEAD", "--"}, paths...)
		log, _ := repo.Git(ctx, t.dir, args...)
		if strings.TrimSpace(log) == "" {
			fmt.Fprintf(b, "No commits touched those files after %s.\n", short)
		} else {
			fmt.Fprintf(b, "Commits after %s touching them (newest first):\n", short)
			for k, l := range strings.Split(strings.TrimSpace(log), "\n") {
				h := strings.Fields(l)[0]
				if !repo.ValidRef(h) {
					continue
				}
				fmt.Fprintf(b, "* %s\n", l)
				if k < 3 {
					d, _ := repo.Git(ctx, t.dir, append([]string{"show", "--format=", "--unified=0", "--no-color", h, "--"}, paths...)...)
					b.WriteString(diffLines(d) + "\n")
				}
			}
		}
	}
	dl, _ := repo.Git(ctx, t.dir, "log", "--format=%h %ad %s", "--date=short", "-n5", sha+"..HEAD", "--", doc)
	if s := strings.TrimSpace(dl); s != "" {
		fmt.Fprintf(b, "Later commits to %s (that did not rewrite this line):\n%s\n", doc, s)
	}
}

func diffLines(d string) string {
	var out []string
	n := 0
	for _, l := range strings.Split(d, "\n") {
		if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "---") {
			out = append(out, l)
			n += len(l) + 1
			if n > maxDiffChars {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func orNone(s, none string) string {
	if s == "" {
		return none
	}
	return s
}

// fileKind sorts files into doc, code, config and other.
func fileKind(f string) string {
	b := path.Base(f)
	switch path.Ext(f) {
	case ".md", ".txt", ".rst", ".markdown":
		return "doc"
	case ".go", ".py", ".sh", ".js", ".ts", ".sql", ".proto", ".tf", ".hcl", ".rs", ".java", ".kt":
		return "code"
	case ".yaml", ".yml", ".json", ".toml", ".tfvars", ".ini", ".conf", ".cfg", ".env":
		return "config"
	}
	if b == "Makefile" || b == "Dockerfile" || strings.HasPrefix(b, ".") {
		return "config"
	}
	return "other"
}

var defPatterns = []string{
	`^\s*func\s+(\([^)]*\)\s*)?%s\b`, `^\s*type\s+%s\b`, `^\s*(const|var)\s+%s\b`, `^\s*%s\s*(=|:=)`,
	`^\s*%s\s*:`, `^\s*-?\s*name:\s*["']?%s["']?\s*$`, `^\s*def\s+%s\b`, `^\s*class\s+%s\b`,
	`^%s\s*[:?]?=`, `^export\s+%s=`, `^\s*(resource|module|variable|output|data)\s+"[^"]*"?\s*"?%s"`,
	`^\s*%s\(\)\s*\{`, `(String|Int|Bool|Duration|Float64|Var)\(\s*"%s"`,
}

// findReferences is the find_references tool: definitions first, then uses by file kind.
func (t *Tools) findReferences(ctx context.Context, name string) (any, error) {
	name = strings.Trim(strings.TrimSpace(name), "`")
	base := strings.TrimLeft(name, "-")
	if len(base) < 2 || len(base) > maxPatternBytes {
		return nil, errors.New("find_references: name too short or too long")
	}
	res, err := t.Search(ctx, SearchQuery{Pattern: regexp.QuoteMeta(base), Regex: true, Max: maxSearchResults, PerFile: refsPerFile})
	if err != nil {
		return nil, err
	}
	defLines, uses, nfiles := classifyRefs(base, res.Matches)
	var b strings.Builder
	fmt.Fprintf(&b, "references of `%s`: %d lines in %d files (at most %d per file)\n", name, len(res.Matches), nfiles, refsPerFile)
	if named := t.namedPaths(ctx, base); len(named) > 0 {
		b.WriteString("files/dirs with that path: " + strings.Join(named, ", ") + "\n")
	}
	if len(defLines) == 0 {
		b.WriteString("DEFINITIONS: none found by pattern\n")
	} else {
		b.WriteString("DEFINITIONS:\n" + strings.Join(defLines[:min(12, len(defLines))], "\n") + "\n")
	}
	for _, k := range []string{"code", "config", "other", "doc"} {
		if u := uses[k]; len(u) > 0 {
			lim := 10
			if k == "doc" {
				lim = 6
			}
			fmt.Fprintf(&b, "USES in %s (%d):\n%s\n", k, len(u), strings.Join(u[:min(lim, len(u))], "\n"))
		}
	}
	return &textResult{Text: b.String()}, nil
}

// classifyRefs splits matches into definitions (by pattern, outside documentation) and uses
// by file kind.
func classifyRefs(base string, ms []grepMatch) ([]string, map[string][]string, int) {
	defs := make([]*regexp.Regexp, 0, len(defPatterns))
	for _, p := range defPatterns {
		defs = append(defs, regexp.MustCompile(fmt.Sprintf(p, regexp.QuoteMeta(base))))
	}
	var defLines []string
	uses := map[string][]string{}
	files := map[string]bool{}
	for _, m := range ms {
		files[m.Path] = true
		item := fmt.Sprintf("%s:%d: %s", m.Path, m.Line, cutStr(strings.TrimSpace(m.Text), 160))
		k := fileKind(m.Path)
		if k != "doc" && matchesAny(defs, m.Text) {
			defLines = append(defLines, item)
			continue
		}
		uses[k] = append(uses[k], item)
	}
	return defLines, uses, len(files)
}

// namedPaths are tracked files whose path is, or is below, or has the base name of name.
func (t *Tools) namedPaths(ctx context.Context, name string) []string {
	d := strings.Trim(name, "/")
	var named []string
	for _, f := range t.TrackedFiles(ctx) {
		if len(named) < 10 && t.allow(f) && (f == d || strings.HasPrefix(f, d+"/") || path.Base(f) == name) {
			named = append(named, f)
		}
	}
	return named
}

func matchesAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
