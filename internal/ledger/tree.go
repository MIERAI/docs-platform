package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Tree is the file tree of one commit: path → blob.
type Tree struct {
	Ref   string
	Blobs map[string]string
	segs  map[string]bool // every path segment (file and directory names)
	dirs  map[string]bool
}

// ReadTree snapshots the tree of ref in dir (git ls-tree, read-only).
func ReadTree(ctx context.Context, dir, ref string) (*Tree, error) {
	sha, err := Resolve(ctx, dir, ref)
	if err != nil {
		return nil, err
	}
	out, err := repo.Git(ctx, dir, "ls-tree", "-r", "-z", "--full-tree", sha)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	t := &Tree{Ref: sha, Blobs: map[string]string{}, segs: map[string]bool{}, dirs: map[string]bool{}}
	for _, rec := range strings.Split(out, "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[1] != "blob" {
			continue
		}
		t.Blobs[p] = f[2]
		for _, s := range strings.Split(p, "/") {
			t.segs[s] = true
		}
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			t.dirs[d] = true
		}
	}
	return t, nil
}

// Resolve returns the full commit SHA of ref.
func Resolve(ctx context.Context, dir, ref string) (string, error) {
	if !repo.ValidRef(ref) {
		return "", fmt.Errorf("ledger: invalid revision %q", ref)
	}
	out, err := repo.Git(ctx, dir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Exists reports whether p is a file or a directory (a prefix of a file) in the tree.
func (t *Tree) Exists(p string) bool {
	_, ok := t.Blobs[p]
	return ok || t.dirs[strings.TrimSuffix(p, "/")]
}

// HasName reports whether any file or directory is named n.
func (t *Tree) HasName(n string) bool { return t.segs[n] }

// ScopeHash is the identity of the files an absence search covered: the tracked files the
// scope glob selects, minus the skipped document, by path and blob.
func (t *Tree) ScopeHash(scope, skip string) (string, int, error) {
	m, err := judge.CompileScope(scope)
	if err != nil {
		return "", 0, fmt.Errorf("ledger: %w", err)
	}
	var keys []string
	for p, b := range t.Blobs {
		if p != skip && m(p) {
			keys = append(keys, p+"\x00"+b)
		}
	}
	sort.Strings(keys)
	h := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(h[:])[:16], len(keys), nil
}

// Changes are the paths that differ between two commits.
type Changes struct {
	From, To string
	// Status by path: A (added), D (deleted), M (modified, including type changes).
	Paths map[string]byte
	// for existedBefore: the repository and the tree at From, read lazily
	ctx    context.Context
	dir    string
	before *Tree
}

// Diff lists the paths changed from one commit to another (renames as delete + add).
func Diff(ctx context.Context, dir, from, to string) (*Changes, error) {
	out, err := repo.Git(ctx, dir, "diff", "--name-status", "--no-renames", "-z", from, to, "--")
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	c := &Changes{From: from, To: to, Paths: map[string]byte{}, ctx: ctx, dir: dir}
	f := strings.Split(out, "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		if st := f[i]; st != "" {
			c.Paths[f[i+1]] = st[0]
		}
	}
	return c, nil
}

// FileAt returns the content of path at ref ("" and false when it does not exist).
func FileAt(ctx context.Context, dir, ref, p string) (string, bool) {
	c, err := judge.CleanPath(p)
	if err != nil {
		return "", false
	}
	out, err := repo.Git(ctx, dir, "show", ref+":"+c)
	if err != nil {
		return "", false
	}
	return out, true
}
