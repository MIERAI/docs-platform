package instance

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name, doc, wantErr string
	}{
		{"empty", "", ""},
		{"claude-code", "version: 1\nai:\n  judge: {provider: claude-code, model: claude-opus-5-5}\n", ""},
		{"no model", "version: 1\nai:\n  judge: {provider: claude-code}\n", "model is required"},
		{"relative program", "version: 1\nai:\n  judge: {provider: claude-code, model: m, claude_code: {program: claude}}\n", "absolute"},
		{"anthropic needs key", "version: 1\nai:\n  draft: {provider: anthropic, model: m, anthropic: {program: /x}}\n", "exactly one"},
		{"anthropic both keys", "version: 1\nai:\n  draft: {provider: anthropic, model: m, anthropic: {program: /x, api_key_file: /k, api_key_env: K}}\n", "exactly one"},
		{"anthropic ok", "version: 1\nai:\n  draft: {provider: anthropic, model: m, anthropic: {program: /x, api_key_file: /k}}\n", ""},
		{"exec relative", "version: 1\nai:\n  judge: {provider: exec, model: m, exec: {command: [prog]}}\n", "absolute"},
		{"unknown provider", "version: 1\nai:\n  judge: {provider: openai, model: m}\n", "want none"},
		{"bad effort", "version: 1\nai:\n  judge: {provider: claude-code, model: m, effort: huge}\n", "effort"},
		{"bad env", "version: 1\nai:\n  judge: {provider: claude-code, model: m, claude_code: {pass_env: [\"A=B\"]}}\n", "pass_env"},
		{"unknown field", "version: 1\nai:\n  judge: {provider: none, endpoint: x}\n", "not found"},
		{"bad class", "version: 1\nclassification: {default: secret}\n", "classification.default"},
		{"version", "version: 2\n", "version 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.doc))
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestClassOf(t *testing.T) {
	in, err := Parse([]byte(`version: 1
classification:
  repos: {pub: public, priv: private}
  paths:
    - {repo: pub, glob: "secret/**", class: private}
    - {repo: priv, glob: "docs/*.md", class: public}
`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ repo, path, want string }{
		{"pub", "README.md", Public},
		{"pub", "secret/a/b.md", Private},
		{"priv", "docs/x.md", Public},
		{"priv", "docs/sub/x.md", Private},
		{"unknown", "README.md", Private},
	}
	for _, tt := range tests {
		if got := in.Classification.ClassOf(tt.repo, tt.path); got != tt.want {
			t.Errorf("ClassOf(%s, %s) = %s, want %s", tt.repo, tt.path, got, tt.want)
		}
	}
}

func TestPrices(t *testing.T) {
	in, err := Parse([]byte("version: 1\nprices:\n  my-model: {input: 1, output: 2}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := in.PriceOf("my-model")
	if !ok || p.Cost(1_000_000, 1_000_000, 0) != 3 {
		t.Fatalf("custom price %+v", p)
	}
	if _, ok := in.PriceOf("claude-sonnet-5-5"); !ok {
		t.Fatal("builtin price missing")
	}
	if _, ok := in.PriceOf("nope"); ok {
		t.Fatal("unknown model priced")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nai:\n  judge: {provider: claude-code, model: m}\n"))
	f.Add([]byte("classification: {repos: {a: public}}"))
	f.Fuzz(func(t *testing.T, b []byte) {
		in, err := Parse(b)
		if err != nil {
			return
		}
		if c := in.Classification.ClassOf("r", "p"); c != Public && c != Private {
			t.Fatalf("class %q", c)
		}
	})
}

func TestGroupsAndConsistencyValidation(t *testing.T) {
	ok := `version: 1
consistency: {max_age_days: 30, classes: [{name: A, max_age_days: 7, docs: ["README.md"]}], slo: {accuracy: 0.8}}
groups:
  - name: shop
    repos: [{name: api, role: code}, {name: docs, role: docs, path: /srv/docs}]
    doc_sets: [{name: design, repo: docs, docs: ["design/**"], targets: [{repo: api, ref: develop}, {repo: api, ref: prod}]}]
  - name: specs
    repos: [{name: api, role: code}, {name: spec, role: api-spec}]
    doc_sets:
      - name: graphql
        repo: spec
        docs: ["api/graphql/**"]
        targets: [{repo: api, ref: origin/develop}]
        contract: {design_adapter: openapi-graphql, code_adapter: kotlin-graphql, design_ref: origin/main, code_paths: ["svc/**"], authority: code, generator: schema export}
`
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"version: 1\nconsistency: {slo: {accuracy: 1.5}}\n",
		"version: 1\nconsistency: {classes: [{name: A}]}\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: wiki}]}]\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: code, path: rel}]}]\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: docs}], doc_sets: [{name: d, repo: a, docs: [x], targets: [{repo: b, ref: main}]}]}]\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: docs}], doc_sets: [{name: d, repo: a, docs: [x], targets: [{repo: a, ref: '-x'}]}]}]\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: docs}], doc_sets: [{name: d, repo: a, docs: [x], contract: {design_adapter: x}}]}]\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: docs}], doc_sets: [{name: d, repo: a, docs: [x], contract: {design_adapter: x, code_adapter: y, authority: boss}}]}]\n",
		"version: 1\ngroups: [{name: g, repos: [{name: a, role: docs}], doc_sets: [{name: d, repo: a, docs: [x], contract: {design_adapter: x, code_adapter: y, unknown: 1}}]}]\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted:\n%s", bad)
		}
	}
}
