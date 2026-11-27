package draftverify

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's object id, not a security primitive
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// BlobID is git's object id of content as a blob (git hash-object without filters), in the
// repository's object format (sha1, or sha256).
func BlobID(ctx context.Context, root, content string) string {
	hdr := "blob " + strconv.Itoa(len(content)) + "\x00"
	if out, err := repo.Git(ctx, root, "rev-parse", "--show-object-format"); err == nil && strings.TrimSpace(out) == "sha256" {
		h := sha256.Sum256([]byte(hdr + content))
		return hex.EncodeToString(h[:])
	}
	h := sha1.Sum([]byte(hdr + content)) //nolint:gosec // git's object id
	return hex.EncodeToString(h[:])
}

// guardGate: the document in the checkout is the blob the draft was made against. It also
// returns the current blob.
func guardGate(ctx context.Context, root, current, base string) (Gate, string) {
	cur := BlobID(ctx, root, current)
	g := Gate{Name: GateGuard, Status: Pass, Detail: "the document is the blob the draft was made against"}
	switch {
	case base == "":
		g.Status, g.Detail = Fail, "the draft records no base blob: it cannot be applied safely"
	case base != cur:
		g.Status, g.Detail = Fail, fmt.Sprintf("the document changed since the draft was made (blob %.12s, draft made against %.12s): draft again", cur, base)
	}
	return g, cur
}

// ErrMoved is returned when the document's blob differs from the draft's base blob.
var ErrMoved = errors.New("the document changed since the draft was made")

// Apply writes newText to the document p under root, but only when the document's current
// blob is base (the blob the draft was made against) and the new text parses (the format
// gate). It is the only function of this package that writes.
func Apply(ctx context.Context, root, p, base, newText string) error {
	clean, err := judge.CleanPath(p)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	defer func() { _ = r.Close() }()
	b, err := r.ReadFile(filepath.FromSlash(clean))
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if g, cur := guardGate(ctx, root, string(b), base); g.Status != Pass {
		return fmt.Errorf("apply %s: %w (blob %.12s, draft made against %.12s)", clean, ErrMoved, cur, base)
	}
	changed := changedNew(diffLines(splitLines(string(b)), splitLines(newText)))
	if g := formatGate(ctx, clean, string(b), newText, changed); g.Status == Fail {
		return fmt.Errorf("apply %s: the draft does not parse: %s", clean, firstFail(g))
	}
	st, err := r.Stat(filepath.FromSlash(clean))
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if err := r.WriteFile(filepath.FromSlash(clean), []byte(newText), st.Mode().Perm()); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	return nil
}

func firstFail(g Gate) string {
	for _, it := range g.Items {
		if it.Status == Fail {
			return fmt.Sprintf("line %d: %s", it.Line, it.Detail)
		}
	}
	return g.Detail
}
