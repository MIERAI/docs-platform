// Package repo gives read-only access to an inspected repository: a file listing that honours
// exclusions, bounded file reads and a few read-only git queries. Nothing here ever writes into
// the inspected repository.
package repo

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MaxFileSize bounds how much of a single file is read; larger files are skipped (a coverage gap).
const MaxFileSize = 2 << 20

// Repo is an opened repository root.
type Repo struct {
	Root  string
	files []string // inspected files (exclusions applied)
	all   []string // every file, for existence checks
	dirs  map[string]bool
	// dirBase holds the base names of all directories.
	dirBase map[string]bool
	base    map[string][]string
	Gaps    []Gap
}

// Gap records a file that could not be inspected.
type Gap struct {
	Path   string
	Reason string
}

// alwaysSkip are directory names never walked: VCS data and vendored/generated trees.
var alwaysSkip = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".terraform": true}

// Open walks root (read-only) and indexes its files. exclude holds glob patterns (see Match).
func Open(root string, exclude []string) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("repo root %s is not a directory", abs)
	}
	r := &Repo{Root: abs, dirs: map[string]bool{".": true}, dirBase: map[string]bool{}, base: map[string][]string{}}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		return r.visit(p, d, werr, exclude)
	})
	if err != nil {
		return nil, fmt.Errorf("walk repo: %w", err)
	}
	sort.Strings(r.files)
	sort.Strings(r.all)
	return r, nil
}

// excludedDir reports whether any parent directory of rel matches an exclusion.
func excludedDir(exclude []string, rel string) bool {
	for d := path.Dir(rel); d != "."; d = path.Dir(d) {
		if MatchAny(exclude, d+"/") || MatchAny(exclude, d) {
			return true
		}
	}
	return false
}

func (r *Repo) visit(p string, d fs.DirEntry, werr error, exclude []string) error {
	rel, err := filepath.Rel(r.Root, p)
	if err != nil {
		return fmt.Errorf("relative path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if werr != nil {
		r.Gaps = append(r.Gaps, Gap{Path: rel, Reason: werr.Error()})
		if d != nil && d.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if rel == "." {
		return nil
	}
	if d.IsDir() {
		if alwaysSkip[d.Name()] {
			return fs.SkipDir
		}
		r.dirs[rel] = true
		r.dirBase[d.Name()] = true
		return nil
	}
	if !d.Type().IsRegular() {
		return nil
	}
	r.all = append(r.all, rel)
	b := path.Base(rel)
	r.base[b] = append(r.base[b], rel)
	if !MatchAny(exclude, rel) && !excludedDir(exclude, rel) {
		r.files = append(r.files, rel)
	}
	return nil
}

// Files returns the repo-relative paths to inspect (exclusions applied), sorted.
func (r *Repo) Files() []string { return r.files }

// AllFiles returns every file in the checkout (exclusions not applied), sorted.
func (r *Repo) AllFiles() []string { return r.all }

// IsFile reports whether rel is a file in the checkout (excluded or not).
func (r *Repo) IsFile(rel string) bool {
	i := sort.SearchStrings(r.all, rel)
	return i < len(r.all) && r.all[i] == rel
}

// IsDir reports whether rel is a walked directory.
func (r *Repo) IsDir(rel string) bool { return r.dirs[strings.TrimSuffix(rel, "/")] }

// Exists reports whether rel names a file or a directory.
func (r *Repo) Exists(rel string) bool { return r.IsFile(rel) || r.IsDir(rel) }

// ByBase returns files whose base name is b.
func (r *Repo) ByBase(b string) []string { return r.base[b] }

// HasDirNamed reports whether any directory has base name b.
func (r *Repo) HasDirNamed(b string) bool { return r.dirBase[b] }

// TopLevel reports whether name is an entry directly under the root.
func (r *Repo) TopLevel(name string) bool { return r.Exists(name) }

// ErrTooLarge marks a file over MaxFileSize.
var ErrTooLarge = errors.New("file too large")

// Read returns the content of a repo-relative file, refusing paths that escape the root.
func (r *Repo) Read(rel string) ([]byte, error) {
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return nil, fmt.Errorf("path %q escapes the repository", rel)
	}
	root, err := os.OpenRoot(r.Root)
	if err != nil {
		return nil, fmt.Errorf("open root: %w", err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.FromSlash(clean))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	if len(b) > MaxFileSize {
		return nil, ErrTooLarge
	}
	return b, nil
}
