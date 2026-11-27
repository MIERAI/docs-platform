package claim

import (
	"path"
	"regexp"
	"strings"
)

// Reasons a claim is not put to the judge (Filter.Skip); every skip is counted by reason.
const (
	// SkipHistorical: the sentence describes the past ("previously", "used to", "before v2",
	// 以前, 曾经, 已废弃, 以前は, 廃止された): it is not a statement about the code now.
	SkipHistorical = "historical"
	// SkipChangelog: the document is a changelog, history or release notes: a record of the
	// past by design.
	SkipChangelog = "changelog"
	// SkipTestFile: the claim is a comment in a test file, or names only test files.
	SkipTestFile = "test_file"
	// SkipPrivate: the claim documents, or names only, unexported / private symbols.
	SkipPrivate = "private_symbol"
)

// DefaultChangelogs are the changelog-type documents skipped by default. A pattern without
// "/" matches a file name in any directory; one with "/" matches the whole path ("**" = any
// directories). Matching ignores case.
var DefaultChangelogs = []string{
	"changelog*", "changes.md", "history*", "news.md", "release-notes*", "release_notes*", "releasenotes*",
	"docs/**/changelog*", "docs/**/release-notes/**", "docs/**/releases/**",
}

// Filter decides which claims are not judged. The zero value skips nothing; DefaultFilter
// is what a repository gets without configuration (.docsp.yaml audit.skip).
type Filter struct {
	Historical bool
	Changelogs []string
	Tests      bool
	Private    bool
}

// DefaultFilter skips all four kinds.
func DefaultFilter() Filter {
	return Filter{Historical: true, Changelogs: DefaultChangelogs, Tests: true, Private: true}
}

// Doc returns why every claim of a document is skipped ("" = none): changelog-type
// documents. Code files are never changelogs (internal/history/history.go is code).
func (f Filter) Doc(p string) string {
	if isDocFile(p) && MatchAny(f.Changelogs, p) {
		return SkipChangelog
	}
	return ""
}

// Skip returns why a claim is not judged ("" = judge it). decl is the declaration a
// code-comment claim documents (the first code line after the comment; "" when unknown).
func (f Filter) Skip(c *Claim, decl string) string {
	if r := f.Doc(c.Path); r != "" && !c.Comment {
		return r
	}
	if c.Comment {
		switch {
		case f.Tests && IsTestPath(c.Path):
			return SkipTestFile
		case f.Private && rePrivateDecl.MatchString(decl):
			return SkipPrivate
		}
	}
	if f.Historical && IsHistorical(c.Text) {
		return SkipHistorical
	}
	return f.about(c)
}

// Text is Skip for a claim known only by its document and line text (a ledger entry).
func (f Filter) Text(p, text string) string {
	c := fromText(text, spansOf(text))
	c.Path = p
	return f.Skip(&c, "")
}

// about: every identifier the claim names is a test file or a private symbol.
func (f Filter) about(c *Claim) string {
	if !f.Tests && !f.Private {
		return ""
	}
	n, tests, private := 0, 0, 0
	fromSpan := map[string]bool{}
	for _, sp := range spansOf(c.Text) {
		fromSpan[identKey(sp)] = true
		n++
		switch {
		case f.Tests && IsTestPath(identKey(sp)):
			tests++
		case f.Private && IsPrivateSymbol(strings.TrimSpace(sp)):
			private++
		}
	}
	for _, id := range c.Idents {
		if fromSpan[id] {
			continue
		}
		n++
		if f.Tests && IsTestPath(id) {
			tests++
		}
	}
	switch {
	case n == 0 || tests+private < n:
		return ""
	case tests >= private:
		return SkipTestFile
	}
	return SkipPrivate
}

var (
	reHistorical = regexp.MustCompile(`(?i)\b(?:previously|formerly|historically|originally|used to)\b` +
		`|\b(?:was|were|had been) (?:deprecated|removed|renamed|replaced|dropped|introduced|added|changed)\b` +
		`|\b(?:before|prior to|until) (?:v\d+(?:\.\d+)*|(?:version|release) \d+(?:\.\d+)*|\d+\.\d+\.\d+)\b` +
		`|\bin (?:older|earlier|previous|old) (?:versions?|releases?)\b` +
		`|(?:^|[^0-9天日周月年时分秒])以前|曾经|已废弃|原先|旧版本|之前的版本` +
		`|以前は|かつて|廃止された|廃止済み|従来は|旧バージョン`)
	// rePrivateDecl: an unexported Go declaration, a Python _name, a private member.
	rePrivateDecl = regexp.MustCompile(`^\s*(?:func (?:\([^)]*\) )?[a-z_]|(?:type|var|const) [a-z_]` +
		`|(?:async )?def _[A-Za-z]|class _[A-Za-z]|(?:(?:public|protected|internal|static|final|override|readonly|abstract)\s+)*private\s|#[A-Za-z_])`)
	// rePrivateIdent: _name (not a __dunder__), #name, Type.method / (*Type).method,
	// pkg.function() written with lower-case names.
	rePrivateIdent = regexp.MustCompile(`^(?:_[A-Za-z]\w*|#[A-Za-z_]\w*|\(?\*?[A-Z]\w*\)?\.[a-z_]\w*(?:\(\))?|[a-z]\w*\.[a-z_]\w*\(\))$`)
	reDunder       = regexp.MustCompile(`^__\w+__$`)
	reTestPath     = regexp.MustCompile(`(?i)(?:^|/)(?:tests?|__tests__|testdata|spec|src/test)/|_test\.go$|(?:^|/)test_[^/]*\.py$|_test\.py$|\.(?:test|spec)\.[cm]?[jt]sx?$|(?:Test|Tests|Spec)\.(?:java|kt|cs|swift)$`)
)

// IsHistorical reports whether a sentence describes the past.
func IsHistorical(s string) bool { return reHistorical.MatchString(stripSpans(s)) }

// IsTestPath reports whether a path is a test file or test data.
func IsTestPath(p string) bool { return reTestPath.MatchString(p) }

// IsPrivateSymbol reports whether a code span names an unexported or private symbol.
func IsPrivateSymbol(s string) bool { return rePrivateIdent.MatchString(s) && !reDunder.MatchString(s) }

// IsPrivateDecl reports whether a line of code declares an unexported or private symbol.
func IsPrivateDecl(line string) bool { return rePrivateDecl.MatchString(line) }

func isDocFile(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".markdown", ".rst", ".txt", ".adoc", "":
		return true
	}
	return false
}

// MatchAny reports whether a path matches one of the patterns (see DefaultChangelogs).
func MatchAny(patterns []string, p string) bool {
	p = strings.ToLower(strings.TrimPrefix(p, "./"))
	for _, pat := range patterns {
		pat = strings.ToLower(strings.TrimPrefix(pat, "./"))
		if !strings.Contains(pat, "/") {
			if ok, _ := path.Match(pat, path.Base(p)); ok {
				return true
			}
			continue
		}
		if matchSegs(strings.Split(pat, "/"), strings.Split(p, "/")) {
			return true
		}
	}
	return false
}

func matchSegs(pat, p []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(p); i++ {
				if matchSegs(pat[1:], p[i:]) {
					return true
				}
			}
			return false
		}
		if len(p) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], p[0]); !ok {
			return false
		}
		pat, p = pat[1:], p[1:]
	}
	return len(p) == 0
}
