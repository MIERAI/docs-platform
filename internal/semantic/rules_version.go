package semantic

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/semantic/evidence"
)

// semver is a release tag: an optional family prefix ("runtime/") and major.minor.patch.
type semver struct {
	Family string
	Tag    string
	N      [3]int
}

func parseSemver(s string) ([3]int, bool) {
	s = strings.TrimPrefix(s, "v")
	core, _, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	var n [3]int
	if len(parts) < 2 || len(parts) > 3 {
		return n, false
	}
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return n, false
		}
		n[i] = v
	}
	return n, true
}

func parseTags(tags []string) []semver {
	var out []semver
	for _, t := range tags {
		fam, ver := "", t
		if i := strings.LastIndexByte(t, '/'); i >= 0 {
			fam, ver = t[:i+1], t[i+1:]
		}
		if n, ok := parseSemver(ver); ok && strings.Contains(ver, ".") {
			out = append(out, semver{Family: fam, Tag: t, N: n})
		}
	}
	return out
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// ownWords are words that may precede a version of this repository itself.
var ownWords = map[string]bool{
	"": true, "since": true, "until": true, "to": true, "from": true, "in": true, "on": true, "at": true,
	"of": true, "before": true, "after": true, "as": true, "version": true, "release": true, "tag": true,
	"up": true, "with": true, "by": true, "is": true, "and": true, "or": true, "for": true, "(": true,
}

// checkVersions: a version of this repository that was never released (no such tag, inside
// the released range), or a product version that differs from the one the code pins.
func (c *checker) checkVersions(cl *claim.Claim) {
	for _, v := range cl.Values {
		if v.Kind != claim.Version {
			continue
		}
		n, ok := parseSemver(v.Text)
		if !ok {
			continue
		}
		noun := strings.ToLower(v.Noun)
		if fam, own := c.family(noun); own {
			if strings.Contains(v.Text, "-") {
				continue // pre-release and pseudo-versions are not release claims
			}
			c.unreleased(cl, v, fam, n)
			continue
		}
		c.pinnedVersion(cl, v)
	}
}

// family maps the word before a version to a tag family of this repository.
func (c *checker) family(noun string) (string, bool) {
	if ownWords[noun] {
		return "", true
	}
	for _, t := range c.tags {
		if t.Family != "" && strings.EqualFold(strings.TrimSuffix(t.Family, "/"), noun) {
			return t.Family, true
		}
	}
	return "", false
}

func (c *checker) unreleased(cl *claim.Claim, v claim.Value, fam string, n [3]int) {
	var lo, hi *semver
	for i, t := range c.tags {
		if t.Family != fam {
			continue
		}
		if t.N == n {
			return
		}
		if lo == nil || less(t.N, lo.N) {
			lo = &c.tags[i]
		}
		if hi == nil || less(hi.N, t.N) {
			hi = &c.tags[i]
		}
	}
	if lo == nil || less(n, lo.N) || less(hi.N, n) {
		return // before the first tag (other history) or after the last (not released yet)
	}
	c.add(cl, Issue{
		Rule: RuleVersionUnreleased, Subject: fam + v.Raw, Confidence: model.ConfMedium,
		Detail:     fmt.Sprintf("there is no release %s%s; released tags around it: %s … %s", fam, v.Raw, lo.Tag, hi.Tag),
		Suggestion: "Name the release that actually shipped it (see the tags).",
		Quote:      hi.Tag,
	})
}

// pinnedVersion compares "kube-prometheus-stack 65.1.0" / "golang:1.27.4" with the version the
// code pins for the same name (image tags, chart versions, constants).
func (c *checker) pinnedVersion(cl *claim.Claim, v claim.Value) {
	if len(v.Noun) < 3 {
		return
	}
	var facts []evidence.Fact
	for _, f := range c.ix.FactsNamed(v.Noun) {
		if versionOf(f) != "" && !f.Commented {
			facts = append(facts, f)
		}
	}
	if len(facts) == 0 {
		return
	}
	want := strings.TrimPrefix(v.Text, "v")
	for _, f := range facts {
		if strings.TrimPrefix(versionOf(f), "v") == want {
			return
		}
	}
	f := facts[0]
	c.add(cl, Issue{
		Rule: RuleVersionMismatch, Subject: v.Noun + "@" + want, Confidence: model.ConfMedium,
		Detail:     fmt.Sprintf("the docs say %s %s, %s pins %s", v.Noun, v.Text, f.Path, versionOf(f)),
		Suggestion: fmt.Sprintf("Update the docs to %s.", versionOf(f)),
		Quote:      c.quoteLine(f.Path, f.Line), Locations: factLocs(facts),
	})
}

func versionOf(f evidence.Fact) string {
	for _, v := range f.Vals {
		if v.Kind == claim.Version {
			return v.Text
		}
	}
	return ""
}
