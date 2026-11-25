// Package plugin defines the extractor plugin interface. Everything that knows about a concrete
// language, documentation format or file layout lives behind it; the core never imports a plugin.
package plugin

import (
	"fmt"
	"sort"

	"github.com/MIERAI/docs-platform/internal/model"
)

// Side tells the core whether an extractor reads documentation or code.
type Side string

// Sides.
const (
	SideDocs Side = "docs"
	SideCode Side = "code"
	// SideComments extractors read code files but report documentation references (comments).
	SideComments Side = "comments"
)

// Stripper is optionally implemented by comment extractors: it blanks comment text so the core
// can index code tokens that are not merely mentioned in comments.
type Stripper interface {
	Strip(path string, content []byte) []byte
}

// Info identifies an extractor; Version is recorded in every findings document (collector.tools).
type Info struct {
	Name    string
	Version string
	Side    Side
}

// File is one repository file handed to an extractor. Path is slash-separated and repo-relative.
type File struct {
	Path    string
	Content []byte
}

// Extractor turns one file into symbols (code side) or references and mentions (docs side).
// Implementations must be pure: no I/O besides the given content, safe for concurrent use.
type Extractor interface {
	Info() Info
	// Accepts reports whether the extractor wants this repo-relative path.
	Accepts(path string) bool
	Extract(f File) (model.Result, error)
}

// Registry holds the enabled extractors.
type Registry struct {
	byName map[string]Extractor
}

// NewRegistry builds a registry; duplicate names are an error.
func NewRegistry(exts ...Extractor) (*Registry, error) {
	r := &Registry{byName: map[string]Extractor{}}
	for _, e := range exts {
		n := e.Info().Name
		if _, dup := r.byName[n]; dup {
			return nil, fmt.Errorf("duplicate extractor %q", n)
		}
		r.byName[n] = e
	}
	return r, nil
}

// Enabled returns the extractors sorted by name, minus the disabled ones.
func (r *Registry) Enabled(disabled map[string]bool) []Extractor {
	out := make([]Extractor, 0, len(r.byName))
	for n, e := range r.byName {
		if !disabled[n] {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info().Name < out[j].Info().Name })
	return out
}

// Versions maps extractor name to version, for collector.tools.
func Versions(exts []Extractor) map[string]string {
	m := make(map[string]string, len(exts))
	for _, e := range exts {
		m["extract-"+e.Info().Name] = e.Info().Version
	}
	return m
}
