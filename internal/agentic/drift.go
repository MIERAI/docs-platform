package agentic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// DriftList is the result of one repository drift scan.
type DriftList struct {
	Repo       string `json:"repo"`
	Head       string `json:"head"`
	PromptHash string `json:"prompt_hash"`
	Model      string `json:"model"`
	// Raw is the model's list as answered (kept, so validation can always be re-run).
	Raw []DriftEntry `json:"raw"`
	// Entries are the validated entries (ids D1, D2, ...): only these reach the judgments.
	Entries []DriftEntry `json:"validated"`
	Parse   string       `json:"parse"`
	CostUSD float64      `json:"cost_usd"`
	Error   string       `json:"error,omitempty"`
	Cached  bool         `json:"-"`
}

// IDs returns the set of validated entry ids.
func (d *DriftList) IDs() map[string]bool {
	out := map[string]bool{}
	if d == nil {
		return out
	}
	for _, e := range d.Entries {
		out[e.ID] = true
	}
	return out
}

// maxDriftInPrompt bounds the entries shared with each judgment.
const maxDriftInPrompt = 40

// Text renders the validated entries for a prompt.
func (d *DriftList) Text() string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	for i, e := range d.Entries {
		if i == maxDriftInPrompt {
			break
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s [%s] %s -> %s: %s", e.ID, e.Kind, e.Old, e.New, e.Summary)
		if len(e.Commits) > 0 {
			fmt.Fprintf(&b, " (commits %s)", strings.Join(e.Commits[:min(3, len(e.Commits))], ", "))
		}
		if len(e.Evidence) > 0 {
			var at []string
			for _, q := range e.Evidence[:min(2, len(e.Evidence))] {
				at = append(at, fmt.Sprintf("%s:%d", q.Path, q.Line))
			}
			fmt.Fprintf(&b, " (now: %s)", strings.Join(at, "; "))
		}
	}
	return b.String()
}

var (
	reCommit    = regexp.MustCompile(`^[0-9a-fA-F]{6,40}$`)
	rePathish   = regexp.MustCompile(`^[\w./<>*-]+$`)
	pathishExts = []string{".yaml", ".yml", ".md", ".py", ".sh", ".go", ".tf"}
)

// ValidateDrift keeps the entries the repository bears out, numbered D1, D2, ... in order.
// For each entry, commits must be hashes of ancestors of HEAD and quotes must be verbatim at
// path:line±2 (anything else is dropped from the entry). An entry that says a path was
// deleted, renamed or moved must name a path that is really gone, or it is rejected. An
// entry is kept when it has a verified quote, or a valid commit and a verified removed path.
func ValidateDrift(ctx context.Context, t *judge.Tools, raw []DriftEntry) []DriftEntry {
	var ok []DriftEntry
	for _, e := range raw {
		if v, keep := validateEntry(ctx, t, e); keep {
			v.ID = "D" + strconv.Itoa(len(ok)+1)
			ok = append(ok, v)
		}
	}
	return ok
}

func validateEntry(ctx context.Context, t *judge.Tools, e DriftEntry) (DriftEntry, bool) {
	v := DriftEntry{Kind: e.Kind, Old: e.Old, New: e.New, Summary: e.Summary, Commits: []string{}, Evidence: []QuoteRef{}}
	for _, c := range e.Commits {
		if c = strings.TrimSpace(c); reCommit.MatchString(c) && isAncestor(ctx, t.Dir(), c) {
			v.Commits = append(v.Commits, c)
		}
	}
	for _, q := range e.Evidence {
		if t.VerifyNear(q.Path, q.Line, q.Quote).OK {
			v.Evidence = append(v.Evidence, q)
		}
	}
	if old := strings.Trim(strings.TrimSpace(e.Old), "`"); claimsGone(e.Kind) && pathish(old) {
		if v.AbsentOldPath = !t.Exists(trimGlob(old)); !v.AbsentOldPath {
			return v, false // says the path is gone; it is not
		}
	}
	return v, len(v.Evidence) > 0 || len(v.Commits) > 0 && v.AbsentOldPath
}

func claimsGone(kind string) bool { return kind == "deleted" || kind == "renamed" || kind == "moved" }

func pathish(s string) bool {
	if !rePathish.MatchString(s) {
		return false
	}
	if strings.Contains(s, "/") {
		return true
	}
	for _, x := range pathishExts {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}

// trimGlob turns "dir/*", "dir/**", "dir/<name>" and "dir/" into "dir".
func trimGlob(s string) string {
	s = strings.TrimPrefix(s, "./")
	if i := strings.IndexAny(s, "*<"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "/")
}

func isAncestor(ctx context.Context, dir, c string) bool {
	_, err := repo.Git(ctx, dir, "merge-base", "--is-ancestor", c, "HEAD")
	return err == nil
}

// Head returns the full SHA of HEAD in dir.
func Head(ctx context.Context, dir string) (string, error) {
	out, err := repo.Git(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("drift: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// cacheFile is the cache entry of one scan: repository, HEAD, prompt and model.
func cacheFile(dir, repoName, head, model string) string {
	key := hash16(repoName, head, DriftPromptHash(), model)
	safe := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(repoName, "_")
	return filepath.Join(dir, fmt.Sprintf("%s-%s-%s.json", safe, head[:min(12, len(head))], key))
}

func loadCache(file string) (*DriftList, bool) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, false
	}
	var d DriftList
	if json.Unmarshal(b, &d) != nil || d.Error != "" {
		return nil, false
	}
	return &d, true
}

func storeCache(file string, d *DriftList) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return fmt.Errorf("drift cache: %w", err)
	}
	b, err := json.MarshalIndent(d, "", " ")
	if err != nil {
		return fmt.Errorf("drift cache: %w", err)
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return fmt.Errorf("drift cache: %w", err)
	}
	return nil
}

// DefaultCacheDir is where drift lists are cached unless configured: the user's cache
// directory (never inside an inspected repository).
func DefaultCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "docsp", "drift")
}
