package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/judgev1"
)

// ToolsContract names the tool contract.
const ToolsContract = "judge-tools/v1"

// Default tool limits.
const (
	DefaultMaxSteps       = 20
	DefaultMaxResultBytes = 32 << 10
	DefaultMaxTotalBytes  = 512 << 10
	maxToolFileBytes      = 2 << 20
	maxGrepScanBytes      = 64 << 20
	maxListEntries        = 500
	maxReadLines          = 400
	maxLineChars          = 2000 // as long as a model's own file reader shows
	verifySlack           = 2
)

// AllTools lists the tools of judge-tools/v1 (1.1).
var AllTools = []string{
	judgev1.ToolListDir, judgev1.ToolReadFile, judgev1.ToolGrep, judgev1.ToolGitLog, judgev1.ToolGitShow,
	judgev1.ToolSearch, judgev1.ToolGlob, judgev1.ToolGitChanges, judgev1.ToolChangesSince,
	judgev1.ToolFindReferences, judgev1.ToolCheckQuote, judgev1.ToolOutline, judgev1.ToolConfigValue,
}

// Tools executes judge-tools/v1 calls for one agentic request: read-only, confined to one
// worktree (os.Root refuses absolute paths, ".." and symlinks out of the tree), size-capped,
// git limited to log/show of validated revisions, and — for a cloud provider — refusing
// paths the classification marks private. The provider never touches the filesystem.
type Tools struct {
	dir    string
	root   *os.Root
	allow  func(p string) bool
	limits judgev1.ToolLimits
	mu     sync.Mutex
	total  int
	steps  []judgev1.Step
	doc    Doc
	// files is the tracked-file list, computed once.
	filesOnce sync.Once
	files     []string
	// record, when set, receives every step as it is taken (an out-of-process tool server
	// writes its transcript this way).
	record func(judgev1.Step)
	policy *Policy
}

// NewTools opens dir read-only. allow (nil = everything) says which repository-relative paths
// may be shown to the provider.
func NewTools(dir string, allow func(string) bool, lim judgev1.ToolLimits) (*Tools, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("tools: open worktree: %w", err)
	}
	if lim.MaxSteps <= 0 {
		lim.MaxSteps = DefaultMaxSteps
	}
	if lim.MaxResultBytes <= 0 {
		lim.MaxResultBytes = DefaultMaxResultBytes
	}
	if lim.MaxTotalBytes <= 0 {
		lim.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if allow == nil {
		allow = func(string) bool { return true }
	}
	return &Tools{dir: dir, root: root, allow: allow, limits: lim}, nil
}

// Policy says which paths of one repository a provider may see: everything for a local
// provider, only paths classified public for a cloud provider. Unlike a bare allow function
// it can be handed to an out-of-process tool server.
type Policy struct {
	Repo  string                  `json:"repo"`
	Class instance.Classification `json:"classification"`
	Cloud bool                    `json:"cloud"`
}

// Allow reports whether the provider may see repository-relative path p.
func (p Policy) Allow(rel string) bool {
	return !p.Cloud || p.Class.ClassOf(p.Repo, rel) == instance.Public
}

// NewPolicyTools opens dir read-only for a provider governed by pol.
func NewPolicyTools(dir string, pol Policy, lim judgev1.ToolLimits) (*Tools, error) {
	t, err := NewTools(dir, pol.Allow, lim)
	if err != nil {
		return nil, err
	}
	t.policy = &pol
	return t, nil
}

// Dir is the worktree the tools serve.
func (t *Tools) Dir() string { return t.dir }

// Limits are the tool limits of this request.
func (t *Tools) Limits() judgev1.ToolLimits { return t.limits }

// Spec announces the tools in a request.
func (t *Tools) Spec() *judgev1.ToolSpec {
	return &judgev1.ToolSpec{Contract: ToolsContract, Allowed: AllTools, Limits: t.limits, Definitions: ToolDefs()}
}

// Close releases the worktree.
func (t *Tools) Close() error {
	if err := t.root.Close(); err != nil {
		return fmt.Errorf("tools: close worktree: %w", err)
	}
	return nil
}

// Transcript returns the steps so far.
func (t *Tools) Transcript() []judgev1.Step {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]judgev1.Step{}, t.steps...)
}

// Call runs one tool call and records it in the transcript.
func (t *Tools) Call(ctx context.Context, c judgev1.ToolCall) judgev1.ToolResult {
	res := judgev1.ToolResult{Type: "tool_result", ID: c.ID, Call: c.Call}
	t.mu.Lock()
	n := len(t.steps) + 1
	over := n > t.limits.MaxSteps || t.total >= t.limits.MaxTotalBytes
	t.mu.Unlock()
	var content any
	var err error
	if over {
		err = errors.New("tool budget exhausted (steps or bytes): answer with what you have")
	} else {
		content, err = t.run(ctx, c.Tool, c.Args)
	}
	if err == nil {
		res.Content, res.Truncated, err = t.encode(content)
	}
	if err == nil {
		res.Text = Render(c.Tool, res.Content, res.Truncated)
	}
	if err != nil {
		res.Error = cutStr(err.Error(), 500)
	} else {
		res.OK = true
	}
	args := c.Args
	if !json.Valid(args) || len(args) == 0 || args[0] != '{' {
		args = json.RawMessage(`{}`)
	}
	step := judgev1.Step{N: n, Tool: c.Tool, Args: args, ResultBytes: len(res.Content), Error: res.Error}
	t.mu.Lock()
	t.total += len(res.Content)
	t.steps = append(t.steps, step)
	rec := t.record
	t.mu.Unlock()
	if rec != nil {
		rec(step)
	}
	return res
}

func (t *Tools) encode(v any) (json.RawMessage, bool, error) {
	b, err := marshalRaw(v)
	if err != nil {
		return nil, false, err
	}
	if len(b) <= t.limits.MaxResultBytes {
		return b, false, nil
	}
	tr, ok := v.(truncater)
	if !ok {
		return nil, false, errors.New("result too large")
	}
	for len(b) > t.limits.MaxResultBytes && tr.shrink() {
		if b, err = marshalRaw(v); err != nil {
			return nil, false, err
		}
	}
	return b, true, nil
}

// marshalRaw encodes a result without HTML escaping: repository text must reach the model
// verbatim ("<name>", "&&", "->"), or quotes copied from it cannot match the file.
func marshalRaw(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode result: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// truncater results can drop their tail to fit the size cap.
type truncater interface{ shrink() bool }

func (t *Tools) run(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	switch tool {
	case judgev1.ToolListDir:
		var a struct {
			Path string `json:"path"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.listDir(a.Path)
	case judgev1.ToolReadFile:
		var a struct {
			Path     string `json:"path"`
			FromLine int    `json:"from_line"`
			ToLine   int    `json:"to_line"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.readFile(a.Path, a.FromLine, a.ToLine)
	case judgev1.ToolGrep:
		var a struct {
			Pattern    string `json:"pattern"`
			Path       string `json:"path"`
			MaxResults int    `json:"max_results"`
		}
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return t.grep(ctx, a.Pattern, a.Path, a.MaxResults)
	case judgev1.ToolGitLog, judgev1.ToolGitShow:
		return t.git(ctx, tool, raw)
	}
	if f, ok := moreTools[tool]; ok {
		return f(ctx, t, raw)
	}
	return nil, fmt.Errorf("unknown tool %q (judge-tools/v1: %s)", tool, strings.Join(AllTools, ", "))
}

// ParseToolArgs decodes tool arguments strictly (unknown fields are an error).
func strictArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// CleanPath validates a repository-relative path: no absolute paths, no "..", no NUL, no
// leading "-" (never an option to git). "" means the root (".").
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || p == "./" {
		return ".", nil
	}
	if strings.ContainsRune(p, 0) || path.IsAbs(p) || strings.HasPrefix(p, "-") || strings.Contains(p, `\`) {
		return "", fmt.Errorf("path %q refused: repository-relative paths only", p)
	}
	c := path.Clean(p)
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q refused: outside the worktree", p)
	}
	if strings.HasPrefix(c, "-") { // "./-x" cleans to "-x": never an option to git
		return "", fmt.Errorf("path %q refused: repository-relative paths only", p)
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == ".git" { // exactly ".git": ".github", ".gitignore" are ordinary paths
			return "", fmt.Errorf("path %q refused: .git is not readable (use git_log, git_show, git_changes)", p)
		}
	}
	return c, nil
}

func (t *Tools) checked(p string) (string, error) {
	c, err := CleanPath(p)
	if err != nil {
		return "", err
	}
	if c != "." && !t.allow(c) {
		return "", fmt.Errorf("path %q refused: classified private (the provider is a cloud provider)", c)
	}
	return c, nil
}

type dirEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

type listResult struct {
	Entries []dirEntry `json:"entries"`
}

func (r *listResult) shrink() bool {
	if len(r.Entries) == 0 {
		return false
	}
	r.Entries = r.Entries[:len(r.Entries)/2]
	return true
}

func (t *Tools) listDir(p string) (any, error) {
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	ents, err := fs.ReadDir(t.root.FS(), c)
	if err != nil {
		return nil, fmt.Errorf("list_dir %s: %w", c, unwrapPath(err))
	}
	res := &listResult{Entries: []dirEntry{}}
	for _, e := range ents {
		rel := path.Join(c, e.Name())
		if e.Name() == ".git" || !t.allow(rel) || len(res.Entries) == maxListEntries {
			continue
		}
		d := dirEntry{Name: e.Name(), Type: "file"}
		if e.IsDir() {
			d.Type = "dir"
		} else if info, err := e.Info(); err == nil {
			d.Size = info.Size()
		}
		res.Entries = append(res.Entries, d)
	}
	return res, nil
}

type linesResult struct {
	Path       string   `json:"path"`
	Rev        string   `json:"rev,omitempty"`
	FromLine   int      `json:"from_line"`
	ToLine     int      `json:"to_line"`
	TotalLines int      `json:"total_lines"`
	Lines      []string `json:"lines"`
}

func (r *linesResult) shrink() bool {
	if len(r.Lines) <= 1 {
		return false
	}
	r.Lines = r.Lines[:len(r.Lines)/2]
	r.ToLine = r.FromLine + len(r.Lines) - 1
	return true
}

func (t *Tools) readFile(p string, from, to int) (any, error) {
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	lines, err := t.fileLines(c)
	if err != nil {
		return nil, err
	}
	return window(c, lines, from, to), nil
}

func (t *Tools) fileLines(c string) ([]string, error) {
	f, err := t.root.Open(c)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", c, unwrapPath(err))
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxToolFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", c, unwrapPath(err))
	}
	if len(b) > maxToolFileBytes || bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
		return nil, fmt.Errorf("read %s: binary or larger than %d bytes", c, maxToolFileBytes)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), nil
}

func window(p string, lines []string, from, to int) *linesResult {
	if from < 1 {
		from = 1
	}
	if to < from || to > len(lines) {
		to = len(lines)
	}
	to = min(to, from+maxReadLines-1)
	res := &linesResult{Path: p, FromLine: from, ToLine: to, TotalLines: len(lines), Lines: []string{}}
	for i := from; i <= to && i <= len(lines); i++ {
		res.Lines = append(res.Lines, cutStr(lines[i-1], maxLineChars))
	}
	res.ToLine = from + len(res.Lines) - 1
	return res
}

func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func cutStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

type grepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type grepResult struct {
	Matches []grepMatch `json:"matches"`
}

func (r *grepResult) shrink() bool {
	if len(r.Matches) == 0 {
		return false
	}
	r.Matches = r.Matches[:len(r.Matches)/2]
	return true
}

func (t *Tools) grep(ctx context.Context, pattern, p string, maxResults int) (any, error) {
	if pattern == "" || len(pattern) > 500 {
		return nil, errors.New("grep: pattern is required (at most 500 bytes)")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("grep: %w", err)
	}
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	if maxResults <= 0 || maxResults > 200 {
		maxResults = 50
	}
	g := &grepWalk{t: t, ctx: ctx, re: re, max: maxResults, res: &grepResult{Matches: []grepMatch{}}}
	if err := fs.WalkDir(t.root.FS(), c, g.visit); err != nil {
		return nil, fmt.Errorf("grep: %w", err)
	}
	return g.res, nil
}

type grepWalk struct {
	t       *Tools
	ctx     context.Context
	re      *regexp.Regexp
	max     int
	scanned int
	res     *grepResult
}

func (g *grepWalk) visit(fp string, d fs.DirEntry, err error) error {
	switch {
	case err != nil || g.ctx.Err() != nil:
		return fs.SkipDir
	case d.IsDir() && d.Name() == ".git":
		return fs.SkipDir
	case d.IsDir() || !g.t.allow(fp) || len(g.res.Matches) >= g.max || g.scanned > maxGrepScanBytes:
		return nil
	}
	lines, err := g.t.fileLines(fp)
	if err != nil {
		return nil //nolint:nilerr // unreadable or binary files are skipped
	}
	for i, l := range lines {
		g.scanned += len(l) + 1
		if g.re.MatchString(l) && len(g.res.Matches) < g.max {
			g.res.Matches = append(g.res.Matches, grepMatch{Path: fp, Line: i + 1, Text: cutStr(l, maxLineChars)})
		}
	}
	return nil
}

type commit struct {
	SHA     string `json:"sha"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

type logResult struct {
	Commits []commit `json:"commits"`
}

func (r *logResult) shrink() bool {
	if len(r.Commits) == 0 {
		return false
	}
	r.Commits = r.Commits[:len(r.Commits)/2]
	return true
}

type showResult struct {
	Rev  string `json:"rev"`
	Text string `json:"text"`
}

func (r *showResult) shrink() bool {
	if len(r.Text) < 200 {
		return false
	}
	r.Text = strings.ToValidUTF8(r.Text[:len(r.Text)/2], "") + "\n… (truncated)"
	return true
}

func (t *Tools) git(ctx context.Context, tool string, raw json.RawMessage) (any, error) {
	var a struct {
		Path     string `json:"path"`
		Rev      string `json:"rev"`
		MaxCount int    `json:"max_count"`
	}
	if err := strictArgs(raw, &a); err != nil {
		return nil, err
	}
	if tool == judgev1.ToolGitLog {
		if a.Rev != "" {
			return nil, errors.New("git_log: unknown argument rev")
		}
		return t.gitLog(ctx, a.Path, a.MaxCount)
	}
	if a.MaxCount != 0 {
		return nil, errors.New("git_show: unknown argument max_count")
	}
	return t.gitShow(ctx, a.Rev, a.Path)
}

func (t *Tools) gitLog(ctx context.Context, p string, n int) (any, error) {
	c, err := t.checked(p)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 50 {
		n = 20
	}
	out, err := repo.Git(ctx, t.dir, "log", fmt.Sprintf("--max-count=%d", n), "--format=%H%x09%cI%x09%s", "HEAD", "--", c)
	if err != nil {
		return nil, fmt.Errorf("git_log: %w", err)
	}
	res := &logResult{Commits: []commit{}}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(l, "\t", 3)
		if len(f) == 3 {
			res.Commits = append(res.Commits, commit{SHA: f[0][:min(12, len(f[0]))], Date: f[1], Subject: cutStr(f[2], 200)})
		}
	}
	return res, nil
}

func (t *Tools) gitShow(ctx context.Context, rev, p string) (any, error) {
	if !repo.ValidRef(rev) {
		return nil, fmt.Errorf("git_show: invalid revision %q", rev)
	}
	if p != "" {
		c, err := t.checked(p)
		if err != nil {
			return nil, err
		}
		out, err := repo.Git(ctx, t.dir, "show", rev+":"+c)
		if err != nil {
			return nil, fmt.Errorf("git_show: %w", err)
		}
		lr := window(c, strings.Split(strings.TrimSuffix(out, "\n"), "\n"), 1, 0)
		lr.Rev = rev
		return lr, nil
	}
	names, err := repo.Git(ctx, t.dir, "show", "--name-only", "--format=", rev, "--")
	if err != nil {
		return nil, fmt.Errorf("git_show: %w", err)
	}
	for _, n := range strings.Fields(names) {
		if !t.allow(n) {
			return nil, fmt.Errorf("git_show %s refused: it touches %s, classified private", rev, n)
		}
	}
	out, err := repo.Git(ctx, t.dir, "show", "--no-color", "--no-ext-diff", "--stat", "--patch", rev, "--")
	if err != nil {
		return nil, fmt.Errorf("git_show: %w", err)
	}
	return &showResult{Rev: rev, Text: out}, nil
}

// Verify checks every evidence reference: the quote must appear within ±2 lines of the given
// line, in the worktree (or in the file at rev). It returns how many were verified and the
// first failure.
func (t *Tools) Verify(ctx context.Context, refs []judgev1.EvidenceRef) (int, error) {
	ok := 0
	for _, r := range refs {
		if err := t.verifyOne(ctx, r); err != nil {
			return ok, err
		}
		ok++
	}
	return ok, nil
}

func (t *Tools) verifyOne(ctx context.Context, r judgev1.EvidenceRef) error {
	q := strings.TrimSpace(r.Quote)
	if len(q) < MinQuoteBytes {
		return fmt.Errorf("evidence %s:%d: quote too short", r.Path, r.Line)
	}
	c, err := t.checked(r.Path)
	if err != nil {
		return err
	}
	var lines []string
	if r.Rev != "" {
		if !repo.ValidRef(r.Rev) {
			return fmt.Errorf("evidence %s: invalid revision", r.Path)
		}
		out, err := repo.Git(ctx, t.dir, "show", r.Rev+":"+c)
		if err != nil {
			return fmt.Errorf("evidence %s@%s: %w", c, r.Rev, err)
		}
		lines = strings.Split(out, "\n")
	} else if lines, err = t.fileLines(c); err != nil {
		return err
	}
	for n := max(1, r.Line-verifySlack); n <= min(len(lines), r.Line+verifySlack); n++ {
		if strings.Contains(lines[n-1], q) {
			return nil
		}
	}
	return fmt.Errorf("evidence %s:%d: quote not found within ±%d lines", c, r.Line, verifySlack)
}

type toolsKey struct{}

// WithTools attaches the tools of an agentic request to ctx (providers find them there).
func WithTools(ctx context.Context, t *Tools) context.Context {
	return context.WithValue(ctx, toolsKey{}, t)
}

// ToolsFrom returns the tools attached to ctx, or nil.
func ToolsFrom(ctx context.Context) *Tools {
	t, _ := ctx.Value(toolsKey{}).(*Tools)
	return t
}
