// Package residue is a layer ③ check for things that must not appear in a repository's text at
// all: names of unrelated or private projects, internal host names, personal paths. The terms
// come from a private instance file (never from the product repository); matching is
// case-insensitive on word boundaries.
package residue

import (
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/extract/lineidx"
	"github.com/MIERAI/docs-platform/internal/match"
	"github.com/MIERAI/docs-platform/internal/model"
)

// Version of the residue check.
const Version = "0.1.0"

// Checker holds compiled terms.
type Checker struct {
	terms []term
}

type term struct {
	text string
	re   *regexp.Regexp
}

// New compiles terms (literal strings).
func New(terms []string) *Checker {
	c := &Checker{}
	for _, t := range terms {
		re := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(` + regexp.QuoteMeta(t) + `)(?:$|[^A-Za-z0-9_])`)
		c.terms = append(c.terms, term{text: t, re: re})
	}
	return c
}

// Empty reports whether there is nothing to check.
func (c *Checker) Empty() bool { return c == nil || len(c.terms) == 0 }

// Check returns one issue per (file, term) with every line where the term appears.
func (c *Checker) Check(path string, content []byte) []match.Issue {
	if c.Empty() {
		return nil
	}
	idx := lineidx.New(content)
	var out []match.Issue
	for _, t := range c.terms {
		locs := matches(t.re, content, idx, path)
		if len(locs) == 0 {
			continue
		}
		out = append(out, match.Issue{
			Type: match.TypeResidue, Kind: model.KindTerm, Subject: strings.ToLower(t.text),
			Confidence: model.ConfHigh, Anchor: path, Locations: locs,
		})
	}
	return out
}

func matches(re *regexp.Regexp, content []byte, idx lineidx.Index, path string) []match.Location {
	var locs []match.Location
	last := 0
	for _, m := range re.FindAllSubmatchIndex(content, -1) {
		if l := idx.Line(m[2]); l != last {
			locs = append(locs, match.Location{Path: path, Line: l, Role: "doc"})
			last = l
		}
	}
	return locs
}
