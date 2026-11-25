package judge

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/MIERAI/docs-platform/judgev1"
)

// MinQuoteBytes is the shortest quote accepted as evidence.
const MinQuoteBytes = 10

// QuoteCheck is the result of verifying a yes answer's quote.
type QuoteCheck struct {
	OK bool
	// Quote is the verified quote (trimmed).
	Quote    string
	Evidence judgev1.Evidence
	// Line is the line number of the quote in its file (0 if unknown).
	Line   int
	Reason string
}

// VerifyQuote accepts a quote only if it appears verbatim in one evidence snippet, is long
// enough to be evidence, is not merely text copied from the claim, and — for
// claim_contradiction — is about the claim (shares an identifier word or two Han characters).
func VerifyQuote(req *judgev1.Request, quote string) QuoteCheck {
	q := strings.TrimSpace(quote)
	switch {
	case len(q) < MinQuoteBytes:
		return QuoteCheck{Reason: "quote too short"}
	case strings.Contains(req.Claim.Text, q):
		return QuoteCheck{Reason: "quote copied from the claim"}
	case req.Task.Type == judgev1.TaskClaimContradiction && !Related(q, req.Claim.Text):
		return QuoteCheck{Reason: "quote unrelated to the claim"}
	}
	for _, e := range req.Evidence {
		for i, l := range strings.Split(e.Text, "\n") {
			if strings.Contains(l, q) {
				line := 0
				if e.From > 0 {
					line = e.From + i
				}
				return QuoteCheck{OK: true, Quote: q, Evidence: e, Line: line}
			}
		}
	}
	return QuoteCheck{Reason: "quote not found verbatim in one evidence line"}
}

var (
	reWord    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,40}`)
	reHanPair = regexp.MustCompile(`\p{Han}{2}`)
)

// Related reports whether a and b share an identifier word (≥ 4 letters, also after splitting
// snake_case and camelCase) or a pair of Han characters.
func Related(a, b string) bool {
	al := strings.ToLower(a)
	for _, w := range Words(b) {
		if strings.Contains(al, w) {
			return true
		}
	}
	rs := []rune(b)
	for i := 0; i+1 < len(rs); i++ {
		if p := string(rs[i : i+2]); reHanPair.MatchString(p) && strings.Contains(a, p) {
			return true
		}
	}
	return false
}

// Words returns the lower-cased identifier words of s (≥ 4 letters), including the parts of
// snake_case and camelCase identifiers.
func Words(s string) []string {
	var out []string
	for _, w := range reWord.FindAllString(s, -1) {
		out = append(out, strings.ToLower(w))
		for _, p := range splitIdent(w) {
			if len(p) >= 4 && p != strings.ToLower(w) {
				out = append(out, p)
			}
		}
	}
	return out
}

func splitIdent(w string) []string {
	var parts []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(w)
	for i, r := range rs {
		switch {
		case r == '_' || unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && unicode.IsLower(rs[i-1]):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return parts
}
