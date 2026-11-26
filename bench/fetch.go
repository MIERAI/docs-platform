package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Manifest describes the self-contained benchmark data under bench/data: the public
// repositories it is about, each pinned to a full commit SHA, and the sha256 of every data
// file (bench/data/manifest.json).
type Manifest struct {
	Schema string         `json:"schema"`
	Repos  []ManifestRepo `json:"repos"`
	// Files maps a path relative to the manifest's directory to its sha256.
	Files map[string]string `json:"files"`
}

// ManifestRepo is one public repository of the benchmark.
type ManifestRepo struct {
	Name     string `json:"name"`     // logical name (ground truth "repo" without the owner)
	Repo     string `json:"repo"`     // owner/name on GitHub
	Revision string `json:"revision"` // full commit SHA every number was measured at
	Split    string `json:"split"`    // dev or test
}

// ManifestSchema identifies the manifest format.
const ManifestSchema = "docs-platform/bench-data/v1"

var reSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// LoadManifest reads and checks a manifest.
func LoadManifest(file string) (*Manifest, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", file, err)
	}
	if m.Schema != ManifestSchema {
		return nil, fmt.Errorf("manifest %s: schema %q, want %q", file, m.Schema, ManifestSchema)
	}
	for _, r := range m.Repos {
		if !reRepo.MatchString(r.Repo) || !reSHA.MatchString(r.Revision) || r.Name != filepath.Base(r.Repo) {
			return nil, fmt.Errorf("manifest %s: bad repository entry %+v", file, r)
		}
	}
	return &m, nil
}

// addManifest adds the manifest's pins that are not set yet ("" = no manifest).
func (p pins) addManifest(file string) error {
	if file == "" {
		return nil
	}
	m, err := LoadManifest(file)
	if err != nil {
		return err
	}
	for _, r := range m.Repos {
		if _, ok := p[r.Name]; !ok {
			p[r.Name] = r.Revision
		}
	}
	return nil
}

// Pin returns the pinned revision of a repository by logical name.
func (m *Manifest) Pin(name string) (string, bool) {
	for _, r := range m.Repos {
		if r.Name == name {
			return r.Revision, true
		}
	}
	return "", false
}

// VerifyFiles checks every listed file against its sha256; dir is the manifest's directory.
func (m *Manifest) VerifyFiles(dir string) []string {
	var bad []string
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		got, err := fileSHA256(filepath.Join(dir, filepath.FromSlash(n)))
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s: %v", n, err))
		case got != m.Files[n]:
			bad = append(bad, fmt.Sprintf("%s: sha256 %s, manifest says %s", n, got, m.Files[n]))
		}
	}
	return bad
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// runFetch is `bench fetch`: a full clone of every public repository of the manifest under
// -work, checked out (detached) at its pinned revision. Existing clones are reused; a pin
// missing from a clone is fetched first. The clone's HEAD is verified to equal the pin.
func runFetch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	file := fs.String("manifest", "bench/data/manifest.json", "benchmark manifest")
	work := fs.String("work", "", "directory for the clones (one per repository, named after it)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("fetch flags: %w", err)
	}
	if *work == "" {
		return errors.New("fetch: -work is required")
	}
	m, err := LoadManifest(*file)
	if err != nil {
		return err
	}
	if bad := m.VerifyFiles(filepath.Dir(*file)); len(bad) > 0 {
		return fmt.Errorf("fetch: bench data does not match the manifest:\n  %s", strings.Join(bad, "\n  "))
	}
	if err := os.MkdirAll(*work, 0o750); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	for _, r := range m.Repos {
		dir := filepath.Join(*work, r.Name)
		head, err := fetchPinned(ctx, r, dir)
		if err != nil {
			return err
		}
		fmt.Printf("%-22s %s  %s\n", r.Name, head, dir)
	}
	return nil
}

func fetchPinned(ctx context.Context, r ManifestRepo, dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := git(ctx, "", "clone", "--quiet", "https://github.com/"+r.Repo+".git", dir); err != nil {
			return "", err
		}
	}
	if git(ctx, dir, "cat-file", "-e", r.Revision+"^{commit}") != nil {
		if err := git(ctx, dir, "fetch", "--quiet", "origin", r.Revision); err != nil {
			return "", fmt.Errorf("%s: pinned revision %s not found on GitHub: %w", r.Repo, r.Revision, err)
		}
	}
	if err := git(ctx, dir, "checkout", "--quiet", "--detach", r.Revision); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output() //nolint:gosec // argv vector, validated dir
	if err != nil {
		return "", fmt.Errorf("rev-parse: %w", err)
	}
	head := strings.TrimSpace(string(out))
	if head != r.Revision {
		return "", fmt.Errorf("%s: HEAD is %s, want %s", r.Repo, head, r.Revision)
	}
	return head, nil
}
