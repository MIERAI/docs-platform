// Package refclass classifies text found in documentation (inline code, command lines) into
// typed references. It is documentation-format neutral: Markdown, AsciiDoc or reST extractors
// all hand it plain strings.
package refclass

import (
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

// Candidate is a classified reference before it gets a location.
type Candidate struct {
	Kind       model.Kind
	Text       string
	Program    string
	Confidence model.Confidence
	// Offset is the byte offset of the candidate in the classified text, when known (-1 otherwise).
	Offset int
}

var (
	reLongFlag  = regexp.MustCompile(`^--([a-zA-Z0-9][a-zA-Z0-9_.-]*)(=.*)?$`)
	reShortFlag = regexp.MustCompile(`^-([a-zA-Z])$`)
	reEnvRef    = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)`)
	reEnvAssign = regexp.MustCompile(`^([A-Z_][A-Z0-9_]*)=`)
	reUpperSnk  = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)+$`)
	reMetric    = regexp.MustCompile(`^[a-z_:][a-z0-9_:]*_(total|seconds|bytes|count|sum|bucket|ratio|info|milliseconds|errors|created|timestamp_seconds)$`)
	reMetricIn  = regexp.MustCompile(`[a-z_:][a-z0-9_:]*_(?:total|seconds|bytes|count|sum|bucket|ratio|info|milliseconds)\b`)
	rePathChars = regexp.MustCompile(`^~?[A-Za-z0-9_.@+/*-]+$`)
	reDotted    = regexp.MustCompile(`^[a-z][a-zA-Z0-9_-]*(\.[a-zA-Z0-9_-]+)+$`)
	reScheme    = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
)

// knownExt are file extensions that make a token look like a file path.
var knownExt = map[string]bool{
	".go": true, ".py": true, ".sh": true, ".bash": true, ".md": true, ".yaml": true, ".yml": true,
	".json": true, ".jsonl": true, ".toml": true, ".tf": true, ".tfvars": true, ".hcl": true, ".js": true,
	".ts": true, ".mjs": true, ".txt": true, ".cfg": true, ".ini": true, ".conf": true, ".sql": true,
	".proto": true, ".mod": true, ".sum": true, ".lock": true, ".env": true, ".tpl": true, ".j2": true,
	".rb": true, ".rs": true, ".java": true, ".kt": true, ".ps1": true, ".service": true, ".xml": true,
	".html": true, ".css": true, ".csv": true, ".rego": true, ".cue": true, ".nix": true, ".pem": true,
}

// tlds are last segments that make a dotted token a host name rather than a config key.
var tlds = map[string]bool{
	"com": true, "org": true, "io": true, "net": true, "dev": true, "local": true, "internal": true,
	"cluster": true, "svc": true, "app": true, "co": true, "jp": true, "cn": true, "ai": true, "sh": true,
}

// HasKnownExt reports whether p ends in an extension listed in knownExt.
func HasKnownExt(p string) bool { return knownExt[strings.ToLower(path.Ext(p))] }

// Span classifies the content of an inline code span.
func Span(text string) []Candidate {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 300 {
		return nil
	}
	if strings.ContainsAny(t, " \t") {
		return CommandLine(t, model.ConfMedium)
	}
	if c, ok := single(t); ok {
		return []Candidate{c}
	}
	return promql(t)
}

func single(t string) (Candidate, bool) {
	switch {
	case reScheme.MatchString(t):
		return Candidate{}, false
	case reLongFlag.MatchString(t):
		return Candidate{Kind: model.KindCLIFlag, Text: "--" + reLongFlag.FindStringSubmatch(t)[1], Confidence: model.ConfMedium}, true
	case reShortFlag.MatchString(t):
		return Candidate{Kind: model.KindCLIFlag, Text: t, Confidence: model.ConfLow}, true
	case reEnvRef.MatchString(t) && (t[0] == '$'):
		return Candidate{Kind: model.KindEnvVar, Text: reEnvRef.FindStringSubmatch(t)[1], Confidence: model.ConfHigh}, true
	case reEnvAssign.MatchString(t):
		return Candidate{Kind: model.KindEnvVar, Text: reEnvAssign.FindStringSubmatch(t)[1], Confidence: model.ConfMedium}, true
	case reUpperSnk.MatchString(t):
		return Candidate{Kind: model.KindEnvVar, Text: t, Confidence: model.ConfMedium}, true
	case reMetric.MatchString(t):
		return Candidate{Kind: model.KindMetric, Text: t, Confidence: model.ConfMedium}, true
	}
	return pathOrKey(t)
}

func pathOrKey(t string) (Candidate, bool) {
	if rePathChars.MatchString(t) && (strings.Contains(t, "/") || HasKnownExt(t)) && !strings.Contains(t, "//") {
		return Candidate{Kind: model.KindFilePath, Text: t, Confidence: model.ConfMedium}, true
	}
	if reDotted.MatchString(t) {
		last := t[strings.LastIndexByte(t, '.')+1:]
		if !tlds[last] && !HasKnownExt(t) {
			return Candidate{Kind: model.KindConfigKey, Text: t, Confidence: model.ConfLow}, true
		}
	}
	return Candidate{}, false
}

// promql finds metric-like names inside an expression such as rate(x_total[5m]).
func promql(t string) []Candidate {
	if !strings.ContainsAny(t, "([{") {
		return nil
	}
	var out []Candidate
	for _, m := range reMetricIn.FindAllString(t, -1) {
		out = append(out, Candidate{Kind: model.KindMetric, Text: m, Confidence: model.ConfLow})
	}
	return out
}

var rePathWord = regexp.MustCompile(`(?:^|[^A-Za-z0-9_./@+*-])((?:\.{1,2}/)?[A-Za-z0-9_.@+-]+(?:/[A-Za-z0-9_.@*+-]+)*\.[A-Za-z0-9]+)`)

// ProsePaths finds file paths written without markup in running text ("see scripts/env.sh").
// Only tokens ending in a known extension qualify; a bare file name is low confidence.
func ProsePaths(text string) []Candidate {
	var out []Candidate
	for _, m := range rePathWord.FindAllStringSubmatchIndex(text, -1) {
		p := strings.TrimRight(text[m[2]:m[3]], ".")
		if !HasKnownExt(p) || reScheme.MatchString(p) || strings.HasPrefix(p, "www.") {
			continue
		}
		c := model.ConfMedium
		if !strings.Contains(p, "/") {
			c = model.ConfLow
		}
		out = append(out, Candidate{Kind: model.KindFilePath, Text: p, Confidence: c, Offset: m[2]})
	}
	return out
}

// Qualifier returns the identifier-like word just before offset (the reference's left context),
// e.g. "MIERAI/other" in "MIERAI/other docs/x.md".
func Qualifier(text string, offset int) string {
	if offset <= 0 || offset > len(text) {
		return ""
	}
	q := strings.TrimRight(text[:offset], " \t`'\"(:")
	i := len(q)
	for i > 0 && isQualChar(q[i-1]) {
		i--
	}
	return q[i:]
}

func isQualChar(c byte) bool {
	return c == '_' || c == '-' || c == '.' || c == '/' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
