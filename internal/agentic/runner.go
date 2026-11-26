package agentic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/judgev1"
)

// Groupings of the per-line judgment.
const (
	GroupLine = "line" // one request per line (default; the evaluated design)
	GroupDoc  = "doc"  // one request per document, up to GroupSize lines (cheaper)
)

// Defaults (from the prototype the design was evaluated with).
const (
	DefaultLineSteps    = 16
	DefaultDriftSteps   = 45
	DefaultGroupSize    = 6
	DefaultLineUSD      = 0.6
	DefaultDriftUSD     = 2.5
	maxResultBytes      = 32 << 10
	maxLineTotalBytes   = 512 << 10
	maxDriftTotalBytes  = 1 << 20
	maxDocFileBytes     = 2 << 20
	docGroupStepsBase   = 8
	docGroupStepsPerRow = 5
	docGroupMaxSteps    = 45
)

// Settings tune the agentic mode (instance configuration ai.judge.agentic, CLI flags).
type Settings struct {
	LineSteps  int
	DriftSteps int
	// LineUSD and DriftUSD cap one request (0 = no cap of its own; the run budget still
	// applies). A doc group may spend LineUSD per line plus one LineUSD.
	LineUSD   float64
	DriftUSD  float64
	Group     string
	GroupSize int
}

// FromInstance reads the settings of a judge task, filling defaults.
func FromInstance(a instance.Agentic) Settings {
	s := Settings{LineSteps: a.MaxSteps, DriftSteps: a.DriftMaxSteps, LineUSD: a.MaxRequestUSD, DriftUSD: a.DriftMaxRequestUSD, Group: a.Group}
	return s.withDefaults()
}

func (s Settings) withDefaults() Settings {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&s.LineSteps, DefaultLineSteps)
	def(&s.DriftSteps, DefaultDriftSteps)
	def(&s.GroupSize, DefaultGroupSize)
	if s.LineUSD == 0 {
		s.LineUSD = DefaultLineUSD
	}
	if s.DriftUSD == 0 {
		s.DriftUSD = DefaultDriftUSD
	}
	if s.Group == "" {
		s.Group = GroupLine
	}
	return s
}

// Options of a Runner (one repository).
type Options struct {
	Guard *judge.Guard
	// Repo is the logical repository name (classification is per repository and path).
	Repo string
	// Dir is the worktree; it is only ever read.
	Dir   string
	Class instance.Classification
	// CacheDir holds drift lists by HEAD SHA ("" = no cache).
	CacheDir string
	NoDrift  bool
	DryRun   bool
	Settings Settings
	// DocDir and DocRepo (repository groups) hold the documents when they are not in the
	// explored repository: the tools explore Dir (the target), the statement comes from
	// DocDir. Empty = the same repository.
	DocDir  string
	DocRepo string
	// Generated reports whether a document is generated, and from what input: generated
	// documents are not judged on their text (their input is).
	Generated func(path string) (input string, ok bool)
	// External are path globs whose content does not count as evidence (vendored sources,
	// mirrors, submodules).
	External []string
}

// crossRepo reports whether the documents live in another repository than the tools.
func (o *Options) crossRepo() bool { return o.DocDir != "" && o.DocDir != o.Dir }

func (o *Options) docDir() string {
	if o.crossRepo() {
		return o.DocDir
	}
	return o.Dir
}

func (o *Options) docRepo() string {
	if o.DocRepo != "" {
		return o.DocRepo
	}
	return o.Repo
}

// Runner judges documentation lines of one repository in agentic mode.
type Runner struct {
	o         Options
	policy    judge.Policy
	driftOnce sync.Once
	drift     *DriftList
	driftErr  error
	repoOnce  sync.Once
	repoText  string
}

// maxRecentCommits is how many recent commits the repository context lists.
const maxRecentCommits = 5

// repoText is the repository context of every prompt (computed once).
func (r *Runner) repoCtx(ctx context.Context) string {
	r.repoOnce.Do(func() {
		head, err := Head(ctx, r.o.Dir)
		if err != nil {
			return
		}
		branch, _ := repo.Git(ctx, r.o.Dir, "rev-parse", "--abbrev-ref", "HEAD")
		log, _ := repo.Git(ctx, r.o.Dir, "log", "-n"+strconv.Itoa(maxRecentCommits), "--format=%h %s", "HEAD", "--")
		var commits []string
		for _, l := range strings.Split(strings.TrimSpace(log), "\n") {
			if l != "" {
				commits = append(commits, cutRunes(l, 200))
			}
		}
		r.repoText = repoContext(r.o.Repo, head, strings.TrimSpace(branch), commits)
		if r.o.crossRepo() {
			r.repoText += fmt.Sprintf("\nThe documentation under test is from repository %s (not reachable through the tools); it documents %s.", r.o.docRepo(), r.o.Repo)
		}
	})
	return r.repoText
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// New returns a runner.
func New(o Options) *Runner {
	o.Settings = o.Settings.withDefaults()
	return &Runner{o: o, policy: judge.Policy{Repo: o.Repo, Class: o.Class, Cloud: o.Guard.Provider().Cloud()}}
}

// LineResult is the outcome for one documentation line.
type LineResult struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	// Category is derived by the harness from validated evidence; Verdict maps it to judge/v1
	// (a problem = yes, confirmed = no, anything else = unknown).
	Category string `json:"category"`
	Verdict  string `json:"verdict"`
	Derivation
	Reason       string  `json:"reason,omitempty"`
	Parse        string  `json:"parse,omitempty"`
	Error        string  `json:"error,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	LatencyMS    int64   `json:"latency_ms,omitempty"`
	Model        string  `json:"model,omitempty"`
	PromptHash   string  `json:"prompt_hash,omitempty"`
	GroupSize    int     `json:"group_size"`
	ToolSteps    int     `json:"tool_steps"`
	// Transcript is the tool steps of the request (arguments and result sizes, never contents).
	Transcript []judgev1.Step `json:"transcript,omitempty"`
	// Generated: the document is generated; it was not judged.
	Generated bool `json:"generated,omitempty"`
}

// Verdict maps a category to a judge/v1 verdict.
func Verdict(category string) string {
	switch {
	case IsProblem(category):
		return judgev1.Yes
	case category == Confirmed:
		return judgev1.No
	}
	return judgev1.Unknown
}

// Drift returns the repository's validated drift list: from the cache when this HEAD was
// scanned with the same prompt and model, else from one drift scan. Failure leaves the
// judgments without drift context (and is reported), never stops them.
func (r *Runner) Drift(ctx context.Context) (*DriftList, error) {
	r.driftOnce.Do(func() { r.drift, r.driftErr = r.scan(ctx) })
	return r.drift, r.driftErr
}

func (r *Runner) scan(ctx context.Context) (*DriftList, error) {
	head, err := Head(ctx, r.o.Dir)
	if err != nil {
		return nil, err
	}
	model := r.o.Guard.Name()
	d := &DriftList{Repo: r.o.Repo, Head: head, PromptHash: DriftPromptHash(), Model: model}
	file := ""
	if r.o.CacheDir != "" && !r.o.DryRun {
		file = cacheFile(r.o.CacheDir, r.o.Repo, head, model)
	}
	t, err := judge.NewPolicyTools(r.o.Dir, r.policy, judgev1.ToolLimits{MaxSteps: r.o.Settings.DriftSteps, MaxResultBytes: maxResultBytes, MaxTotalBytes: maxDriftTotalBytes})
	if err != nil {
		return nil, fmt.Errorf("drift: %w", err)
	}
	defer func() { _ = t.Close() }()
	if c, ok := loadCache(file); file != "" && ok {
		c.Entries, c.Cached = ValidateDrift(ctx, t, c.Raw), true // validation is always re-run
		return c, nil
	}
	p := driftPrompts(r.repoCtx(ctx))
	req := r.request("drift-"+hash16(r.o.Repo)[:8]+"-"+head[:12], judgev1.TaskRepoDrift,
		judgev1.Claim{Repo: r.o.Repo, Path: ".", Text: "repository " + r.o.Repo + " at " + head}, p, r.o.Settings.DriftSteps, r.o.Settings.DriftUSD)
	out, err := r.o.Guard.Judge(judge.WithTools(ctx, t), req)
	if err != nil {
		return nil, fmt.Errorf("drift scan: %w", err)
	}
	resp := out.Response
	d.CostUSD, d.Parse, d.Transcript = resp.Usage.CostUSD, resp.AnswerParse, resp.Transcript
	if resp.Error != nil || r.o.DryRun {
		if resp.Error != nil {
			d.Error = resp.Error.Code + ": " + resp.Error.Message
		}
		return d, nil
	}
	raw, err := ParseDriftAnswer(resp.Answer)
	if err != nil {
		d.Error = err.Error()
		return d, nil
	}
	d.Raw, d.Entries = raw, ValidateDrift(ctx, t, raw)
	if file != "" {
		if err := storeCache(file, d); err != nil {
			return d, err
		}
	}
	return d, nil
}

func (r *Runner) request(id, task string, cl judgev1.Claim, p prompts, steps int, usd float64) *judgev1.Request {
	req := &judgev1.Request{
		SchemaVersion: judgev1.SchemaVersion, ID: id, Task: judgev1.Task{Type: task}, Claim: cl,
		Evidence: []judgev1.Evidence{}, Mode: judgev1.ModeAgentic, DryRun: r.o.DryRun,
		Prompt: judgev1.Prompt{Version: PromptVersion, System: p.system, User: p.user, ReplySchema: p.schema, Hash: p.hash()},
		Limits: judgev1.Limits{MaxTurns: steps},
	}
	if usd > 0 {
		req.Limits.MaxRequestUSD = &usd
	}
	return req
}

// driftText is the drift context of the judgments ("" without drift).
func (r *Runner) driftText(ctx context.Context) (string, map[string]bool) {
	if r.o.NoDrift {
		return "", nil
	}
	d, err := r.Drift(ctx)
	if err != nil || d == nil {
		return "", nil
	}
	return d.Text(), d.IDs()
}

// Judge judges lines of one document: one request per line, or per group of lines when the
// grouping is "doc". Results come back in line order.
func (r *Runner) Judge(ctx context.Context, docPath string, lines []int) []LineResult {
	lines = append([]int{}, lines...)
	sort.Ints(lines)
	if r.o.Generated != nil {
		if in, gen := r.o.Generated(docPath); gen {
			return generatedResults(docPath, lines, in)
		}
	}
	src, err := readLines(r.o.docDir(), docPath)
	var sts []statement
	var out []LineResult
	for _, n := range lines {
		if err != nil || n < 1 || n > len(src) {
			out = append(out, LineResult{Path: docPath, Line: n, Category: Unknown, Verdict: judgev1.Unknown, Error: lineErr(err, docPath, n)})
			continue
		}
		sts = append(sts, statement{Path: docPath, Line: n, Context: contextOf(src, n)})
	}
	text := func(n int) string { return src[n-1] }
	if r.o.Settings.Group != GroupDoc {
		for _, s := range sts {
			out = append(out, r.judgeOne(ctx, s, text(s.Line)))
		}
	} else {
		for i := 0; i < len(sts); i += r.o.Settings.GroupSize {
			out = append(out, r.judgeGroup(ctx, sts[i:min(len(sts), i+r.o.Settings.GroupSize)], text)...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

func lineErr(err error, p string, n int) string {
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%s has no line %d", p, n)
}

func (r *Runner) tools(steps, total int) (*judge.Tools, error) {
	t, err := judge.NewPolicyTools(r.o.Dir, r.policy, judgev1.ToolLimits{MaxSteps: steps, MaxResultBytes: maxResultBytes, MaxTotalBytes: total})
	if err != nil {
		return nil, fmt.Errorf("agentic: %w", err)
	}
	return t, nil
}

func (r *Runner) judgeOne(ctx context.Context, s statement, text string) LineResult {
	dtext, ids := r.driftText(ctx)
	p := linePrompts(s, dtext, r.repoCtx(ctx))
	steps := r.o.Settings.LineSteps
	req := r.request(fmt.Sprintf("line-%s-%d", hash16(r.o.docRepo()+r.o.Repo, s.Path)[:8], s.Line), judgev1.TaskClaimFacts,
		judgev1.Claim{Repo: r.o.docRepo(), Path: s.Path, Line: s.Line, Text: text}, p, steps, r.o.Settings.LineUSD)
	res := LineResult{Path: s.Path, Line: s.Line, Category: Unknown, Verdict: judgev1.Unknown, GroupSize: 1, PromptHash: req.Prompt.Hash}
	t, err := r.tools(steps, maxLineTotalBytes)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = t.Close() }()
	r.setDoc(t, s.Path, []int{s.Line})
	resp, ok := r.ask(judge.WithTools(ctx, t), req, &res)
	if !ok {
		return res
	}
	a, err := ParseLineAnswer(resp.Answer)
	if err != nil {
		res.Error = err.Error()
	}
	r.derive(ctx, t, ids, a, &res)
	return res
}

// ask sends a request through the guard and accounts it in res; ok is false when there is
// no answer to derive from.
func (r *Runner) ask(ctx context.Context, req *judgev1.Request, res *LineResult) (*judgev1.Response, bool) {
	out, err := r.o.Guard.Judge(ctx, req)
	if err != nil {
		res.Error = err.Error()
		return nil, false
	}
	resp := out.Response
	res.CostUSD, res.LatencyMS, res.Model = resp.Usage.CostUSD, resp.LatencyMS, resp.Model.ID
	res.InputTokens = resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens
	res.OutputTokens, res.Parse, res.ToolSteps, res.Transcript = resp.Usage.OutputTokens, resp.AnswerParse, len(resp.Transcript), resp.Transcript
	if resp.Error != nil {
		res.Error = resp.Error.Code + ": " + resp.Error.Message
		return nil, false
	}
	return resp, !r.o.DryRun
}

func (r *Runner) derive(ctx context.Context, t *judge.Tools, ids map[string]bool, a LineAnswer, res *LineResult) {
	v := &Validator{Tools: t, Drift: ids, Doc: res.Path, Line: res.Line, External: r.o.External}
	if r.o.crossRepo() {
		v.Doc = "" // the document is not in the explored repository
	}
	res.Derivation = Derive(ctx, v, a.Facts)
	res.Category, res.Verdict, res.Reason = res.Derivation.Category, Verdict(res.Derivation.Category), a.Reason
}

func (r *Runner) judgeGroup(ctx context.Context, sts []statement, text func(int) string) []LineResult {
	dtext, ids := r.driftText(ctx)
	p := docPrompts(sts, dtext, r.repoCtx(ctx))
	n := len(sts)
	steps := min(docGroupMaxSteps, docGroupStepsBase+docGroupStepsPerRow*n)
	claims := make([]judgev1.Claim, 0, n)
	lines := make([]int, 0, n)
	for _, s := range sts {
		claims = append(claims, judgev1.Claim{Repo: r.o.docRepo(), Path: s.Path, Line: s.Line, Text: text(s.Line)})
		lines = append(lines, s.Line)
	}
	req := r.request(fmt.Sprintf("doc-%s-%d-%d", hash16(r.o.docRepo()+r.o.Repo, sts[0].Path)[:8], sts[0].Line, n), judgev1.TaskDocFacts,
		claims[0], p, steps, r.o.Settings.LineUSD*float64(n+1))
	req.Claims = claims
	base := LineResult{Category: Unknown, Verdict: judgev1.Unknown, GroupSize: n, PromptHash: req.Prompt.Hash}
	t, err := r.tools(steps, maxDriftTotalBytes)
	if err != nil {
		base.Error = err.Error()
		return spread(base, sts)
	}
	defer func() { _ = t.Close() }()
	r.setDoc(t, sts[0].Path, lines)
	resp, ok := r.ask(judge.WithTools(ctx, t), req, &base)
	base.CostUSD /= float64(n)
	base.InputTokens /= n
	base.OutputTokens /= n
	if !ok {
		return spread(base, sts)
	}
	answers, err := ParseDocAnswer(resp.Answer)
	out := spread(base, sts)
	for i := range out {
		a, found := answers[out[i].Line]
		switch {
		case err != nil:
			out[i].Error = err.Error()
		case !found:
			out[i].Error, out[i].Parse = "the answer has no entry for this line", "missing_line"
		default:
			r.derive(ctx, t, ids, a, &out[i])
		}
	}
	return out
}

// setDoc tells the tools which lines are under test (only when they are in the explored
// repository: a quote of them is not evidence).
func (r *Runner) setDoc(t *judge.Tools, p string, lines []int) {
	if !r.o.crossRepo() {
		t.SetDoc(judge.Doc{Path: p, Lines: lines})
	}
}

// generatedResults answers the lines of a generated document without a model.
func generatedResults(p string, lines []int, input string) []LineResult {
	why := "generated document: judge its generator input"
	if input != "" {
		why += " " + input
	}
	out := make([]LineResult, 0, len(lines))
	for _, n := range lines {
		out = append(out, LineResult{Path: p, Line: n, Category: Unknown, Verdict: judgev1.Unknown, Error: why, Generated: true})
	}
	return out
}

func spread(base LineResult, sts []statement) []LineResult {
	out := make([]LineResult, 0, len(sts))
	for _, s := range sts {
		r := base
		r.Path, r.Line = s.Path, s.Line
		out = append(out, r)
	}
	return out
}

// readLines reads a document of the worktree (confined to it; never through a symlink out).
func readLines(dir, p string) ([]string, error) {
	c, err := judge.CleanPath(p)
	if err != nil {
		return nil, fmt.Errorf("agentic: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("agentic: %w", err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(c)
	if err != nil {
		return nil, fmt.Errorf("agentic: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxDocFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("agentic: %w", err)
	}
	if len(b) > maxDocFileBytes {
		return nil, errors.New("agentic: document too large")
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), nil
}
