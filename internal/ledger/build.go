package ledger

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
)

// Builder turns agentic judgments of one repository at one revision into claims.
type Builder struct {
	Dir   string
	Repo  string
	Tree  *Tree
	Drift *agentic.DriftList
	Class instance.Classification
	Model string
	Now   func() time.Time
	docs  map[string][]string
}

// NewBuilder snapshots dir at ref.
func NewBuilder(ctx context.Context, dir, repoName, ref string) (*Builder, error) {
	t, err := ReadTree(ctx, dir, ref)
	if err != nil {
		return nil, err
	}
	return &Builder{Dir: dir, Repo: repoName, Tree: t, Now: time.Now, docs: map[string][]string{}}, nil
}

// lineText is line n of a document at the builder's revision.
func (b *Builder) lineText(ctx context.Context, p string, n int) (string, bool) {
	ls, ok := b.docs[p]
	if !ok {
		text, found := FileAt(ctx, b.Dir, b.Tree.Ref, p)
		if found {
			ls = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		}
		b.docs[p] = ls
	}
	if n < 1 || n > len(ls) {
		return "", false
	}
	return ls[n-1], true
}

// Claim records one judged line with the evidence that validated. Judgments without an
// answer (errors, dry runs) give no claim.
func (b *Builder) Claim(ctx context.Context, lr *agentic.LineResult) (Claim, bool) {
	if lr.Error != "" || lr.Parse == "" || lr.Parse == "failed" {
		return Claim{}, false
	}
	text, ok := b.lineText(ctx, lr.Path, lr.Line)
	if !ok {
		return Claim{}, false
	}
	c := Claim{
		ID: ClaimID(b.Repo, lr.Path, text), Repo: b.Repo, Path: lr.Path, Line: lr.Line, Text: text,
		DocBlob: b.Tree.Blobs[lr.Path], Category: lr.Category, Verdict: lr.Verdict,
		VerifiedAt: b.Now().UTC().Format(time.RFC3339), VerifiedRef: b.Tree.Ref, Model: lr.Model, PromptHash: lr.PromptHash,
		Private: b.Class.ClassOf(b.Repo, lr.Path) == instance.Private,
	}
	if b.Model != "" {
		c.Model = b.Model
	}
	for _, f := range lr.Facts {
		c.Facts = append(c.Facts, Fact{Fact: f.Fact, Status: f.Status, Valid: f.Valid})
		for _, e := range f.Evidence {
			if e.OK {
				b.add(&c, lr.Path, e.Evidence)
			}
		}
	}
	return c, true
}

func (b *Builder) add(c *Claim, doc string, e agentic.Evidence) {
	switch e.Kind {
	case agentic.KindQuote:
		if p, err := judge.CleanPath(e.Path); err == nil {
			c.Evidence.Quotes = append(c.Evidence.Quotes, Quote{Repo: b.Repo, Path: p, Line: e.Line, Blob: b.Tree.Blobs[p]})
		}
	case agentic.KindAbsence:
		scope := e.SearchScope
		if strings.TrimSpace(scope) == "" {
			scope = "*"
		}
		h, n, err := b.Tree.ScopeHash(scope, doc)
		if err == nil {
			c.Evidence.Absence = append(c.Evidence.Absence, Absence{Pattern: e.SearchPattern, Scope: scope, Regex: e.SearchRegex, SkipFile: doc, FilesHash: h, Files: n})
		}
	case agentic.KindDrift:
		c.Evidence.Drift = append(c.Evidence.Drift, b.drift(strings.TrimSpace(e.DriftID)))
	case agentic.KindPathMissing:
		c.Evidence.PathMissing = append(c.Evidence.PathMissing, pathMissing(doc, e.Path))
	}
}

func (b *Builder) drift(id string) Drift {
	d := Drift{ID: id}
	if b.Drift == nil {
		return d
	}
	for _, e := range b.Drift.Entries {
		if e.ID != id {
			continue
		}
		d.Commits = e.Commits
		for _, q := range e.Evidence {
			if p, err := judge.CleanPath(q.Path); err == nil {
				d.Files = append(d.Files, File{Path: p, Blob: b.Tree.Blobs[p]})
			}
		}
		if e.AbsentOldPath {
			d.RemovedPath = strings.TrimSuffix(strings.TrimPrefix(strings.Trim(strings.TrimSpace(e.Old), "`"), "./"), "/")
			if i := strings.IndexAny(d.RemovedPath, "*<"); i >= 0 {
				d.RemovedPath = strings.TrimSuffix(d.RemovedPath[:i], "/")
			}
		}
	}
	return d
}

func pathMissing(doc, p string) PathMissing {
	c, err := judge.CleanPath(strings.Trim(strings.TrimSpace(p), "`"))
	if err != nil {
		return PathMissing{Path: p}
	}
	pm := PathMissing{Path: c, Candidates: []string{c}}
	if rel := path.Join(path.Dir(doc), c); rel != c {
		pm.Candidates = append(pm.Candidates, rel)
	}
	if !strings.Contains(c, "/") {
		pm.Name = c
	}
	return pm
}
