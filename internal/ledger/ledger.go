// Package ledger is the claim and evidence ledger (contracts/ledger-v1.schema.json): every
// judged documentation line with the validated evidence its verdict rests on, addressed as
// repo@ref:path:line with git blob hashes. A later revision invalidates a claim
// deterministically - no model - when any piece of that evidence changed: only those claims
// are judged again (docs/design.md §12.1).
//
// The ledger holds documentation text and paths of the inspected repositories: it is private
// data, written with mode 0600 and never into an inspected repository.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion of docs-platform/ledger/v1.
const SchemaVersion = "1.0"

// maxLedgerBytes bounds a ledger file.
const maxLedgerBytes = 256 << 20

// Ledger is docs-platform/ledger/v1.
type Ledger struct {
	SchemaVersion string  `json:"schema_version"`
	Claims        []Claim `json:"claims"`
}

// Claim is one judged documentation line.
type Claim struct {
	// ID is stable across line moves: repository, document path and normalised line text.
	ID   string `json:"id"`
	Repo string `json:"repo"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
	// DocBlob is the document's blob at VerifiedRef.
	DocBlob  string `json:"doc_blob"`
	Category string `json:"category"`
	Verdict  string `json:"verdict"`
	// VerifiedAt (RFC 3339) and VerifiedRef (a commit) say when and against what the claim
	// was last judged.
	VerifiedAt  string   `json:"verified_at"`
	VerifiedRef string   `json:"verified_ref"`
	Model       string   `json:"model,omitempty"`
	PromptHash  string   `json:"prompt_hash,omitempty"`
	Facts       []Fact   `json:"facts,omitempty"`
	Evidence    Evidence `json:"evidence"`
	// Private: the document is classified private (the claim must not reach a cloud model).
	Private bool `json:"private,omitempty"`
	// Target (repository groups) is what the claim was judged against when that is not the
	// document's own repository at the same revision: VerifiedRef is then the target's
	// commit, and DocRef the document repository's commit.
	Target *Target `json:"target,omitempty"`
	DocRef string  `json:"doc_ref,omitempty"`
}

// Target is the repository and declared revision a claim was judged against.
type Target struct {
	Repo string `json:"repo"`
	// Ref is the declared revision (e.g. develop, prod); the commit is VerifiedRef.
	Ref string `json:"ref"`
}

// docRef is the document repository's commit the claim was judged at.
func (c *Claim) docRef() string {
	if c.DocRef != "" {
		return c.DocRef
	}
	return c.VerifiedRef
}

// Fact is a fact of the claim with its status and whether its evidence validated.
type Fact struct {
	Fact   string `json:"fact"`
	Status string `json:"status"`
	Valid  bool   `json:"valid"`
}

// Evidence is the validated evidence of a claim.
type Evidence struct {
	Quotes      []Quote       `json:"quotes,omitempty"`
	Drift       []Drift       `json:"drift,omitempty"`
	Absence     []Absence     `json:"absence,omitempty"`
	PathMissing []PathMissing `json:"path_missing,omitempty"`
}

// Empty reports whether the claim rests on no evidence (it cannot be invalidated, only
// rechecked periodically).
func (e Evidence) Empty() bool {
	return len(e.Quotes)+len(e.Drift)+len(e.Absence)+len(e.PathMissing) == 0
}

// Quote is a cited line: repo@ref:path:line, and the file's blob at ref.
type Quote struct {
	Repo string `json:"repo,omitempty"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Blob string `json:"blob"`
}

// Drift is a validated drift entry the claim cites.
type Drift struct {
	ID      string   `json:"id"`
	Commits []string `json:"commits,omitempty"`
	// Files are the entry's quoted files with their blobs.
	Files []File `json:"files,omitempty"`
	// RemovedPath is the path the entry says is gone (it must stay gone).
	RemovedPath string `json:"removed_path,omitempty"`
}

// File is a path and its blob.
type File struct {
	Path string `json:"path"`
	Blob string `json:"blob"`
}

// Absence is a search that found nothing, with the file set it covered.
type Absence struct {
	Pattern string `json:"pattern"`
	Scope   string `json:"scope"`
	Regex   bool   `json:"regex,omitempty"`
	// SkipFile is the document under test, excluded from the search.
	SkipFile string `json:"skip_file,omitempty"`
	// FilesHash identifies the covered files (paths and blobs); Files counts them.
	FilesHash string `json:"files_hash"`
	Files     int    `json:"files"`
}

// PathMissing is a path confirmed absent; Candidates are the forms checked (from the root,
// next to the document); Name is set for a bare file name (no file or directory of that
// name may exist anywhere).
type PathMissing struct {
	Path       string   `json:"path"`
	Candidates []string `json:"candidates"`
	Name       string   `json:"name,omitempty"`
}

// ClaimID is the stable id of a claim: independent of the line number.
func ClaimID(repo, path, text string) string {
	h := sha256.Sum256([]byte(repo + "\x00" + path + "\x00" + strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(h[:])[:16]
}

// TargetClaimID is the id of a claim judged against a declared target: a line documenting
// both develop and prod is two claims.
func TargetClaimID(repo, path, text string, t Target) string {
	return ClaimID(repo+"\x00"+t.Repo+"@"+t.Ref, path, text)
}

// Load reads a ledger; a missing file is an empty ledger.
func Load(p string) (*Ledger, error) {
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Ledger{SchemaVersion: SchemaVersion, Claims: []Claim{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxLedgerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if len(b) > maxLedgerBytes {
		return nil, fmt.Errorf("ledger %s exceeds %d bytes", p, maxLedgerBytes)
	}
	return Parse(b)
}

// Parse decodes a ledger document.
func Parse(b []byte) (*Ledger, error) {
	var l Ledger
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if !strings.HasPrefix(l.SchemaVersion, "1.") {
		return nil, fmt.Errorf("ledger: schema_version %q: this docsp reads 1.x", l.SchemaVersion)
	}
	for i, c := range l.Claims {
		if c.ID == "" || c.Repo == "" || c.Path == "" || c.VerifiedRef == "" {
			return nil, fmt.Errorf("ledger: claim %d: id, repo, path and verified_ref are required", i)
		}
	}
	if l.Claims == nil {
		l.Claims = []Claim{}
	}
	return &l, nil
}

// Save writes the ledger (mode 0600, parent directories 0700), claims sorted by id.
func (l *Ledger) Save(p string) error {
	l.SchemaVersion = SchemaVersion
	sort.Slice(l.Claims, func(i, j int) bool { return l.Claims[i].ID < l.Claims[j].ID })
	b, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	return nil
}

// Merge adds claims; a claim replaces the stored one with the same id when it was verified
// later (or at the same time).
func (l *Ledger) Merge(cs ...Claim) {
	idx := map[string]int{}
	for i, c := range l.Claims {
		idx[c.ID] = i
	}
	for _, c := range cs {
		i, ok := idx[c.ID]
		switch {
		case !ok:
			idx[c.ID] = len(l.Claims)
			l.Claims = append(l.Claims, c)
		case c.VerifiedAt >= l.Claims[i].VerifiedAt:
			l.Claims[i] = c
		}
	}
}

// Remove drops the claims with the given ids and returns how many it removed.
func (l *Ledger) Remove(ids ...string) int {
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	kept := l.Claims[:0]
	for _, c := range l.Claims {
		if !drop[c.ID] {
			kept = append(kept, c)
		}
	}
	n := len(l.Claims) - len(kept)
	l.Claims = kept
	return n
}
