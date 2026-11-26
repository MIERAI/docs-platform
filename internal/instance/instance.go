// Package instance loads the instance configuration (contract docs-platform/instance/v1,
// contracts/instance-v1.schema.json): everything that belongs to one operator's installation
// rather than to a repository or to the product — which model provider answers which task,
// its model id, limits and budget, where credentials live, how each inspected repository and
// path is classified, and the thresholds that decide when a provider may replace another.
// A repository's own .docsp.yaml can never select a model or a provider.
package instance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Provider kinds.
const (
	ProviderNone       = "none"
	ProviderOllama     = "ollama"
	ProviderClaudeCode = "claude-code"
	ProviderAnthropic  = "anthropic"
	ProviderExec       = "exec"
)

// Classifications.
const (
	Public  = "public"
	Private = "private"
)

// Instance is docs-platform/instance/v1.
type Instance struct {
	Version        int            `yaml:"version"`
	AI             AI             `yaml:"ai"`
	Classification Classification `yaml:"classification"`
	Eval           Eval           `yaml:"eval"`
	// Prices override or extend the built-in list prices used for estimates (USD per million
	// tokens, keyed by model id).
	Prices map[string]Price `yaml:"prices"`
	// Consistency is the continuous-consistency policy: freshness (cycle counting), ABC
	// classes, the per-run budget and the SLO (docs/design.md §12).
	Consistency Consistency `yaml:"consistency"`
	// Groups are repository groups: documentation in one repository checked against code in
	// others, at the revisions each doc set declares (docs/design.md §12.4).
	Groups []Group `yaml:"groups"`
}

// Repository roles in a group.
const (
	RoleCode        = "code"
	RoleDocs        = "docs"
	RoleAPISpec     = "api-spec"
	RoleDBMigration = "db-migration"
	RoleInfra       = "infra"
)

// Group is a set of repositories whose documentation and code are checked together.
type Group struct {
	Name    string      `yaml:"name"`
	Repos   []GroupRepo `yaml:"repos"`
	DocSets []DocSet    `yaml:"doc_sets"`
	// External are path globs (in any repository of the group) whose content is not this
	// group's to verify (vendored sources, mirrors): evidence there does not count.
	External []string `yaml:"external"`
}

// GroupRepo is a repository of a group; Path is its default checkout (a -checkout flag
// overrides it).
type GroupRepo struct {
	Name string `yaml:"name"`
	Role string `yaml:"role"`
	Path string `yaml:"path"`
}

// DocSet is documentation in one repository that describes code at declared revisions.
type DocSet struct {
	Name string   `yaml:"name"`
	Repo string   `yaml:"repo"`
	Docs []string `yaml:"docs"`
	// Targets are what the docs describe: repository and revision (a branch, tag or commit;
	// several allowed, e.g. develop and prod). Empty = the doc's own repository at HEAD.
	Targets []Target `yaml:"targets"`
	// Generated docs are judged at their generator input, not on their text.
	Generated []Generated `yaml:"generated"`
}

// Target is a repository at a declared revision.
type Target struct {
	Repo string `yaml:"repo"`
	Ref  string `yaml:"ref"`
}

// Generated maps generated documents (glob) to the input they are generated from.
type Generated struct {
	Docs  string `yaml:"docs"`
	Input string `yaml:"input"`
}

var roles = map[string]bool{RoleCode: true, RoleDocs: true, RoleAPISpec: true, RoleDBMigration: true, RoleInfra: true}

// problem says what is wrong with a group repository ("" = nothing).
func (r GroupRepo) problem() string {
	switch {
	case r.Name == "" || !roles[r.Role]:
		return "name and a role (code, docs, api-spec, db-migration, infra) are required"
	case r.Path != "" && !filepath.IsAbs(r.Path):
		return "path must be absolute"
	}
	return ""
}

func (g Group) validate(i int) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("groups[%d]: "+format, append([]any{i}, a...)...))
	}
	if g.Name == "" {
		bad("name is required")
	}
	repos := map[string]bool{}
	for _, r := range g.Repos {
		if msg := r.problem(); msg != "" {
			bad("repo %q: %s", r.Name, msg)
		}
		repos[r.Name] = true
	}
	for _, d := range g.DocSets {
		if d.Name == "" || !repos[d.Repo] || len(d.Docs) == 0 {
			bad("doc_set %q: name, a repo of the group and docs globs are required", d.Name)
		}
		for _, t := range d.Targets {
			if !repos[t.Repo] || t.Ref == "" || strings.HasPrefix(t.Ref, "-") || strings.Contains(t.Ref, "..") {
				bad("doc_set %q: target %s@%s: a repo of the group and a revision are required", d.Name, t.Repo, t.Ref)
			}
		}
	}
	return errs
}

// Consistency is the continuous-consistency policy (0 / empty = the default).
type Consistency struct {
	// MaxAgeDays: every claim is verified again within this many days (default 30).
	MaxAgeDays int `yaml:"max_age_days"`
	// ChurnCommits / ChurnLines: a claim is due early when this many commits, or changed
	// lines, touched its evidence scope since it was verified (0 = not checked).
	ChurnCommits int `yaml:"churn_commits"`
	ChurnLines   int `yaml:"churn_lines"`
	// Classes are the ABC classes, first match wins; a claim matching none is class C with
	// MaxAgeDays.
	Classes []ABCClass `yaml:"classes"`
	// RunIntervalDays is how often the scheduler runs (default 1): it picks at least the
	// claims needed so that every claim is verified within its class's max age.
	RunIntervalDays int `yaml:"run_interval_days"`
	// Budget caps one scheduler run.
	Budget RunBudget `yaml:"budget"`
	SLO    SLO       `yaml:"slo"`
	// SampleSize is the size of the weekly accuracy sample (default 30).
	SampleSize int `yaml:"sample_size"`
}

// ABCClass is one ABC class: by document glob (importance) or by recent churn.
type ABCClass struct {
	Name       string   `yaml:"name"`
	MaxAgeDays int      `yaml:"max_age_days"`
	Docs       []string `yaml:"docs"`
	// MinCommits90d: the class applies when at least this many commits touched the claim's
	// document or evidence in the last 90 days (0 = by document glob only).
	MinCommits90d int `yaml:"min_commits_90d"`
}

// RunBudget caps one scheduler run (0 = no cap of that kind).
type RunBudget struct {
	MaxClaims int     `yaml:"max_claims"`
	MaxUSD    float64 `yaml:"max_usd"`
	// USDPerClaim estimates one re-judgment (default 0.04, the measured agentic cost).
	USDPerClaim float64 `yaml:"usd_per_claim"`
}

// SLO holds the documentation-consistency objectives (rates 0..1).
type SLO struct {
	// Accuracy: share of sampled claims that still hold (default 0.8: error budget 20%).
	Accuracy float64 `yaml:"accuracy"`
	// Freshness: share of claims verified within their max age (default 0.95).
	Freshness float64 `yaml:"freshness"`
	// PhantomRate: largest acceptable share of dangling references (default 0.01).
	PhantomRate float64 `yaml:"phantom_rate"`
}

// WithDefaults fills unset fields.
func (c Consistency) WithDefaults() Consistency {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.MaxAgeDays, 30)
	def(&c.RunIntervalDays, 1)
	def(&c.SampleSize, 30)
	if c.Budget.USDPerClaim <= 0 {
		c.Budget.USDPerClaim = 0.04
	}
	if c.SLO.Accuracy <= 0 {
		c.SLO.Accuracy = 0.8
	}
	if c.SLO.Freshness <= 0 {
		c.SLO.Freshness = 0.95
	}
	if c.SLO.PhantomRate <= 0 {
		c.SLO.PhantomRate = 0.01
	}
	return c
}

func (s SLO) validate() []error {
	for _, r := range []float64{s.Accuracy, s.Freshness, s.PhantomRate} {
		if r < 0 || r > 1 {
			return []error{errors.New("consistency.slo: rates are between 0 and 1")}
		}
	}
	return nil
}

func (c Consistency) validate() []error {
	var errs []error
	if c.MaxAgeDays < 0 || c.ChurnCommits < 0 || c.ChurnLines < 0 || c.RunIntervalDays < 0 || c.SampleSize < 0 ||
		c.Budget.MaxClaims < 0 || c.Budget.MaxUSD < 0 || c.Budget.USDPerClaim < 0 {
		errs = append(errs, errors.New("consistency: limits must not be negative"))
	}
	errs = append(errs, c.SLO.validate()...)
	for i, k := range c.Classes {
		if k.Name == "" || k.MaxAgeDays <= 0 || k.MinCommits90d < 0 {
			errs = append(errs, fmt.Errorf("consistency.classes[%d]: name and a positive max_age_days are required", i))
		}
	}
	return errs
}

// AI selects a model per task; each task is swappable and evaluated on its own.
type AI struct {
	// Judge answers "does this evidence / change contradict this doc section?".
	Judge Task `yaml:"judge"`
	// Draft proposes a doc edit, only after the judge said yes; a human reviews it.
	Draft Task `yaml:"draft"`
}

// Task configures the provider of one task.
type Task struct {
	Provider        string     `yaml:"provider"`
	Model           string     `yaml:"model"`
	Effort          string     `yaml:"effort"`
	TimeoutSeconds  int        `yaml:"timeout_seconds"`
	MaxConcurrency  int        `yaml:"max_concurrency"`
	MaxOutputTokens int        `yaml:"max_output_tokens"`
	Budget          Budget     `yaml:"budget"`
	ClaudeCode      ClaudeCode `yaml:"claude_code"`
	Anthropic       Anthropic  `yaml:"anthropic"`
	Ollama          Ollama     `yaml:"ollama"`
	Exec            Exec       `yaml:"exec"`
	// Agentic tunes the agentic mode of the judge (docsp audit/eval-model -mode agentic).
	Agentic Agentic `yaml:"agentic"`
}

// Agentic tunes the agentic judge (0 / "" = the default).
type Agentic struct {
	// MaxSteps bounds the tool steps of one per-line request (default 16).
	MaxSteps int `yaml:"max_steps"`
	// DriftMaxSteps bounds the tool steps of the repository drift scan (default 45).
	DriftMaxSteps int `yaml:"drift_max_steps"`
	// MaxRequestUSD caps one per-line request (default 0.6); DriftMaxRequestUSD the drift scan
	// (default 2.5). Reaching a cap ends that request without an answer, not the run.
	MaxRequestUSD      float64 `yaml:"max_request_usd"`
	DriftMaxRequestUSD float64 `yaml:"drift_max_request_usd"`
	// Group is line (one request per line, default) or doc (up to six lines of one document
	// per request: cheaper, evaluated less).
	Group string `yaml:"group"`
	// Effort of agentic requests. Empty (default) = the model's own default, which is what
	// the agentic design was evaluated with; ai.judge.effort applies to snippet mode only.
	Effort string `yaml:"effort"`
}

// Budget is the hard per-run limit of one task (0 = the default).
type Budget struct {
	MaxRequests int     `yaml:"max_requests"`
	MaxUSD      float64 `yaml:"max_usd"`
	MaxTokens   int     `yaml:"max_tokens"`
}

// ClaudeCode runs the Claude Code CLI non-interactively (`claude -p`) with the operator's own
// login; docs-platform never reads, prints or copies the credential.
type ClaudeCode struct {
	Program string `yaml:"program"`
	// PassEnv names environment variables handed to the CLI unchanged (e.g. the CI token
	// variable); values are never read by docs-platform.
	PassEnv []string `yaml:"pass_env"`
}

// Anthropic is the Messages API provider (an out-of-process judge/v1 program).
type Anthropic struct {
	Program    string `yaml:"program"`
	APIKeyFile string `yaml:"api_key_file"`
	APIKeyEnv  string `yaml:"api_key_env"`
	MaxRetries int    `yaml:"max_retries"`
	// Fallbacks is "default" (server-side refusal fallback) or "none".
	Fallbacks string `yaml:"fallbacks"`
}

// Ollama is a local model server.
type Ollama struct {
	Endpoint   string `yaml:"endpoint"`
	Seed       int    `yaml:"seed"`
	NumCtx     int    `yaml:"num_ctx"`
	NumPredict int    `yaml:"num_predict"`
	Retries    int    `yaml:"retries"`
}

// Exec is any external program speaking judge/v1 as JSON Lines on stdin/stdout.
type Exec struct {
	Command []string `yaml:"command"`
	// Local must be true to send private content: an exec provider is treated as a cloud
	// provider unless the operator declares it local.
	Local   bool     `yaml:"local"`
	PassEnv []string `yaml:"pass_env"`
	// Modes the program supports: snippet (default) and/or agentic (judge-tools/v1).
	Modes []string `yaml:"modes"`
}

// Price is a list price in USD per million tokens.
type Price struct {
	Input     float64 `yaml:"input"`
	Output    float64 `yaml:"output"`
	CacheRead float64 `yaml:"cache_read"`
}

// Classification says which content may leave the operator's machines.
type Classification struct {
	// Default applies to repositories and paths not listed (default private).
	Default string            `yaml:"default"`
	Repos   map[string]string `yaml:"repos"`
	Paths   []PathClass       `yaml:"paths"`
}

// PathClass overrides the class of paths (glob, "**" allowed) in one repository.
type PathClass struct {
	Repo  string `yaml:"repo"`
	Glob  string `yaml:"glob"`
	Class string `yaml:"class"`
}

// Eval holds the switching rule: a candidate provider replaces the current one for a task
// when it meets every threshold of that task on the frozen eval set.
type Eval struct {
	Judge JudgeThresholds `yaml:"judge"`
	Draft DraftThresholds `yaml:"draft"`
}

// JudgeThresholds for the judge task (rates are 0..1; 0 = not checked for max_*).
type JudgeThresholds struct {
	MinPrecision          float64 `yaml:"min_precision"`
	MinRecall             float64 `yaml:"min_recall"`
	MaxUnknownRate        float64 `yaml:"max_unknown_rate"`
	MaxQuoteRejectionRate float64 `yaml:"max_quote_rejection_rate"`
	MaxUSDPerItem         float64 `yaml:"max_usd_per_item"`
	MaxP50LatencyMS       int     `yaml:"max_p50_latency_ms"`
}

// DraftThresholds for the draft task.
type DraftThresholds struct {
	MinSupportedRate float64 `yaml:"min_supported_rate"`
	MinSimilarity    float64 `yaml:"min_similarity"`
	MaxUSDPerItem    float64 `yaml:"max_usd_per_item"`
	MaxP50LatencyMS  int     `yaml:"max_p50_latency_ms"`
}

// Defaults returns the instance used without a file: no model, everything private,
// conservative thresholds.
func Defaults() *Instance {
	return &Instance{
		Version: 1,
		AI: AI{
			Judge: Task{Provider: ProviderNone},
			Draft: Task{Provider: ProviderNone},
		},
		Classification: Classification{Default: Private},
		Eval: Eval{
			Judge: JudgeThresholds{MinPrecision: 0.8, MinRecall: 0.5, MaxUnknownRate: 0.3, MaxQuoteRejectionRate: 0.1},
			Draft: DraftThresholds{MinSupportedRate: 0.9, MinSimilarity: 0.3},
		},
	}
}

// Load reads an instance file; "" yields Defaults().
func Load(p string) (*Instance, error) {
	if p == "" {
		return Defaults(), nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("instance config: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("instance config: %w", err)
	}
	return Parse(b)
}

// Parse decodes and validates an instance document.
func Parse(b []byte) (*Instance, error) {
	in := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(in); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse instance config: %w", err)
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	return in, nil
}

// Validate checks semantic constraints.
func (in *Instance) Validate() error {
	var errs []error
	if in.Version != 1 {
		errs = append(errs, fmt.Errorf("instance config version %d: this docsp reads version 1", in.Version))
	}
	errs = append(errs, in.AI.Judge.validate("ai.judge")...)
	errs = append(errs, in.AI.Draft.validate("ai.draft")...)
	errs = append(errs, in.Classification.validate()...)
	errs = append(errs, in.Consistency.validate()...)
	for i, g := range in.Groups {
		errs = append(errs, g.validate(i)...)
	}
	for id, p := range in.Prices {
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 {
			errs = append(errs, fmt.Errorf("prices[%s]: must not be negative", id))
		}
	}
	return errors.Join(errs...)
}

func (t *Task) validate(where string) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf(where+": "+format, a...))
	}
	if t.Provider == "" || t.Provider == ProviderNone {
		t.Provider = ProviderNone
		return nil
	}
	errs = append(errs, t.validateProvider(where)...)
	if t.Model == "" {
		bad("model is required for provider %s", t.Provider)
	}
	switch t.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		bad("effort %q: want low, medium, high, xhigh or max", t.Effort)
	}
	if t.TimeoutSeconds < 0 || t.MaxConcurrency < 0 || t.MaxOutputTokens < 0 || t.Budget.MaxRequests < 0 ||
		t.Budget.MaxUSD < 0 || t.Budget.MaxTokens < 0 {
		bad("limits must not be negative")
	}
	errs = append(errs, t.Agentic.validate(where)...)
	for _, e := range append(append([]string{}, t.ClaudeCode.PassEnv...), t.Exec.PassEnv...) {
		if !validEnvName(e) {
			bad("pass_env %q is not an environment variable name", e)
		}
	}
	return errs
}

func (a Agentic) validate(where string) []error {
	var errs []error
	if a.MaxSteps < 0 || a.DriftMaxSteps < 0 || a.MaxRequestUSD < 0 || a.DriftMaxRequestUSD < 0 {
		errs = append(errs, fmt.Errorf("%s: agentic limits must not be negative", where))
	}
	if a.Group != "" && a.Group != "line" && a.Group != "doc" {
		errs = append(errs, fmt.Errorf("%s: agentic.group %q: want line or doc", where, a.Group))
	}
	switch a.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		errs = append(errs, fmt.Errorf("%s: agentic.effort %q: want low, medium, high, xhigh or max", where, a.Effort))
	}
	return errs
}

func (t *Task) validateProvider(where string) []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf(where+": "+format, a...))
	}
	switch t.Provider {
	case ProviderOllama:
		if t.Ollama.Endpoint == "" {
			bad("provider ollama needs ollama.endpoint")
		}
	case ProviderClaudeCode:
		if t.ClaudeCode.Program != "" && !filepath.IsAbs(t.ClaudeCode.Program) {
			bad("claude_code.program must be an absolute path")
		}
	case ProviderAnthropic:
		errs = append(errs, t.Anthropic.validate(where)...)
	case ProviderExec:
		if len(t.Exec.Command) == 0 || !filepath.IsAbs(t.Exec.Command[0]) {
			bad("provider exec needs exec.command with an absolute program path")
		}
		for _, m := range t.Exec.Modes {
			if m != "snippet" && m != "agentic" {
				bad("exec.modes %q: want snippet or agentic", m)
			}
		}
	default:
		bad("provider %q: want none, ollama, claude-code, anthropic or exec", t.Provider)
	}
	return errs
}

func (a Anthropic) validate(where string) []error {
	var errs []error
	if a.Program == "" || !filepath.IsAbs(a.Program) {
		errs = append(errs, fmt.Errorf("%s: anthropic.program must be the absolute path of docsp-judge-anthropic", where))
	}
	if (a.APIKeyFile == "") == (a.APIKeyEnv == "") {
		errs = append(errs, fmt.Errorf("%s: set exactly one of anthropic.api_key_file (mode 0600) or anthropic.api_key_env", where))
	}
	if a.APIKeyFile != "" && !filepath.IsAbs(a.APIKeyFile) {
		errs = append(errs, fmt.Errorf("%s: anthropic.api_key_file must be absolute", where))
	}
	if a.APIKeyEnv != "" && !validEnvName(a.APIKeyEnv) {
		errs = append(errs, fmt.Errorf("%s: anthropic.api_key_env is not an environment variable name", where))
	}
	switch a.Fallbacks {
	case "", "default", "none":
	default:
		errs = append(errs, fmt.Errorf("%s: anthropic.fallbacks %q: want default or none", where, a.Fallbacks))
	}
	return errs
}

func validEnvName(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for i, r := range s {
		ok := r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9'
		if !ok {
			return false
		}
	}
	return true
}

func (c *Classification) validate() []error {
	var errs []error
	if c.Default == "" {
		c.Default = Private
	}
	okClass := func(s string) bool { return s == Public || s == Private }
	if !okClass(c.Default) {
		errs = append(errs, fmt.Errorf("classification.default %q: want public or private", c.Default))
	}
	for r, cl := range c.Repos {
		if !okClass(cl) {
			errs = append(errs, fmt.Errorf("classification.repos[%s] %q: want public or private", r, cl))
		}
	}
	for i, p := range c.Paths {
		if !okClass(p.Class) || p.Repo == "" || p.Glob == "" {
			errs = append(errs, fmt.Errorf("classification.paths[%d]: repo, glob and class (public|private) are required", i))
		}
		for _, seg := range strings.Split(p.Glob, "/") {
			if _, err := path.Match(seg, ""); err != nil {
				errs = append(errs, fmt.Errorf("classification.paths[%d]: bad glob %q", i, p.Glob))
			}
		}
	}
	return errs
}

// ClassOf returns the classification of path p in repository repo: the last matching path
// rule wins, then the repository's class, then the default. Unknown means private.
func (c Classification) ClassOf(repo, p string) string {
	class := c.Default
	if v, ok := c.Repos[repo]; ok {
		class = v
	}
	for _, r := range c.Paths {
		if r.Repo == repo && globMatch(r.Glob, p) {
			class = r.Class
		}
	}
	if class != Public {
		return Private
	}
	return Public
}

// globMatch matches slash-separated globs where "**" spans any number of segments.
func globMatch(pat, p string) bool {
	ps, xs := strings.Split(pat, "/"), strings.Split(p, "/")
	var rec func(i, j int) bool
	rec = func(i, j int) bool {
		for i < len(ps) {
			if ps[i] == "**" {
				for k := j; k <= len(xs); k++ {
					if rec(i+1, k) {
						return true
					}
				}
				return false
			}
			if j >= len(xs) {
				return false
			}
			if ok, _ := path.Match(ps[i], xs[j]); !ok {
				return false
			}
			i, j = i+1, j+1
		}
		return j == len(xs)
	}
	return rec(0, 0)
}

// builtinPrices are list prices (USD per million tokens) for estimates; instance prices
// override them. Aliases used by the Claude Code CLI map to the current model of the family.
var builtinPrices = map[string]Price{
	"claude-fable-5-2":  {Input: 8, Output: 40, CacheRead: 0.20},
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01},
	"fable":             {Input: 8, Output: 40, CacheRead: 0.20},
	"opus":              {Input: 4, Output: 20, CacheRead: 0.20},
	"sonnet":            {Input: 2, Output: 10, CacheRead: 0.10},
	"haiku":             {Input: 0.10, Output: 0.50, CacheRead: 0.01},
}

// PriceOf returns the list price of a model id and whether one is known.
func (in *Instance) PriceOf(model string) (Price, bool) {
	if p, ok := in.Prices[model]; ok {
		return p, true
	}
	p, ok := builtinPrices[model]
	return p, ok
}

// Cost is the list-price cost of a call.
func (p Price) Cost(input, output, cacheRead int) float64 {
	return (float64(input)*p.Input + float64(output)*p.Output + float64(cacheRead)*p.CacheRead) / 1e6
}
