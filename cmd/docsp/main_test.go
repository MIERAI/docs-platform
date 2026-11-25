package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "../../testdata/fixtures/basic"

func TestRunCommands(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		code     int
		inStdout string
	}{
		{"no args", nil, exitUsage, ""},
		{"unknown", []string{"frobnicate"}, exitUsage, ""},
		{"version", []string{"version"}, exitOK, "docsp " + version},
		{"help", []string{"help"}, exitOK, "usage: docsp"},
		{"check json", []string{"check", fixture}, exitOK, `"schema_version": "1.3"`},
		{"check text", []string{"check", "-format", "text", fixture}, exitOK, "docs.reference.unresolved.file_path"},
		{"check fail-on", []string{"check", "-format", "none", "-fail-on", "medium", fixture}, exitFindings, ""},
		{"check fail-on high passes", []string{"check", "-format", "none", "-fail-on", "high", fixture}, exitOK, ""},
		{"check bad fail-on", []string{"check", "-fail-on", "loud", fixture}, exitUsage, ""},
		{"check bad confidence", []string{"check", "-min-confidence", "sure", fixture}, exitUsage, ""},
		{"check missing repo", []string{"check", "/nonexistent/repo"}, exitUsage, ""},
		{"impact needs base", []string{"impact", fixture}, exitUsage, ""},
		{"extract", []string{"extract", fixture}, exitOK, `"kind": "cli_flag"`},
		{"validate golden", []string{"validate", "../../testdata/golden/basic-low.findings.json"}, exitOK, "valid findings/v1"},
		{"check help", []string{"check", "-h"}, exitOK, ""},
		{"audit none", []string{"audit", "-ai", "none", fixture}, exitOK, `"name": "docs-audit"`},
		{"audit cloud refused", []string{"audit", "-ai", "openai", fixture}, exitUsage, ""},
		{"audit ollama needs endpoint", []string{"audit", "-ai", "ollama", fixture}, exitUsage, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(context.Background(), c.args, &out, &errb)
			if code != c.code {
				t.Fatalf("exit = %d, want %d (stderr %s)", code, c.code, errb.String())
			}
			if !strings.Contains(out.String(), c.inStdout) {
				t.Errorf("stdout lacks %q:\n%s", c.inStdout, out.String())
			}
		})
	}
}

func TestCheckOutFileIsPrivate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "f.json")
	var so, se bytes.Buffer
	if code := run(context.Background(), []string{"check", "-out", out, fixture}, &so, &se); code != exitOK {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	st, err := os.Stat(out)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", st.Mode(), err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil || so.Len() != 0 {
		t.Errorf("out file invalid (%v) or JSON also on stdout", err)
	}
	if !strings.Contains(se.String(), "docsp:") {
		t.Errorf("summary line missing on stderr: %q", se.String())
	}
}
