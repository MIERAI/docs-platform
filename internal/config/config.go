// Package config loads the per-repository configuration file .docsp.yaml (contract
// docsp/config/v1, see contracts/config-v1.schema.json). The file is optional; every field has a
// safe default. Unknown fields are an error so typos never silently disable a check.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
)

// FileName is the configuration file looked up at the repository root.
const FileName = ".docsp.yaml"

// Config is docsp/config/v1.
type Config struct {
	Version       int          `yaml:"version"`
	Docs          []string     `yaml:"docs"`
	Exclude       []string     `yaml:"exclude"`
	PublicConfig  []string     `yaml:"public_config"`
	PublicDepth   int          `yaml:"public_depth"`
	MinConfidence string       `yaml:"min_confidence"`
	Owner         string       `yaml:"owner"`
	Extractors    Extractors   `yaml:"extractors"`
	Ignore        []IgnoreRule `yaml:"ignore"`
	Impact        Impact       `yaml:"impact"`
	Reverse       Reverse      `yaml:"reverse"`
	// RelatedRepos are names of other repositories; a path written right after one of them
	// ("gitops-manifests scripts/x.py") is that repository's path, not this one's.
	RelatedRepos []string `yaml:"related_repos"`
	// Audit configures layer ④(c), the semantic audit (`docsp audit`).
	Audit Audit `yaml:"audit"`
}

// Audit configures the semantic audit.
type Audit struct {
	// StaleCommits is how many later commits to a claim's code make the claim "stale".
	StaleCommits int `yaml:"stale_commits"`
	// AI is rejected: providers, models and budgets belong to the instance configuration
	// (docs-platform/instance/v1), never to a repository.
	AI any `yaml:"ai"`
	// Skip keeps noise away from the judge: claims it matches are not judged, and each skip
	// is counted by reason.
	Skip Skip `yaml:"skip"`
}

// Skip selects the claims not put to the judge. Every field defaults to on.
type Skip struct {
	// Historical: sentences about the past ("previously", "used to", "before v2", 以前, 曾经,
	// 已废弃, 以前は, 廃止された).
	Historical *bool `yaml:"historical"`
	// Changelogs: changelog-type documents (globs; unset = claim.DefaultChangelogs, [] = none).
	Changelogs []string `yaml:"changelogs"`
	// Tests: comments in test files and claims that name only test files.
	Tests *bool `yaml:"tests"`
	// PrivateSymbols: comments on unexported / private declarations and claims that name only
	// such symbols.
	PrivateSymbols *bool `yaml:"private_symbols"`
}

// Filter is the claim filter the configuration selects.
func (s Skip) Filter() claim.Filter {
	on := func(b *bool) bool { return b == nil || *b }
	f := claim.Filter{Historical: on(s.Historical), Changelogs: s.Changelogs, Tests: on(s.Tests), Private: on(s.PrivateSymbols)}
	if s.Changelogs == nil {
		f.Changelogs = claim.DefaultChangelogs
	}
	return f
}

// Extractors enables/disables plugins and declares external ones.
type Extractors struct {
	Disable  []string   `yaml:"disable"`
	External []External `yaml:"external"`
}

// External is an out-of-process extractor speaking docsp/extract/v1 on stdout.
type External struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
}

// IgnoreRule suppresses matching findings. Reason is mandatory.
type IgnoreRule struct {
	Kind   string `yaml:"kind"`
	Text   string `yaml:"text"`
	Path   string `yaml:"path"`
	Check  string `yaml:"check"`
	Reason string `yaml:"reason"`
}

// Impact configures `docsp impact`.
type Impact struct {
	Rules []ImpactRule `yaml:"rules"`
	// Trailer is the commit-message/PR-body key that carries an explicit no-docs reason.
	Trailer string `yaml:"trailer"`
}

// ImpactRule maps code paths to the docs that describe them.
type ImpactRule struct {
	Name string   `yaml:"name"`
	Code []string `yaml:"code"`
	Docs []string `yaml:"docs"`
}

// Reverse configures the undocumented-symbol check.
type Reverse struct {
	// Kinds limits which public symbol kinds must be documented (default: all).
	Kinds []string `yaml:"kinds"`
	// Disabled turns the reverse check off.
	Disabled bool `yaml:"disabled"`
}

// Defaults returns the configuration used when no file exists.
func Defaults() *Config {
	return &Config{
		Version:       1,
		Docs:          []string{"*.md", "*.markdown"},
		Exclude:       []string{"CHANGELOG.md", "CHANGELOG*.md", "testdata/", "third_party/", "LICENSE*"},
		PublicDepth:   1,
		MinConfidence: "medium",
		Owner:         "unassigned",
		Impact:        Impact{Trailer: "Docs-Not-Needed"},
		Audit:         Audit{StaleCommits: 2},
	}
}

// Load reads root/.docsp.yaml (or explicit file); a missing default file yields Defaults().
func Load(root, explicit string) (*Config, error) {
	p := explicit
	if p == "" {
		p = filepath.Join(root, FileName)
	}
	b, err := readSmall(p)
	if errors.Is(err, fs.ErrNotExist) && explicit == "" {
		return Defaults(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(b)
}

func readSmall(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err //nolint:wrapcheck // caller wraps
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return b, nil
}

// Parse decodes and validates a configuration document, filling defaults.
func Parse(b []byte) (*Config, error) {
	c := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks semantic constraints.
func (c *Config) Validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("unsupported config version %d (this docsp reads version 1)", c.Version))
	}
	if _, ok := model.ParseConfidence(c.MinConfidence); !ok {
		errs = append(errs, fmt.Errorf("min_confidence %q: want low, medium or high", c.MinConfidence))
	}
	for i, r := range c.Ignore {
		if strings.TrimSpace(r.Reason) == "" {
			errs = append(errs, fmt.Errorf("ignore[%d]: reason is required", i))
		}
		if r.Text == "" && r.Path == "" && r.Check == "" {
			errs = append(errs, fmt.Errorf("ignore[%d]: needs at least one of text, path, check", i))
		}
	}
	errs = append(errs, c.validateGlobs()...)
	errs = append(errs, c.Audit.validate()...)
	for i, e := range c.Extractors.External {
		if e.Name == "" || len(e.Command) == 0 || !filepath.IsAbs(e.Command[0]) {
			errs = append(errs, fmt.Errorf("extractors.external[%d]: name and an absolute command path are required", i))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) validateGlobs() []error {
	var errs []error
	check := func(where string, pats []string) {
		for _, p := range pats {
			for _, seg := range strings.Split(p, "/") {
				if _, err := path.Match(seg, ""); err != nil {
					errs = append(errs, fmt.Errorf("%s: bad glob %q", where, p))
				}
			}
		}
	}
	check("docs", c.Docs)
	check("exclude", c.Exclude)
	check("public_config", c.PublicConfig)
	check("audit.skip.changelogs", c.Audit.Skip.Changelogs)
	for i, r := range c.Impact.Rules {
		if len(r.Code) == 0 || len(r.Docs) == 0 {
			errs = append(errs, fmt.Errorf("impact.rules[%d]: code and docs are required", i))
		}
		check(fmt.Sprintf("impact.rules[%d].code", i), r.Code)
		check(fmt.Sprintf("impact.rules[%d].docs", i), r.Docs)
	}
	return errs
}

func (a Audit) validate() []error {
	var errs []error
	if a.AI != nil {
		errs = append(errs, errors.New("audit.ai: model providers are configured in the instance configuration (docsp ... -instance FILE), not in a repository"))
	}
	if a.StaleCommits < 1 {
		errs = append(errs, errors.New("audit.stale_commits must be at least 1"))
	}
	return errs
}
