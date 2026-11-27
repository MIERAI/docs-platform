package claim

import (
	"regexp"
	"strings"
)

var (
	reSep       = regexp.MustCompile(`^(?:\s|,|，|、|/|\||\+|;|；|and|or|和|与|及|或|と)*$`)
	rePipeList  = regexp.MustCompile(`[A-Za-z][\w.-]*(?:\s?\|\s?[A-Za-z][\w.-]*){2,}`)
	reSlashList = regexp.MustCompile(`(?:^|[^\w./-])([a-z][a-z0-9-]{1,30}(?:/[a-z][a-z0-9-]{1,30}){3,})(?:$|[^\w./-])`)
	reCommaList = regexp.MustCompile(`[A-Za-z][\w.-]{1,40}(?:\s?(?:,|、|，)\s?(?:and |or )?[A-Za-z][\w.-]{1,40}){2,}`)
	reEnv       = regexp.MustCompile(`\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b`)
	reFlag      = regexp.MustCompile(`(?:^|\s)(--?[a-z][a-z0-9-]+)`)
	rePathish   = regexp.MustCompile(`(?:^|[\s(（:：])((?:\.{0,2}/)?[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.*<>{}-]+)+/?|[A-Za-z0-9_-]+\.(?:go|py|sh|ya?ml|json|tf|md|toml|sql|js|ts|proto|mk))(?:$|[\s),.，。、:：）])`)
)

// spanList returns the code spans written as an enumeration (`a`, `b` and `c`).
func spanList(s string) []string {
	idx := reSpan.FindAllStringIndex(s, -1)
	var best, cur []string
	for i, m := range idx {
		item := s[m[0]+1 : m[1]-1]
		if i > 0 && reSep.MatchString(s[idx[i-1][1]:m[0]]) {
			cur = append(cur, item)
		} else {
			cur = []string{item}
		}
		if len(cur) > len(best) {
			best = append([]string(nil), cur...)
		}
	}
	return best
}

func listIn(s string, _ []string) (List, bool) {
	items := spanList(s)
	plain := stripSpans(s)
	if len(items) < 2 {
		items = nil
		for _, re := range []*regexp.Regexp{rePipeList, reCommaList} {
			if m := re.FindString(plain); m != "" {
				items = splitItems(m)
				break
			}
		}
		if m := reSlashList.FindStringSubmatch(plain); items == nil && m != nil {
			items = strings.Split(m[1], "/")
		}
	}
	items = normItems(items)
	if len(items) < 2 {
		return List{}, false
	}
	return List{Items: items, Open: reOpen.MatchString(plain), Closed: reClosed.MatchString(plain)}, true
}

func splitItems(m string) []string {
	return strings.FieldsFunc(m, func(r rune) bool { return strings.ContainsRune(",、，|", r) })
}

// NormItem normalizes a list item or a set member for comparison.
func NormItem(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "and ")
	s = strings.TrimPrefix(s, "or ")
	s = strings.Trim(s, "`*\"' ")
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimSuffix(s, "/")
	return strings.ToLower(s)
}

func normItems(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range in {
		n := NormItem(it)
		if n == "" || seen[n] || len(n) > 80 {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// idents collects identifiers written in the sentence.
func idents(s string, spans []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(x string) {
		x = identKey(x)
		if len(x) < 2 || len(x) > 200 || seen[x] {
			return
		}
		seen[x] = true
		out = append(out, x)
	}
	for _, sp := range spans {
		add(sp)
	}
	plain := stripSpans(s)
	for _, m := range reEnv.FindAllString(plain, -1) {
		add(m)
	}
	for _, m := range reFlag.FindAllStringSubmatch(plain, -1) {
		add(m[1])
	}
	for _, m := range rePathish.FindAllStringSubmatch(plain, -1) {
		if !strings.Contains(m[1], "://") {
			add(m[1])
		}
	}
	return out
}

// identKey is an identifier as Claim.Idents holds it (quotes, brackets, punctuation trimmed).
func identKey(x string) string { return strings.Trim(strings.TrimSpace(x), "`\"'.,:;()（）") }
