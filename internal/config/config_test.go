package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name, src, wantErr string
	}{
		{"empty uses defaults", "", ""},
		{"minimal", "version: 1\n", ""},
		{"full", "version: 1\ndocs: ['**/*.md']\nmin_confidence: high\nimpact:\n  rules:\n    - name: cli\n      code: [cmd/**]\n      docs: [README.md]\n", ""},
		{"unknown field", "version: 1\ndocz: []\n", "field docz not found"},
		{"bad version", "version: 2\n", "unsupported config version 2"},
		{"bad confidence", "version: 1\nmin_confidence: sure\n", "min_confidence"},
		{"ignore without reason", "version: 1\nignore:\n  - text: x\n", "reason is required"},
		{"ignore without selector", "version: 1\nignore:\n  - reason: r\n", "needs at least one"},
		{"bad glob", "version: 1\ndocs: ['[']\n", "bad glob"},
		{"rule without docs", "version: 1\nimpact:\n  rules:\n    - name: x\n      code: [a]\n", "code and docs are required"},
		{"audit ollama", "version: 1\naudit:\n  stale_commits: 3\n  ai:\n    provider: ollama\n    endpoint: http://127.0.0.1:11434\n    model: m\n    max_concurrency: 2\n", ""},
		{"audit ollama without endpoint", "version: 1\naudit:\n  ai:\n    provider: ollama\n    model: m\n", "needs endpoint and model"},
		{"audit cloud provider", "version: 1\naudit:\n  ai:\n    provider: openai\n", "only local models"},
		{"audit stale_commits", "version: 1\naudit:\n  stale_commits: 0\n", "stale_commits"},
		{"audit negative limits", "version: 1\naudit:\n  ai:\n    max_requests: -1\n", "must not be negative"},
		{"relative external command", "version: 1\nextractors:\n  external:\n    - name: x\n      command: [rel/bin]\n", "absolute command path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := Parse([]byte(c.src))
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.Version != 1 || cfg.Impact.Trailer == "" {
					t.Errorf("defaults not applied: %+v", cfg)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir, "")
	if err != nil || cfg.MinConfidence != "medium" {
		t.Fatalf("missing default file: cfg=%+v err=%v", cfg, err)
	}
	if _, err := Load(dir, filepath.Join(dir, "nope.yaml")); err == nil {
		t.Error("an explicit missing file must be an error")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("version: 1\nowner: docs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(dir, ""); err != nil || cfg.Owner != "docs" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nignore:\n  - text: a\n    reason: b\n"))
	f.Add([]byte("version: 1\nimpact: {rules: [{name: a, code: ['**'], docs: ['x']}]}\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		cfg, err := Parse(b)
		if err == nil && cfg.Validate() != nil {
			t.Fatal("Parse accepted a config that does not validate")
		}
	})
}
