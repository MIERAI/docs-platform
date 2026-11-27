package onboard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Levels of a doctor check, from harmless to blocking.
const (
	OK   = "ok"
	Info = "info"
	Warn = "warn"
	Fail = "fail"
)

// DefaultTokenEnv is the environment variable the CI action passes the Claude token in.
const DefaultTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN" //nolint:gosec // G101: a variable name, not a credential

// Check is the result of one self-check.
type Check struct {
	Name    string `json:"name"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Report is the doctor's result (docs-platform/doctor/v1).
type Report struct {
	SchemaVersion string  `json:"schema_version"`
	Checks        []Check `json:"checks"`
}

func (r *Report) add(name, level, format string, a ...any) {
	r.Checks = append(r.Checks, Check{Name: name, Level: level, Message: fmt.Sprintf(format, a...)})
}

// Count returns how many checks have the level.
func (r *Report) Count(level string) int {
	n := 0
	for _, c := range r.Checks {
		if c.Level == level {
			n++
		}
	}
	return n
}

// DoctorOptions configure one self-check. Getenv and LookPath default to the process's; tests
// replace them. Values of environment variables are never stored or printed, only whether
// they are set.
type DoctorOptions struct {
	Root         string
	ConfigFile   string
	InstanceFile string
	// TokenEnv names the variable holding the model token in CI (default DefaultTokenEnv).
	TokenEnv string
	Getenv   func(string) string
	LookPath func(string) (string, error)
}

func (o *DoctorOptions) defaults() {
	if o.TokenEnv == "" {
		o.TokenEnv = DefaultTokenEnv
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.LookPath == nil {
		o.LookPath = execLookPath
	}
}

// Doctor runs every offline self-check: configuration, globs, tools, the model token, the
// workflow's permissions and budget inputs. Nothing is sent anywhere.
func Doctor(ctx context.Context, o DoctorOptions) *Report {
	o.defaults()
	rep := &Report{SchemaVersion: "1.0", Checks: []Check{}}
	cfg := checkConfig(rep, o)
	if cfg != nil {
		checkGlobs(rep, o.Root, cfg)
	}
	checkGit(ctx, rep, o)
	checkAI(rep, o)
	checkWorkflows(rep, o.Root)
	return rep
}

func checkConfig(rep *Report, o DoctorOptions) *config.Config {
	p := o.ConfigFile
	if p == "" {
		p = filepath.Join(o.Root, config.FileName)
	}
	_, statErr := os.Stat(p)
	cfg, err := config.Load(o.Root, o.ConfigFile)
	switch {
	case err != nil:
		rep.add("config", Fail, "%v", err)
		return nil
	case errors.Is(statErr, fs.ErrNotExist):
		rep.add("config", Info, "no %s: the defaults apply (docs %s); `docsp init` proposes one", config.FileName, strings.Join(cfg.Docs, ", "))
	default:
		rep.add("config", OK, "%s parses", p)
	}
	return cfg
}

func checkGlobs(rep *Report, root string, cfg *config.Config) {
	r, err := repo.Open(root, cfg.Exclude)
	if err != nil {
		rep.add("docs", Fail, "%v", err)
		return
	}
	files := r.Files()
	n := countMatching(cfg.Docs, files)
	if n == 0 {
		rep.add("docs", Fail, "the docs globs (%s) match no file: nothing to check", strings.Join(cfg.Docs, ", "))
		return
	}
	rep.add("docs", OK, "the docs globs match %s", plural(n, "file"))
	var idle []string
	for _, g := range cfg.Docs {
		if countMatching([]string{g}, files) == 0 {
			idle = append(idle, g)
		}
	}
	if len(idle) > 0 {
		rep.add("docs", Info, "globs matching nothing yet (fine for AI-instruction files not written yet): %s", strings.Join(idle, ", "))
	}
	for i, ru := range cfg.Impact.Rules {
		if countMatching(ru.Code, files) == 0 || countMatching(ru.Docs, files) == 0 {
			rep.add("impact", Warn, "impact.rules[%d] (%s): its code or docs globs match no file, the rule never fires", i, ru.Name)
		}
	}
}

func checkGit(ctx context.Context, rep *Report, o DoctorOptions) {
	if _, err := o.LookPath("git"); err != nil {
		rep.add("git", Fail, "git is not on PATH: every change-time task reads history with it")
		return
	}
	out, err := repo.Git(ctx, o.Root, "rev-parse", "--is-shallow-repository")
	switch {
	case err != nil:
		rep.add("git", Warn, "%s is not a git checkout: the PR and ledger tasks need one", o.Root)
	case strings.TrimSpace(out) == "true":
		rep.add("git", Warn, "a shallow clone: the PR mode diffs against the merge base (actions/checkout with fetch-depth: 0)")
	default:
		rep.add("git", OK, "git found; full history")
	}
}

func checkAI(rep *Report, o DoctorOptions) {
	if o.InstanceFile != "" {
		checkInstance(rep, o)
		return
	}
	inCI := o.Getenv("GITHUB_ACTIONS") == "true"
	set := o.Getenv(o.TokenEnv) != ""
	switch {
	case set:
		rep.add("token", OK, "%s is set (value not shown)", o.TokenEnv)
	case inCI:
		rep.add("token", Warn, "%s is not set in this job: no AI, deterministic checks only (pass secrets.%s as claude-oauth-token; fork PRs never get it)", o.TokenEnv, o.TokenEnv)
	default:
		rep.add("token", Info, "%s is not set here; in CI the action receives it from the claude-oauth-token input", o.TokenEnv)
	}
	if !set {
		return
	}
	if _, err := o.LookPath("claude"); err != nil {
		rep.add("judge", Info, "the judge program `claude` is not on PATH; the CI action installs it, a local run needs it")
		return
	}
	rep.add("judge", OK, "the judge program `claude` is on PATH")
}

// checkInstance verifies the instance configuration's judge: it parses, its program exists and
// the variables it passes on are set.
func checkInstance(rep *Report, o DoctorOptions) {
	in, err := instance.Load(o.InstanceFile)
	if err != nil {
		rep.add("instance", Fail, "%v", err)
		return
	}
	t := in.AI.Judge
	rep.add("instance", OK, "%s parses (judge provider %s)", o.InstanceFile, t.Provider)
	prog, envs := judgeProgram(t)
	if prog != "" {
		if _, err := o.LookPath(prog); err != nil {
			rep.add("judge", Fail, "the judge program %q is not found", prog)
		} else {
			rep.add("judge", OK, "the judge program %q is found", prog)
		}
	}
	for _, e := range envs {
		if o.Getenv(e) == "" {
			rep.add("token", Warn, "%s (passed to the judge) is not set: the judge cannot authenticate", e)
		} else {
			rep.add("token", OK, "%s is set (value not shown)", e)
		}
	}
	if t.Provider != instance.ProviderNone && t.Budget.MaxUSD <= 0 {
		rep.add("budget", Warn, "ai.judge.budget.max_usd is not set: one run is bounded only by the default")
	}
}

// judgeProgram is the program a provider starts and the environment variables it needs.
func judgeProgram(t instance.Task) (string, []string) {
	switch t.Provider {
	case instance.ProviderClaudeCode:
		p := t.ClaudeCode.Program
		if p == "" {
			p = "claude"
		}
		return p, t.ClaudeCode.PassEnv
	case instance.ProviderAnthropic:
		var envs []string
		if t.Anthropic.APIKeyEnv != "" {
			envs = append(envs, t.Anthropic.APIKeyEnv)
		}
		return t.Anthropic.Program, envs
	case instance.ProviderExec:
		if len(t.Exec.Command) > 0 {
			return t.Exec.Command[0], t.Exec.PassEnv
		}
	}
	return "", nil
}
