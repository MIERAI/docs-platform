// Package history answers the staleness questions of the semantic audit from git, through the
// read-only, allow-listed git wrapper (only `git log` is used here): when did a documentation
// line last change, how many later commits touched the code it talks about, was that code
// added afterwards, and which release tags exist. Without git (not a repository, shallow
// history) every answer is "unknown" and the staleness signals are simply absent.
package history

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/repo"
)

// Commit is one commit id with its commit time (unix seconds).
type Commit struct {
	ID   string
	Time int64
}

// History caches git answers for one repository.
type History struct {
	dir   string
	ok    bool
	mu    sync.Mutex
	lines map[string]Commit
	added map[string]Commit
	after map[string]int
	calls int
}

// Open checks that dir is a git work tree with history.
func Open(ctx context.Context, dir string) *History {
	h := &History{dir: dir, lines: map[string]Commit{}, added: map[string]Commit{}, after: map[string]int{}}
	out, err := repo.Git(ctx, dir, "log", "-1", "--format=%H")
	h.ok = err == nil && strings.TrimSpace(out) != ""
	return h
}

// Available reports whether git history can be used.
func (h *History) Available() bool { return h != nil && h.ok }

// Calls is the number of git invocations made.
func (h *History) Calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// git runs one allow-listed read-only git command; a failure reads as "no output".
func (h *History) git(ctx context.Context, args ...string) string {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	out, err := repo.Git(ctx, h.dir, args...)
	if err != nil {
		return ""
	}
	return out
}

// parseCommit reads "<sha> <unix time>"; anything else is the zero Commit.
func parseCommit(s string) Commit {
	f := strings.Fields(s)
	if len(f) < 2 || !repo.ValidRef(f[0]) {
		return Commit{}
	}
	t, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return Commit{}
	}
	return Commit{ID: f[0], Time: t}
}

// LineChanged returns the last commit that changed line n of file.
func (h *History) LineChanged(ctx context.Context, file string, n int) (Commit, bool) {
	if !h.Available() || n < 1 || strings.HasPrefix(file, "-") {
		return Commit{}, false
	}
	key := file + ":" + strconv.Itoa(n)
	h.mu.Lock()
	c, hit := h.lines[key]
	h.mu.Unlock()
	if hit {
		return c, c.ID != ""
	}
	out := h.git(ctx, "log", "-n1", "--no-patch", "--format=%H %ct", "-L"+strconv.Itoa(n)+","+strconv.Itoa(n)+":"+file)
	c = parseCommit(firstLine(out))
	h.mu.Lock()
	h.lines[key] = c
	h.mu.Unlock()
	return c, c.ID != ""
}

// CommitsAfter counts commits after `since` (exclusive) that touched any of files.
func (h *History) CommitsAfter(ctx context.Context, since Commit, files []string) int {
	if !h.Available() || since.ID == "" || len(files) == 0 {
		return 0
	}
	key := since.ID + "\x00" + strings.Join(files, "\x00")
	h.mu.Lock()
	n, hit := h.after[key]
	h.mu.Unlock()
	if hit {
		return n
	}
	args := append([]string{"log", "--format=%H", since.ID + "..HEAD", "--"}, files...)
	out := h.git(ctx, args...)
	n = len(strings.Fields(out))
	h.mu.Lock()
	h.after[key] = n
	h.mu.Unlock()
	return n
}

// Added returns the commit that added file (following no renames).
func (h *History) Added(ctx context.Context, file string) (Commit, bool) {
	if !h.Available() {
		return Commit{}, false
	}
	h.mu.Lock()
	c, hit := h.added[file]
	h.mu.Unlock()
	if !hit {
		out := h.git(ctx, "log", "--diff-filter=A", "--format=%H %ct", "--", file)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		c = parseCommit(lines[len(lines)-1])
		h.mu.Lock()
		h.added[file] = c
		h.mu.Unlock()
	}
	return c, c.ID != ""
}

// Tags returns the tags reachable from HEAD (so a pinned checkout never sees later releases).
func (h *History) Tags(ctx context.Context) []string {
	if !h.Available() {
		return nil
	}
	out := h.git(ctx, "log", "--simplify-by-decoration", "--decorate-refs=refs/tags/", "--format=%D", "HEAD")
	var tags []string
	for _, l := range strings.Split(out, "\n") {
		for _, part := range strings.Split(l, ",") {
			if t, ok := strings.CutPrefix(strings.TrimSpace(part), "tag: "); ok {
				tags = append(tags, t)
			}
		}
	}
	return tags
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
