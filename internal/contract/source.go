package contract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/repo"
)

// maxFileBytes bounds every file an adapter reads.
const maxFileBytes = 4 << 20

// Source is one side's files: a repository at a revision (read with git show / ls-tree, the
// checkout is never touched) or a working tree (uncommitted changes included; history comes
// from HEAD plus the uncommitted diff). Paths are globs limiting what the side owns.
type Source struct {
	Repo  string
	Dir   string
	Ref   string // "" = working tree
	Paths []string

	commit string
	files  []string
	isGit  bool

	mu    sync.Mutex
	texts map[string]string
}

// OpenSource resolves the revision and lists the files under the path globs.
func OpenSource(ctx context.Context, name, dir, ref string, paths []string) (*Source, error) {
	s := &Source{Repo: name, Dir: dir, Ref: ref, Paths: paths}
	if name == "" {
		s.Repo = filepath.Base(dir)
	}
	head := ref
	if head == "" {
		head = "HEAD"
	}
	if !repo.ValidRef(head) {
		return nil, fmt.Errorf("contract: invalid revision %q", head)
	}
	out, err := repo.Git(ctx, dir, "rev-parse", "--verify", head+"^{commit}")
	switch {
	case err == nil:
		s.commit, s.isGit = strings.TrimSpace(out), true
	case ref != "":
		return nil, fmt.Errorf("contract: %s: %w", dir, err)
	}
	all, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		if len(paths) == 0 || repo.MatchAny(paths, f) {
			s.files = append(s.files, f)
		}
	}
	sort.Strings(s.files)
	return s, nil
}

func (s *Source) list(ctx context.Context) ([]string, error) {
	var out string
	var err error
	switch {
	case s.Ref != "":
		out, err = repo.Git(ctx, s.Dir, "ls-tree", "-r", "-z", "--name-only", s.commit)
	case s.isGit:
		out, err = repo.Git(ctx, s.Dir, "ls-files", "-z")
	default:
		r, oerr := repo.Open(s.Dir, nil)
		if oerr != nil {
			return nil, fmt.Errorf("contract: %w", oerr)
		}
		return r.AllFiles(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("contract: list %s: %w", s.Dir, err)
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// Info describes the source for the contract document.
func (s *Source) Info() SourceInfo {
	ref := s.Ref
	if ref == "" {
		ref = "WORKTREE"
	}
	return SourceInfo{Repo: s.Repo, Ref: ref, Commit: s.commit, Paths: s.Paths, Worktree: s.Ref == ""}
}

// Files are the files under the path globs, sorted.
func (s *Source) Files() []string { return s.files }

// Has reports whether a file is part of the source.
func (s *Source) Has(p string) bool {
	i := sort.SearchStrings(s.files, p)
	return i < len(s.files) && s.files[i] == p
}

// Read returns a file of the source (at the revision, or from the working tree).
func (s *Source) Read(ctx context.Context, p string) ([]byte, error) {
	if s.Ref != "" {
		out, err := repo.Git(ctx, s.Dir, "show", s.commit+":"+p)
		if err != nil {
			return nil, fmt.Errorf("contract: %w", err)
		}
		if len(out) > maxFileBytes {
			return nil, fmt.Errorf("contract: %s: larger than %d bytes", p, maxFileBytes)
		}
		return []byte(out), nil
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.FromSlash(p))
	if err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("contract: %w", err)
	}
	if len(b) > maxFileBytes {
		return nil, errors.New("contract: " + p + ": too large")
	}
	return b, nil
}

// HistoryRef is the revision history is searched from ("" when the source is not in git).
func (s *Source) HistoryRef() string {
	if !s.isGit {
		return ""
	}
	return s.commit
}

// Names reports whether any file of the source within paths (files or globs) currently
// names term (in any of its spellings).
func (s *Source) Names(ctx context.Context, term string, paths []string) bool {
	if term == "" {
		return false
	}
	re := regexp.MustCompile(strings.ReplaceAll(termRegex(term), "(^|", "(?m)(^|"))
	for _, f := range s.files {
		if !inScope(f, paths) {
			continue
		}
		if re.MatchString(s.text(ctx, f)) {
			return true
		}
	}
	return false
}

func inScope(f string, paths []string) bool {
	for _, p := range paths {
		if p == f || repo.Match(p, f) {
			return true
		}
	}
	return false
}

// text reads a file once (cached; "" when unreadable).
func (s *Source) text(ctx context.Context, f string) string {
	s.mu.Lock()
	if t, ok := s.texts[f]; ok {
		s.mu.Unlock()
		return t
	}
	s.mu.Unlock()
	b, err := s.Read(ctx, f)
	t := ""
	if err == nil {
		t = string(b)
	}
	s.mu.Lock()
	if s.texts == nil {
		s.texts = map[string]string{}
	}
	s.texts[f] = t
	s.mu.Unlock()
	return t
}
