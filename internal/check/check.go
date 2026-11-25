// Package check wires one `docsp check` run: open the repository read-only, run the extractor
// plugins, match (layer ③), run the residue check, and build a findings/v1 document.
package check

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/extract/comments"
	"github.com/MIERAI/docs-platform/internal/extract/golang"
	"github.com/MIERAI/docs-platform/internal/extract/markdown"
	"github.com/MIERAI/docs-platform/internal/extract/misc"
	"github.com/MIERAI/docs-platform/internal/extract/python"
	"github.com/MIERAI/docs-platform/internal/extract/shell"
	"github.com/MIERAI/docs-platform/internal/extract/yamlcfg"
	"github.com/MIERAI/docs-platform/internal/findings"
	"github.com/MIERAI/docs-platform/internal/match"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/residue"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/internal/secret"
)

// Options of one run.
type Options struct {
	Root          string
	ConfigFile    string
	RepoName      string
	Instance      string
	Version       string
	MinConfidence string // overrides the config when set
	DenylistFile  string
	Now           func() time.Time
}

// Result of one run.
type Result struct {
	Document  *findings.Document
	Report    *match.Report
	Inventory *scan.Inventory
	Config    *config.Config
}

// Builtins returns the built-in extractors configured from cfg.
func Builtins(cfg *config.Config) []plugin.Extractor {
	return []plugin.Extractor{
		markdown.Extractor{},
		comments.Extractor{},
		golang.Extractor{},
		python.Extractor{},
		shell.Extractor{},
		yamlcfg.Extractor{Public: publicOrDefault(cfg.PublicConfig), PublicDepth: cfg.PublicDepth},
		misc.Make{},
		misc.Terraform{},
		misc.JavaScript{},
	}
}

func publicOrDefault(p []string) []string {
	if len(p) == 0 {
		return nil
	}
	return p
}

// Run performs a check.
func Run(ctx context.Context, o Options) (*Result, error) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	cfg, err := config.Load(o.Root, o.ConfigFile)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	minConf, err := minConfidence(cfg, o.MinConfidence)
	if err != nil {
		return nil, err
	}
	r, err := repo.Open(o.Root, cfg.Exclude)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	exts, err := extractors(cfg)
	if err != nil {
		return nil, err
	}
	inv := scan.Run(r, exts, scan.Options{Docs: cfg.Docs})
	tools := plugin.Versions(exts)
	if err := external(ctx, cfg, r.Root, inv, tools); err != nil {
		return nil, err
	}
	rep := match.Run(inv, r, match.Options{
		MinConfidence: minConf, Ignore: cfg.Ignore, Reverse: cfg.Reverse, GitIgnore: gitignore(r),
		RelatedRepos: cfg.RelatedRepos,
	})
	if err := residueCheck(o.DenylistFile, r, inv, rep, tools); err != nil {
		return nil, err
	}
	doc := findings.Build(findings.Params{
		Repo: repoName(o), Version: o.Version, Tools: tools, Started: findings.Timestamp(started),
		Finished: findings.Timestamp(now()), Instance: o.Instance, Owner: cfg.Owner,
		Gaps: gaps(inv.Gaps), CodeAsset: !cfg.Reverse.Disabled,
	}, rep)
	return &Result{Document: doc, Report: rep, Inventory: inv, Config: cfg}, nil
}

func minConfidence(cfg *config.Config, override string) (model.Confidence, error) {
	s := cfg.MinConfidence
	if override != "" {
		s = override
	}
	c, ok := model.ParseConfidence(s)
	if !ok {
		return 0, fmt.Errorf("min confidence %q: want low, medium or high", s)
	}
	return c, nil
}

func extractors(cfg *config.Config) ([]plugin.Extractor, error) {
	reg, err := plugin.NewRegistry(Builtins(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("extractors: %w", err)
	}
	disabled := map[string]bool{}
	for _, d := range cfg.Extractors.Disable {
		disabled[d] = true
	}
	return reg.Enabled(disabled), nil
}

func external(ctx context.Context, cfg *config.Config, root string, inv *scan.Inventory, tools map[string]string) error {
	for _, e := range cfg.Extractors.External {
		out, err := plugin.RunExternal(ctx, e.Command, root)
		if err != nil {
			return fmt.Errorf("external extractor %s: %w", e.Name, err)
		}
		inv.Merge(out.Result)
		tools["extract-"+e.Name] = out.Extractor.Version
	}
	return nil
}

// gitignore collects the patterns of every .gitignore, rewritten relative to the repo root.
func gitignore(r *repo.Repo) []string {
	var pats []string
	for _, f := range r.ByBase(".gitignore") {
		b, err := r.Read(f)
		if err != nil {
			continue
		}
		dir := path.Dir(f)
		for _, l := range secret.Lines(b) {
			if l[0] == '!' {
				continue
			}
			pats = append(pats, rootRelative(dir, l))
		}
	}
	return pats
}

func rootRelative(dir, pat string) string {
	if dir == "." {
		return pat
	}
	if !strings.Contains(strings.TrimSuffix(pat, "/"), "/") {
		return dir + "/**/" + pat
	}
	return dir + "/" + strings.TrimPrefix(pat, "/")
}

func residueCheck(file string, r *repo.Repo, inv *scan.Inventory, rep *match.Report, tools map[string]string) error {
	if file == "" {
		return nil
	}
	b, err := secret.ReadFile(file)
	if err != nil {
		return fmt.Errorf("denylist: %w", err)
	}
	c := residue.New(secret.Lines(b))
	tools["residue"] = residue.Version
	for _, f := range append(append([]string{}, inv.DocFiles...), inv.CodeFiles...) {
		content, err := r.Read(f)
		if err != nil {
			continue
		}
		rep.Issues = append(rep.Issues, c.Check(f, content)...)
	}
	return nil
}

func repoName(o Options) string {
	if o.RepoName != "" {
		return o.RepoName
	}
	abs, err := filepath.Abs(o.Root)
	if err != nil {
		return filepath.Base(o.Root)
	}
	return filepath.Base(abs)
}

func gaps(g []repo.Gap) []findings.Gap {
	out := make([]findings.Gap, 0, len(g))
	for _, x := range g {
		out = append(out, findings.Gap{Path: x.Path, Reason: x.Reason})
	}
	return out
}
