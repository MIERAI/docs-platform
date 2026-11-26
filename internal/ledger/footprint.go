package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// Footprint rules beyond the validated evidence. They only send a claim back to the judge
// (stale with a footprint reason); they never decide a verdict. All are off by default:
//
//   - weak: the path- or glob-scoped searches the agent ran while judging (search, grep,
//     glob with a scope narrower than the whole repository); a file added, changed or
//     removed inside one of them re-opens the claim. Whole-repository searches are not a
//     footprint (they would fire on every commit).
//   - siblings: for the document and each quoted file, the same path with one directory
//     level wildcarded (clusters/lab/x.yaml -> clusters/*/x.yaml, */lab/x.yaml); a newly
//     ADDED match re-opens the claim (a statement about the lab goes stale when the laptop
//     gets the same file).
//   - members: for an enumerating claim (a list of components, jobs, apps ...), the
//     directories it listed and the globs it expanded; an entry added to or removed from
//     one of those sets re-opens the claim (the list may now be incomplete).
type Rules struct {
	Weak     bool `json:"weak,omitempty"`
	Siblings bool `json:"siblings,omitempty"`
	Members  bool `json:"members,omitempty"`
}

// Rule names.
const (
	RuleWeak     = "weak"
	RuleSiblings = "siblings"
	RuleMembers  = "members"
)

// Reason kinds of the footprint rules.
const (
	ReasonWeakScope   = "weak_scope_changed"
	ReasonSiblingAdd  = "sibling_added"
	ReasonMemberSet   = "member_set_changed"
	maxFootprintItems = 20
)

// ParseRules reads "weak,siblings,members", "all" or "" (none).
func ParseRules(s string) (Rules, error) {
	var r Rules
	for _, f := range strings.Split(s, ",") {
		switch strings.TrimSpace(f) {
		case "":
		case "all":
			r = Rules{Weak: true, Siblings: true, Members: true}
		case RuleWeak:
			r.Weak = true
		case RuleSiblings:
			r.Siblings = true
		case RuleMembers:
			r.Members = true
		default:
			return r, fmt.Errorf("footprint rule %q: want weak, siblings, members or all", f)
		}
	}
	return r, nil
}

// Any reports whether any rule is on.
func (r Rules) Any() bool { return r.Weak || r.Siblings || r.Members }

// String lists the rules that are on.
func (r Rules) String() string {
	var out []string
	for _, x := range []struct {
		on   bool
		name string
	}{{r.Weak, RuleWeak}, {r.Siblings, RuleSiblings}, {r.Members, RuleMembers}} {
		if x.on {
			out = append(out, x.name)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, "+")
}

// Scope is a scoped search of the agent (weak footprint).
type Scope struct {
	Tool  string `json:"tool"`
	Scope string `json:"scope"`
}

// Member is a set an enumerating claim was compared with: a listed directory or a glob.
type Member struct {
	Dir  string `json:"dir,omitempty"`
	Glob string `json:"glob,omitempty"`
}

// stepArgs are the arguments of the tools the footprints read.
type stepArgs struct {
	Path    string `json:"path"`
	Scope   string `json:"scope"`
	Pattern string `json:"pattern"`
}

// Footprints derives the weak scopes (rules.Weak) and, for an enumerating claim, the
// member sets (rules.Members) from a judgment's tool transcript. Failed steps do not count.
func Footprints(steps []judgev1.Step, rules Rules, enumerating bool) ([]Scope, []Member) {
	var weak []Scope
	var members []Member
	seenW, seenM := map[Scope]bool{}, map[Member]bool{}
	for _, st := range steps {
		if st.Error != "" {
			continue
		}
		var a stepArgs
		if json.Unmarshal(st.Args, &a) != nil {
			continue
		}
		if rules.Weak {
			if s, ok := weakScope(st.Tool, a); ok && !seenW[s] && len(weak) < maxFootprintItems {
				seenW[s] = true
				weak = append(weak, s)
			}
		}
		if rules.Members && enumerating {
			if m, ok := memberSet(st.Tool, a); ok && !seenM[m] && len(members) < maxFootprintItems {
				seenM[m] = true
				members = append(members, m)
			}
		}
	}
	return weak, members
}

func weakScope(tool string, a stepArgs) (Scope, bool) {
	var sc string
	switch tool {
	case judgev1.ToolSearch:
		sc = a.Scope
	case judgev1.ToolGrep:
		sc = a.Path
	case judgev1.ToolGlob:
		sc = a.Pattern
	default:
		return Scope{}, false
	}
	sc = strings.TrimPrefix(strings.TrimSpace(sc), "./")
	if strings.Trim(sc, "*/.") == "" { // the whole repository
		return Scope{}, false
	}
	return Scope{Tool: tool, Scope: sc}, true
}

func memberSet(tool string, a stepArgs) (Member, bool) {
	switch tool {
	case judgev1.ToolListDir:
		d := strings.Trim(strings.TrimPrefix(strings.TrimSpace(a.Path), "./"), "/")
		if d == "" {
			d = "."
		}
		return Member{Dir: d}, true
	case judgev1.ToolGlob:
		if g := strings.TrimPrefix(strings.TrimSpace(a.Pattern), "./"); strings.Trim(g, "*/.") != "" {
			return Member{Glob: g}, true
		}
	}
	return Member{}, false
}

var reEnumeration = regexp.MustCompile(`[,、]|\s/\s|\|`)

// Enumerating reports whether a claim enumerates members: a fact about an incomplete list,
// or a line with at least two separators (commas, "、", " / ", table cells).
func Enumerating(c *Claim) bool {
	for _, f := range c.Facts {
		if f.Status == "incomplete_list" {
			return true
		}
	}
	return len(reEnumeration.FindAllString(c.Text, -1)) >= 2
}

// footprintReasons applies the rules that are on to one change set.
func footprintReasons(c *Claim, ch *Changes, r Rules) []Reason {
	var rs []Reason
	if r.Weak {
		rs = append(rs, weakReasons(c, ch)...)
	}
	if r.Siblings {
		rs = append(rs, siblingReasons(c, ch)...)
	}
	if r.Members {
		rs = append(rs, memberReasons(c, ch)...)
	}
	return rs
}

func weakReasons(c *Claim, ch *Changes) []Reason {
	for _, s := range c.Evidence.Weak {
		m, err := judge.CompileScope(s.Scope)
		if err != nil {
			continue
		}
		for _, p := range sortedPaths(ch) {
			if p != c.Path && m(p) {
				return []Reason{{Kind: ReasonWeakScope, Path: p, Detail: s.Tool + " " + s.Scope}}
			}
		}
	}
	return nil
}

// siblingGlobs are the claim's document and quoted files with one directory level
// wildcarded, keyed by glob, valued by the path they come from.
func siblingGlobs(c *Claim) map[string]string {
	paths := []string{c.Path}
	for _, q := range c.Evidence.Quotes {
		paths = append(paths, q.Path)
	}
	out := map[string]string{}
	for _, p := range paths {
		seg := strings.Split(p, "/")
		for i := 0; i < len(seg)-1; i++ {
			g := append(append(append([]string{}, seg[:i]...), "*"), seg[i+1:]...)
			out[strings.Join(g, "/")] = p
		}
	}
	return out
}

func siblingReasons(c *Claim, ch *Changes) []Reason {
	globs := siblingGlobs(c)
	keys := make([]string, 0, len(globs))
	for g := range globs {
		keys = append(keys, g)
	}
	sort.Strings(keys)
	for _, p := range sortedPaths(ch) {
		if ch.Paths[p] != 'A' {
			continue
		}
		for _, g := range keys {
			if p != globs[g] && segmentMatch(g, p) {
				return []Reason{{Kind: ReasonSiblingAdd, Path: p, Detail: "sibling of " + globs[g]}}
			}
		}
	}
	return nil
}

// segmentMatch matches a slash-separated glob segment by segment (same depth).
func segmentMatch(g, p string) bool {
	gs, ps := strings.Split(g, "/"), strings.Split(p, "/")
	if len(gs) != len(ps) {
		return false
	}
	for i := range gs {
		if ok, err := path.Match(gs[i], ps[i]); err != nil || !ok {
			return false
		}
	}
	return true
}

func memberReasons(c *Claim, ch *Changes) []Reason {
	for _, m := range c.Evidence.Members {
		if m.Dir != "" {
			if p, ok := entryChanged(ch, m.Dir); ok {
				return []Reason{{Kind: ReasonMemberSet, Path: p, Detail: "listed " + m.Dir}}
			}
			continue
		}
		sc, err := judge.CompileScope(m.Glob)
		if err != nil {
			continue
		}
		for _, p := range sortedPaths(ch) {
			if st := ch.Paths[p]; (st == 'A' || st == 'D') && sc(p) {
				return []Reason{{Kind: ReasonMemberSet, Path: p, Detail: "glob " + m.Glob}}
			}
		}
	}
	return nil
}

// entryChanged finds a change that adds a new entry to directory d (a file directly in it,
// or a directory below it that did not exist before), or removes a file directly in it.
func entryChanged(ch *Changes, d string) (string, bool) {
	pre := d + "/"
	if d == "." {
		pre = ""
	}
	for _, p := range sortedPaths(ch) {
		st := ch.Paths[p]
		if !strings.HasPrefix(p, pre) || (st != 'A' && st != 'D') {
			continue
		}
		rest := strings.TrimPrefix(p, pre)
		entry, below, isDir := strings.Cut(rest, "/")
		switch {
		case !isDir:
			return p, true // a file of the directory added or removed
		case st == 'A' && below != "" && !ch.existedBefore(pre+entry):
			return p, true // a new directory entry
		}
	}
	return "", false
}

// existedBefore reports whether p was a file or directory at the change set's base
// (read lazily; without a repository it answers false).
func (c *Changes) existedBefore(p string) bool {
	if c.dir == "" {
		return false
	}
	if c.before == nil {
		t, err := ReadTree(c.ctx, c.dir, c.From)
		if err != nil {
			t = &Tree{Blobs: map[string]string{}, dirs: map[string]bool{}}
		}
		c.before = t
	}
	return c.before.Exists(p)
}

var errNoTranscript = errors.New("no transcript")

// AddFootprints records the weak and member footprints of a claim from a transcript
// (also usable on an existing ledger, from eval results).
func AddFootprints(c *Claim, steps []judgev1.Step, r Rules) error {
	if len(steps) == 0 {
		return errNoTranscript
	}
	c.Evidence.Weak, c.Evidence.Members = Footprints(steps, r, Enumerating(c))
	return nil
}
