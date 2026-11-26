package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/MIERAI/docs-platform/internal/residue"
	"github.com/MIERAI/docs-platform/internal/secret"
)

const dataDir = "data"

// TestBenchDataManifest: every data file is listed in the manifest with its sha256, and the
// manifest pins each public repository to a full commit SHA.
func TestBenchDataManifest(t *testing.T) {
	m, err := LoadManifest(filepath.Join(dataDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repos) != 3 {
		t.Errorf("repos %+v", m.Repos)
	}
	if bad := m.VerifyFiles(dataDir); len(bad) > 0 {
		t.Errorf("data does not match the manifest:\n%s", strings.Join(bad, "\n"))
	}
	for _, f := range dataFiles(t) {
		if _, ok := m.Files[f]; !ok && f != "manifest.json" {
			t.Errorf("%s is not listed in manifest.json", f)
		}
	}
}

// privatePatterns must never appear in the benchmark data: it holds only material about the
// public repositories (their ground truth, eval items, frozen model answers about them).
// Host names and addresses that the public repositories themselves contain are not private.
var privatePatterns = map[string]*regexp.Regexp{
	"local absolute path": regexp.MustCompile(`(?:^|[\s"'(=:])/(?:Users|home|private|var/folders|root)/[A-Za-z0-9._-]+`),
	"windows user path":   regexp.MustCompile(`(?i)[a-z]:\\\\?users\\\\?`),
	"e-mail address":      regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`),
	"anthropic key":       regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`),
	"github token":        regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
	"aws access key":      regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	"google api key":      regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	"slack token":         regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	"private key":         regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	"sops ciphertext":     regexp.MustCompile(`ENC\[AES256_GCM`),
	"jwt":                 regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.`),
}

// TestBenchDataPrivacy scans every data file for private material. With DOCSP_DENYLIST
// pointing to a private term list (mode 0600, one term per line, the same file `docsp check
// -denylist` reads) its terms are checked too.
func TestBenchDataPrivacy(t *testing.T) {
	var deny *residue.Checker
	if f := os.Getenv("DOCSP_DENYLIST"); f != "" {
		b, err := secret.ReadFile(f)
		if err != nil {
			t.Fatalf("DOCSP_DENYLIST: %v", err)
		}
		deny = residue.New(secret.Lines(b))
	}
	for _, f := range dataFiles(t) {
		b, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(privatePatterns))
		for n := range privatePatterns {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if m := privatePatterns[n].Find(b); m != nil {
				t.Errorf("%s: %s %q", f, n, m)
			}
		}
		for _, is := range deny.Check(f, b) {
			t.Errorf("%s: denylisted term %q (%d places)", f, is.Subject, len(is.Locations))
		}
	}
}

func TestPrivatePatternsMatch(t *testing.T) {
	for n, s := range map[string]string{
		"local absolute path": `see "/Users/someone/x"`, "e-mail address": "a.b@example.org",
		"anthropic key": "sk-ant-api03-abcdefgh", "private key": "-----BEGIN OPENSSH PRIVATE KEY-----",
		"sops ciphertext": "ENC[AES256_GCM,data:x]", "windows user path": `C:\Users\x`,
	} {
		if !privatePatterns[n].MatchString(s) {
			t.Errorf("%s does not match %q", n, s)
		}
	}
	for _, s := range []string{"~/.kube/config", "factory@127.0.0.1", "k3s-ansible@1.2.2", "platform/home/x"} {
		for n, re := range privatePatterns {
			if re.MatchString(s) {
				t.Errorf("%s matches harmless %q", n, s)
			}
		}
	}
}

func dataFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dataDir, p)
		out = append(out, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
