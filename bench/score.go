package main

import (
	"strings"

	"github.com/MIERAI/docs-platform/internal/findings"
)

// fview is the part of a finding the scorer needs.
type fview struct {
	Check      string
	Type       string
	Subject    string
	Confidence string
	Locs       []findings.Location
	Rule       string
	SubLayer   string
	Detail     string
	Quote      string
}

func view(f findings.Finding) fview {
	a, _ := f.Attributes["docs"].(findings.DocsAttributes)
	return fview{
		Check: f.Check, Type: a.FindingType, Subject: a.Subject, Confidence: a.Confidence, Locs: a.Locations,
		Rule: a.Rule, SubLayer: a.SubLayer, Detail: f.Title, Quote: a.Quote,
	}
}

// matches decides whether a finding corresponds to a ground-truth item. Identifiers are
// compared case-sensitively and as whole words; a wrong/obsolete finding must sit on the
// ground-truth line (±1) or name the same identifier in the same file.
func matches(f fview, g GTItem) bool {
	raw := g.Claim + "\n" + g.Actual + "\n" + g.Fix
	if f.Type == "semantic" {
		return matchesSemantic(f, g)
	}
	if f.Type == "undocumented" {
		return g.Type == "missing" && len(strings.TrimLeft(f.Subject, "-")) >= 3 && containsWord(raw, f.Subject)
	}
	if g.Type == "missing" {
		return false
	}
	for _, l := range f.Locs {
		if l.Path != g.File {
			continue
		}
		near := g.Line != nil && abs(l.Line-*g.Line) <= 1
		named := len(f.Subject) >= 5 && (strings.Contains(raw, f.Subject) || len(lastSeg(f.Subject)) >= 6 && strings.Contains(raw, lastSeg(f.Subject)))
		if near || named {
			return true
		}
	}
	return false
}

func lastSeg(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

func containsWord(txt, w string) bool {
	for i := 0; ; {
		j := strings.Index(txt[i:], w)
		if j < 0 {
			return false
		}
		s, e := i+j, i+j+len(w)
		if (s == 0 || !isIdent(txt[s-1])) && (e == len(txt) || !isIdent(txt[e])) {
			return true
		}
		i = s + 1
	}
}

func isIdent(c byte) bool {
	return c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

var confRank = map[string]int{"low": 1, "medium": 2, "high": 3}

// matchesSemantic: a semantic finding is about one documentation line: same file, line ±1.
func matchesSemantic(f fview, g GTItem) bool {
	return len(f.Locs) > 0 && f.Locs[0].Path == g.File && g.Line != nil && abs(f.Locs[0].Line-*g.Line) <= 1
}
