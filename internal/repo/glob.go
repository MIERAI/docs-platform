package repo

import (
	"path"
	"strings"
)

// Match reports whether a slash-separated repo-relative path matches a glob pattern.
// Supported: path.Match syntax per segment, "**" for any number of segments, a pattern
// without "/" matches the base name anywhere (gitignore-like), a trailing "/" matches a
// directory prefix.
func Match(pattern, p string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/") {
		dir := strings.TrimSuffix(pattern, "/")
		return matchSegs(strings.Split(dir, "/"), strings.Split(strings.TrimSuffix(p, "/"), "/"), true)
	}
	if !strings.Contains(pattern, "/") {
		ok, err := path.Match(pattern, path.Base(p))
		return err == nil && ok
	}
	return matchSegs(strings.Split(strings.TrimPrefix(pattern, "/"), "/"), strings.Split(p, "/"), false)
}

// MatchAny reports whether any pattern matches p.
func MatchAny(patterns []string, p string) bool {
	for _, pat := range patterns {
		if Match(pat, p) {
			return true
		}
	}
	return false
}

// matchSegs matches pattern segments against path segments; prefix=true accepts a path that
// continues below a fully matched pattern (directory patterns).
func matchSegs(pat, segs []string, prefix bool) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(segs); i++ {
				if matchSegs(pat[1:], segs[i:], prefix) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0 || prefix
}
