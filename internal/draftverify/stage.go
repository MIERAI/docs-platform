package draftverify

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// stageTree is what the sandbox sees as the repository: the checkout's tracked files only
// (git ls-files; no .git, no untracked files such as a local .env whose content an example
// could print into the result), with the drafted document in place. Files are hard-linked
// when possible (the mount is read-only) and copied otherwise; the document is always a
// fresh file.
func stageTree(ctx context.Context, root, dst, doc, newText string) error {
	out, err := repo.Git(ctx, root, "ls-files", "-z", "--cached")
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	for _, p := range strings.Split(out, "\x00") {
		c, err := judge.CleanPath(p)
		if p == "" || err != nil || c == doc {
			continue
		}
		if err := stageFile(filepath.Join(root, filepath.FromSlash(c)), filepath.Join(dst, filepath.FromSlash(c))); err != nil {
			return err
		}
	}
	c, err := judge.CleanPath(doc)
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	d := filepath.Join(dst, filepath.FromSlash(c))
	if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil { //nolint:gosec // read by the sandbox's non-root user
		return fmt.Errorf("stage: %w", err)
	}
	if err := os.WriteFile(d, []byte(newText), 0o644); err != nil { //nolint:gosec // as above
		return fmt.Errorf("stage: %w", err)
	}
	return nil
}

func stageFile(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return nil //nolint:nilerr // tracked but deleted in the checkout: not staged
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // read by the sandbox's non-root user
		return fmt.Errorf("stage: %w", err)
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(src)
		if err != nil {
			return fmt.Errorf("stage: %w", err)
		}
		return wrap(os.Symlink(t, dst))
	case !st.Mode().IsRegular():
		return nil
	}
	if st.Mode().Perm()&0o004 != 0 && os.Link(src, dst) == nil {
		return nil // world-readable: the sandbox user can read the shared inode
	}
	return copyFile(src, dst, st.Mode().Perm()|0o444)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("stage: %w", err)
	}
	return wrap(out.Close())
}

func wrap(err error) error {
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	return nil
}
