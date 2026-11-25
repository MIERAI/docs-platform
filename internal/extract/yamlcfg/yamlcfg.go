// Package yamlcfg is the code-side extractor for YAML: GitHub Actions workflow and action
// inputs, Kubernetes manifests (ConfigMap keys, container env), kustomize generators, Helm
// values, Prometheus recording rules, and the keys of configuration files the instance declares
// public. Every other YAML key is indexed (not public) so documentation that mentions a key that
// exists somewhere resolves.
package yamlcfg

import (
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/yamlkeys"
)

// Name and Version of this extractor.
const (
	Name    = "yaml"
	Version = "0.1.0"
)

// DefaultPublic are config files whose keys are user-facing by convention.
var DefaultPublic = []string{"values.yaml", "*.example.yaml", "*.example.yml", "config.example.*"}

// Extractor is the YAML extractor. Public holds glob patterns of files whose keys are public.
type Extractor struct {
	Public []string
	// PublicDepth is the deepest key level (0 = top level) treated as public.
	PublicDepth int
}

// Info implements plugin.Extractor.
func (Extractor) Info() plugin.Info {
	return plugin.Info{Name: Name, Version: Version, Side: plugin.SideCode}
}

// Accepts implements plugin.Extractor.
func (Extractor) Accepts(p string) bool {
	ext := path.Ext(p)
	return (ext == ".yaml" || ext == ".yml") && !strings.Contains("/"+p, "/testdata/")
}

// Extract implements plugin.Extractor.
func (x Extractor) Extract(f plugin.File) (model.Result, error) {
	docs, err := yamlkeys.Parse(f.Content)
	if err != nil && len(docs) == 0 {
		return model.Result{}, fmt.Errorf("%s: %w", f.Path, err)
	}
	e := &ext{path: f.Path}
	for _, d := range docs {
		x.doc(e, d)
	}
	return model.Result{Symbols: e.syms}, nil
}

func (x Extractor) doc(e *ext, d yamlkeys.Doc) {
	switch {
	case isWorkflow(e.path):
		e.workflow(d.Root)
	case isAction(e.path):
		e.inputs(yamlkeys.Get(d.Root, "inputs"))
	case yamlkeys.Get(d.Root, "apiVersion") != nil && yamlkeys.Get(d.Root, "kind") != nil:
		e.k8s(d.Root)
	case path.Base(e.path) == "kustomization.yaml":
		e.kustomize(d.Root)
	case yamlkeys.Get(d.Root, "groups") != nil:
		e.rules(d.Root)
	}
	public := repo.MatchAny(x.publicPatterns(), e.path)
	for _, k := range d.Keys {
		pub := public && k.Depth <= x.PublicDepth
		conf := model.ConfLow
		if pub {
			conf = model.ConfMedium
		}
		e.add(model.KindConfigKey, k.Path, k.Line, pub, conf)
	}
}

func (x Extractor) publicPatterns() []string {
	if x.Public == nil {
		return DefaultPublic
	}
	return x.Public
}

func isWorkflow(p string) bool { return strings.HasPrefix(p, ".github/workflows/") }

func isAction(p string) bool {
	b := path.Base(p)
	return b == "action.yml" || b == "action.yaml"
}

type ext struct {
	path string
	syms []model.Symbol
}

func (e *ext) add(k model.Kind, name string, line int, public bool, c model.Confidence) {
	e.syms = append(e.syms, model.Symbol{
		Kind: k, Name: name, Path: e.path, Line: line, Public: public, Confidence: c, Source: Name,
	})
}

func (e *ext) workflow(root *yaml.Node) {
	on := yamlkeys.Get(root, "on")
	for _, trig := range []string{"workflow_dispatch", "workflow_call"} {
		t := yamlkeys.Get(on, trig)
		e.inputs(yamlkeys.Get(t, "inputs"))
		for _, k := range yamlkeys.MapKeys(yamlkeys.Get(t, "secrets")) {
			e.add(model.KindWorkflowInput, k.Path, k.Line, true, model.ConfHigh)
		}
	}
}

func (e *ext) inputs(n *yaml.Node) {
	for _, k := range yamlkeys.MapKeys(n) {
		e.add(model.KindWorkflowInput, k.Path, k.Line, true, model.ConfHigh)
	}
}

func (e *ext) k8s(root *yaml.Node) {
	kind := yamlkeys.Get(root, "kind").Value
	if kind == "ConfigMap" {
		for _, k := range yamlkeys.MapKeys(yamlkeys.Get(root, "data")) {
			e.add(model.KindConfigKey, k.Path, k.Line, false, model.ConfMedium)
		}
	}
	walkEnv(root, 0, func(n *yaml.Node) {
		e.add(model.KindEnvVar, n.Value, n.Line, false, model.ConfMedium)
	})
}

// walkEnv calls fn for the name node of every container env entry (any "env" sequence of
// mappings with a "name" key), wherever it is nested.
func walkEnv(n *yaml.Node, depth int, fn func(*yaml.Node)) {
	if n == nil || depth > yamlkeys.MaxDepth {
		return
	}
	if n.Kind == yaml.MappingNode {
		if env := yamlkeys.Get(n, "env"); env != nil && env.Kind == yaml.SequenceNode {
			for _, it := range env.Content {
				if name := yamlkeys.Get(it, "name"); name != nil && name.Kind == yaml.ScalarNode {
					fn(name)
				}
			}
		}
	}
	for _, c := range n.Content {
		walkEnv(c, depth+1, fn)
	}
}

func (e *ext) kustomize(root *yaml.Node) {
	gens := yamlkeys.Get(root, "configMapGenerator")
	if gens == nil {
		return
	}
	for _, g := range gens.Content {
		lits := yamlkeys.Get(g, "literals")
		if lits == nil {
			continue
		}
		for _, l := range lits.Content {
			if k, _, ok := strings.Cut(l.Value, "="); ok {
				e.add(model.KindConfigKey, k, l.Line, false, model.ConfMedium)
			}
		}
	}
}

func (e *ext) rules(root *yaml.Node) {
	groups := yamlkeys.Get(root, "groups")
	if groups == nil || groups.Kind != yaml.SequenceNode {
		return
	}
	for _, g := range groups.Content {
		rules := yamlkeys.Get(g, "rules")
		if rules == nil {
			continue
		}
		for _, r := range rules.Content {
			if rec := yamlkeys.Get(r, "record"); rec != nil {
				e.add(model.KindMetric, rec.Value, rec.Line, false, model.ConfHigh)
			}
		}
	}
}
