package related

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/repo"
)

func lines(s string) []string { return strings.Split(s, "\n") }

// docs: a README with an install, a configuration and a retry section, and a guide.
func docs() map[string][]string {
	return map[string][]string{
		"README.md": lines(`# demo

## Install

Run make install.

## Configuration

Set DEMO_TIMEOUT to bound each request; --max-retries sets how often a failed upload is retried.

## Contributing

Open a pull request.`),
		"docs/guide.md": lines(`# Guide

## Uploads

The uploader in internal/upload/client.go streams chunks; maxChunkSize caps a chunk.

## Glossary

A request is one upload.`),
	}
}

func diff(path string, added ...string) repo.FileDiff {
	d := repo.FileDiff{Path: path, OldPath: path}
	for i, a := range added {
		d.Added = append(d.Added, repo.DiffLine{N: i + 1, Text: a})
	}
	return d
}

func find(rep *Report, p string, from int) *Section {
	for i := range rep.Sections {
		if rep.Sections[i].Path == p && rep.Sections[i].From == from {
			return &rep.Sections[i]
		}
	}
	return nil
}

func TestSelectIdentifierOverlap(t *testing.T) {
	rep := Select(Input{Docs: docs(), Diffs: []repo.FileDiff{
		diff("internal/upload/client.go", `	maxChunkSize := envInt("DEMO_TIMEOUT", 30)`, `	retries := flag.Int("max-retries", 3, "")`),
	}})
	cfg := find(rep, "README.md", 7)
	if cfg == nil || cfg.Reason != ReasonOverlap || cfg.Heading != "Configuration" {
		t.Fatalf("configuration section not selected: %+v", rep.Sections)
	}
	for _, want := range []string{"demo_timeout", "max-retries"} {
		if !contains(cfg.Terms, want) {
			t.Errorf("terms %v lack %s", cfg.Terms, want)
		}
	}
	up := find(rep, "docs/guide.md", 3)
	if up == nil || !contains(up.Terms, "internal/upload/client.go") || !contains(up.Terms, "maxchunksize") {
		t.Fatalf("uploads section: %+v", up)
	}
	if find(rep, "README.md", 3) != nil || find(rep, "README.md", 11) != nil || find(rep, "docs/guide.md", 7) != nil {
		t.Errorf("unrelated sections selected: %+v", rep.Sections)
	}
	if rep.Docs != 2 || rep.Considered != 7 {
		t.Errorf("docs %d, considered %d", rep.Docs, rep.Considered)
	}
}

func TestSelectChangedDocFirst(t *testing.T) {
	d := diff("README.md")
	d.Added = []repo.DiffLine{{N: 13, Text: "Open a pull request."}}
	rep := Select(Input{Docs: docs(), Diffs: []repo.FileDiff{
		diff("internal/upload/client.go", `maxChunkSize := 1`),
		d,
	}})
	if len(rep.Sections) == 0 || rep.Sections[0].Reason != ReasonChanged || rep.Sections[0].Heading != "Contributing" {
		t.Fatalf("the edited section is not first: %+v", rep.Sections)
	}
}

func TestSelectPlainWordsNeedThree(t *testing.T) {
	// "chunks" and "streams" are plain words: two are not enough on their own.
	rep := Select(Input{Docs: docs(), Diffs: []repo.FileDiff{diff("x.go", "// streams chunks")}})
	if len(rep.Sections) != 0 {
		t.Errorf("two plain words selected %+v", rep.Sections)
	}
	rep = Select(Input{Docs: docs(), Diffs: []repo.FileDiff{diff("x.go", "// streams chunks uploader")}})
	if s := find(rep, "docs/guide.md", 3); s == nil || s.Score != 3 {
		t.Errorf("three plain words: %+v", rep.Sections)
	}
}

func TestSelectIgnoresCommonTermsNoiseAndStopWords(t *testing.T) {
	ds := map[string][]string{}
	for _, p := range []string{"a.md", "b.md", "c.md", "d.md"} {
		ds[p] = lines("# " + p + "\n\nThe http_client handles every request.")
	}
	rep := Select(Input{Docs: ds, Diffs: []repo.FileDiff{
		diff("client.go", "http_client := new()"), // in every section: says nothing
		diff("go.sum", "example.com/http_client v1.0.0 h1:abc"),
		diff("x.go", "return function default"),
	}})
	if len(rep.Sections) != 0 {
		t.Errorf("selected %+v", rep.Sections)
	}
}

func TestSelectCapAndDeterminism(t *testing.T) {
	ds := map[string][]string{}
	for _, p := range []string{"a.md", "b.md", "c.md", "d.md", "e.md", "f.md", "g.md", "h.md", "i.md", "j.md", "k.md", "l.md"} {
		ds[p] = lines("# " + p + "\n\nunrelated words here\n\n## Tuning\n\nraise " + strings.TrimSuffix(p, ".md") + "_limit_value carefully")
	}
	var diffs []repo.FileDiff
	for _, p := range []string{"a", "b", "c", "d", "e"} {
		diffs = append(diffs, diff(p+".go", p+"_limit_value = 3"))
	}
	r1 := Select(Input{Docs: ds, Diffs: diffs, Max: 3})
	r2 := Select(Input{Docs: ds, Diffs: diffs, Max: 3})
	if !reflect.DeepEqual(r1, r2) {
		t.Fatal("not deterministic")
	}
	if len(r1.Sections) != 3 || r1.Dropped != 2 {
		t.Fatalf("cap: %d sections, %d dropped", len(r1.Sections), r1.Dropped)
	}
	if r1.Sections[0].Path != "a.md" || r1.Sections[2].Path != "c.md" {
		t.Errorf("order: %+v", r1.Sections)
	}
}

func TestSelectReferenceSections(t *testing.T) {
	refs := []impact.SectionResult{
		{Section: impact.Section{Path: "README.md", Heading: "Install", From: 3, To: 6}, Reasons: []impact.Reason{{Kind: "path", Subject: "Makefile"}}},
		{Section: impact.Section{Path: "cmd/x.go", Heading: "comment", From: 1, To: 2}}, // not a doc section
	}
	rep := Select(Input{Docs: docs(), References: refs})
	if len(rep.Sections) != 1 || rep.Sections[0].Reason != ReasonReference || rep.Sections[0].Terms[0] != "Makefile" {
		t.Fatalf("references: %+v", rep.Sections)
	}
}

func TestContainsAndLoad(t *testing.T) {
	rep := &Report{SchemaVersion: SchemaVersion, Sections: []Section{{Path: "README.md", From: 3, To: 6}}}
	if !rep.Contains("README.md", 3) || !rep.Contains("README.md", 6) || rep.Contains("README.md", 7) || rep.Contains("x.md", 4) {
		t.Error("Contains")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	b, _ := json.Marshal(rep)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || !got.Contains("README.md", 4) {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if err := os.WriteFile(p, []byte(`{"schema_version":"2.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("schema 2.0 accepted")
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file accepted")
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
