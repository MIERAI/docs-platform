// Package scan runs the enabled extractors over a repository (read-only, concurrently) and
// collects their output into an Inventory, the input of the matcher.
package scan

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Inventory is everything the extractors found.
type Inventory struct {
	model.Result
	DocFiles  []string
	CodeFiles []string
	// CodeTokens is every identifier-like token in non-documentation text files: the fallback
	// that keeps a reference resolved when it exists in code in a form no extractor knows.
	CodeTokens map[string]bool
	Gaps       []repo.Gap
}

// Options selects which files are documentation.
type Options struct {
	Docs []string
}

type job struct {
	path string
	doc  bool
}

type out struct {
	res    model.Result
	tokens []string
	gap    *repo.Gap
}

// Run extracts the whole repository.
func Run(r *repo.Repo, exts []plugin.Extractor, o Options) *Inventory {
	inv := &Inventory{CodeTokens: map[string]bool{}, Gaps: append([]repo.Gap(nil), r.Gaps...)}
	jobs := make([]job, 0, len(r.Files()))
	for _, f := range r.Files() {
		d := repo.MatchAny(o.Docs, f)
		jobs = append(jobs, job{path: f, doc: d})
		if d {
			inv.DocFiles = append(inv.DocFiles, f)
		} else {
			inv.CodeFiles = append(inv.CodeFiles, f)
		}
	}
	results := make([]out, len(jobs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			results[i] = extractOne(r, jobs[i], exts)
		}(i)
	}
	wg.Wait()
	for _, o := range results {
		inv.Merge(o.res)
		for _, t := range o.tokens {
			inv.CodeTokens[t] = true
		}
		if o.gap != nil {
			inv.Gaps = append(inv.Gaps, *o.gap)
		}
	}
	sort.Slice(inv.Gaps, func(i, j int) bool { return inv.Gaps[i].Path < inv.Gaps[j].Path })
	return inv
}

func extractOne(r *repo.Repo, j job, exts []plugin.Extractor) out {
	var o out
	b, err := r.Read(j.path)
	if errors.Is(err, repo.ErrTooLarge) {
		return o // generated or data files: not inspected, not a gap
	}
	if err != nil {
		o.gap = &repo.Gap{Path: j.path, Reason: err.Error()}
		return o
	}
	if isBinary(b) {
		return o
	}
	if !j.doc && !isIgnoreFile(j.path) {
		o.tokens = Tokens(stripComments(exts, j.path, b))
	}
	f := plugin.File{Path: j.path, Content: b}
	var errs []error
	for _, e := range exts {
		if !wants(e, j) {
			continue
		}
		res, err := safeExtract(e, f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		o.res.Merge(res)
	}
	if len(errs) > 0 {
		o.gap = &repo.Gap{Path: j.path, Reason: errors.Join(errs...).Error()}
	}
	return o
}

func wants(e plugin.Extractor, j job) bool {
	side := e.Info().Side
	return (side == plugin.SideDocs) == j.doc && e.Accepts(j.path)
}

func stripComments(exts []plugin.Extractor, p string, b []byte) []byte {
	for _, e := range exts {
		if s, ok := e.(plugin.Stripper); ok && e.Accepts(p) {
			return s.Strip(p, b)
		}
	}
	return b
}

// safeExtract isolates the run from a misbehaving plugin: a panic becomes a coverage gap.
func safeExtract(e plugin.Extractor, f plugin.File) (res model.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("extractor %s panicked on %s: %v", e.Info().Name, f.Path, r)
		}
	}()
	res, err = e.Extract(f)
	if err != nil {
		return res, fmt.Errorf("extractor %s: %w", e.Info().Name, err)
	}
	return res, nil
}

// isIgnoreFile: .gitignore-style files list paths that do not exist by design; their tokens
// must not make a dangling documentation path look resolved.
func isIgnoreFile(p string) bool {
	b := path.Base(p)
	return strings.HasPrefix(b, ".") && strings.HasSuffix(b, "ignore")
}

func isBinary(b []byte) bool {
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

var reCodeToken = regexp.MustCompile(`-{0,2}[A-Za-z0-9_][A-Za-z0-9_.\-/]*`)

// Tokens returns identifier-like tokens of a code file, with and without leading dashes and
// trailing dots.
func Tokens(b []byte) []string {
	raw := reCodeToken.FindAll(b, -1)
	out := make([]string, 0, len(raw)*2)
	for _, t := range raw {
		s := string(bytes.TrimRight(t, ".-/"))
		out = append(out, s)
		if bare := string(bytes.TrimLeft([]byte(s), "-")); bare != s {
			out = append(out, bare)
		}
		if strings.ContainsAny(s, "./") {
			out = append(out, strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '/' })...)
		}
	}
	return out
}
