package yamlcfg

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/extract/extracttest"
	"github.com/MIERAI/docs-platform/internal/plugin"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name, path, src string
		want            []string
	}{
		{
			"workflow inputs", ".github/workflows/rel.yml", "on:\n  workflow_dispatch:\n    inputs:\n      version: {}\n  workflow_call:\n    secrets:\n      token: {}\n",
			[]string{"workflow_input token public", "workflow_input version public"},
		},
		{"action inputs", "action.yml", "inputs:\n  repo: {}\n", []string{"workflow_input repo public"}},
		{
			"configmap and env", "k8s/app.yaml", "apiVersion: v1\nkind: ConfigMap\ndata:\n  LOG_LEVEL: info\n---\napiVersion: apps/v1\nkind: Deployment\nspec:\n  template:\n    spec:\n      containers:\n        - env:\n            - name: DB_URL\n",
			[]string{"config_key LOG_LEVEL", "env_var DB_URL"},
		},
		{"recording rule", "rules.yaml", "groups:\n  - rules:\n      - record: job:up:sum\n", []string{"metric job:up:sum"}},
		{
			"helm values are public to depth 1", "chart/values.yaml", "image:\n  tag: x\n  pull:\n    policy: y\n",
			[]string{"config_key image public", "config_key image.pull public", "config_key image.tag public"},
		},
		{"kustomize literals", "kustomization.yaml", "configMapGenerator:\n  - literals:\n      - MODE=prod\n", []string{"config_key MODE"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := filterKind(extracttest.Symbols(t, Extractor{PublicDepth: 1}, c.path, c.src), c.want)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("symbols:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// filterKind drops the generic (non-public) key index entries unless the case expects them.
func filterKind(got, want []string) []string {
	keep := map[string]bool{}
	for _, w := range want {
		keep[w] = true
	}
	out := []string{}
	for _, g := range got {
		if keep[g] || strings.HasSuffix(g, " public") || !strings.HasPrefix(g, "config_key") {
			out = append(out, g)
		}
	}
	return out
}

func TestBrokenYAMLIsAGap(t *testing.T) {
	if _, err := (Extractor{}).Extract(plugin.File{Path: "x.yaml", Content: []byte("a: [\n")}); err == nil {
		t.Error("want an error for unparsable YAML")
	}
}
