package semantic

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/semantic/history"
)

func hasState(cl *claim.Claim, classes ...string) bool {
	for _, s := range cl.States {
		if slices.Contains(classes, s) {
			return true
		}
	}
	return false
}

// checkStates: the docs say "planned / in progress / not yet / TODO", but what the sentence
// names was added to the code after the sentence was written; or a document status line still
// says "in progress" while the code it describes kept changing and gained new files.
func (c *checker) checkStates(cl *claim.Claim) {
	if !c.hist.Available() {
		return
	}
	if cl.Status && hasState(cl, claim.StateInProgress, claim.StatePlanned, claim.StateNotYet) {
		c.checkStatus(cl)
		return
	}
	if !hasState(cl, claim.StatePlanned, claim.StateNotYet, claim.StateInProgress, claim.StateTodo) {
		return
	}
	at, ok := c.hist.LineChanged(c.ctx, cl.Path, cl.Line)
	if !ok {
		return
	}
	for _, id := range cl.Idents {
		p, added, ok := c.newSince(id, at)
		if !ok {
			continue
		}
		c.add(cl, Issue{
			Rule: RuleStateContradiction, Subject: id, Confidence: model.ConfMedium,
			Detail: fmt.Sprintf("the sentence says %s (written %s), but %s only exists since %s (%s, commit %s)",
				strings.Join(cl.States, "/"), day(at), id, day(added), p, short(added.ID)),
			Suggestion: "Update the state: the code it calls future work exists now.",
			Quote:      c.firstLine(p), Locations: []Location{{Path: p, Line: 1, Role: "code"}},
		})
		return
	}
}

// newSince reports whether every file defining id was added after the commit at: the
// identifier did not exist anywhere when the sentence was written.
func (c *checker) newSince(id string, at history.Commit) (string, history.Commit, bool) {
	ts := c.resolve(id)
	var first string
	var firstC history.Commit
	for _, t := range ts {
		if t.Kind == "dir" {
			return "", history.Commit{}, false
		}
		added, ok := c.hist.Added(c.ctx, t.Path)
		if !ok || added.Time <= at.Time {
			return "", history.Commit{}, false
		}
		if first == "" || added.Time < firstC.Time {
			first, firstC = t.Path, added
		}
	}
	return first, firstC, first != ""
}

// checkStatus judges a document status line against the code the whole document refers to.
func (c *checker) checkStatus(cl *claim.Claim) {
	at, ok := c.hist.LineChanged(c.ctx, cl.Path, cl.Line)
	if !ok {
		return
	}
	paths := c.codePaths(c.docIDs[cl.Path])
	if len(paths) == 0 {
		return
	}
	n := c.hist.CommitsAfter(c.ctx, at, paths)
	newer, same := c.addedAfter(at, paths)
	// stale: code it describes was added later, or the status was merged together with the
	// implementation it calls "in progress"
	if len(newer) == 0 || n < c.opt.StaleCommits && same < 2 {
		return
	}
	conf := model.ConfLow
	if n >= 2*c.opt.StaleCommits {
		conf = model.ConfMedium
	}
	locs := []Location{}
	for _, p := range newer[:min(3, len(newer))] {
		locs = append(locs, Location{Path: p, Line: 1, Role: "code"})
	}
	c.add(cl, Issue{
		Rule: RuleStatusStale, Subject: "status", Confidence: conf,
		Detail: fmt.Sprintf("status line unchanged since %s; since then %d commits changed the code this document refers to; added with or after it: %s",
			day(at), n, strings.Join(newer[:min(3, len(newer))], ", ")),
		Suggestion: "Update the status (implemented? partly? which parts remain?).",
		Quote:      c.firstLine(newer[0]), Locations: locs,
	})
}

// addedAfter lists the files added after (or in the same commit as) at; same counts the latter.
func (c *checker) addedAfter(at history.Commit, paths []string) ([]string, int) {
	var out []string
	same := 0
	for _, p := range paths {
		if c.ix.Repo.IsDir(p) {
			continue
		}
		a, ok := c.hist.Added(c.ctx, p)
		switch {
		case !ok:
		case a.ID == at.ID:
			same++
			out = append(out, p)
		case a.Time > at.Time:
			out = append(out, p)
		}
	}
	return out, same
}

// checkRoles: the docs say humans write a path, but its files carry a "generated" marker.
func (c *checker) checkRoles(cl *claim.Claim) {
	if !slices.Contains(cl.Roles, claim.RoleHuman) || slices.Contains(cl.Roles, claim.RoleGenerated) {
		return // a sentence that also talks about generation is too mixed to judge here
	}
	for _, t := range c.pathTargets(nearRole(cl)) {
		files, gen := c.under(t)
		if len(files) < 2 || float64(len(gen)) < 0.8*float64(len(files)) {
			continue
		}
		c.add(cl, Issue{
			Rule: RuleRoleContradiction, Subject: t, Confidence: model.ConfMedium,
			Detail:     fmt.Sprintf("the docs say %s is written by people, but %d of its %d files are marked generated", t, len(gen), len(files)),
			Suggestion: "Say which tool generates it and what people write instead.",
			Quote:      c.quoteLine(gen[0], c.markerLine(gen[0])), Locations: []Location{{Path: gen[0], Line: c.markerLine(gen[0]), Role: "code"}},
		})
	}
}

func (c *checker) pathTargets(ids []string) []string {
	var out []string
	for _, id := range ids {
		if !strings.Contains(id, "/") {
			continue
		}
		for _, t := range c.resolve(id) {
			if t.Kind == "dir" || t.Kind == "file" {
				out = append(out, t.Path)
			}
		}
	}
	return out
}

// under lists the code files at or below p, and those marked generated.
func (c *checker) under(p string) (files, gen []string) {
	for _, f := range c.inv.CodeFiles {
		if f == p || strings.HasPrefix(f, p+"/") {
			files = append(files, f)
			if c.ix.Generated[f] {
				gen = append(gen, f)
			}
		}
	}
	return files, gen
}

func (c *checker) markerLine(p string) int {
	for i, l := range c.ix.Lines(p) {
		if i >= 6 {
			break
		}
		if ll := strings.ToLower(l); strings.Contains(ll, "generated") || strings.Contains(ll, "do not edit") {
			return i + 1
		}
	}
	return 1
}

// checkStaleness: the code a claim names changed in N or more commits after the claim's line
// last changed. A weak, low-confidence signal on its own.
func (c *checker) checkStaleness(cl *claim.Claim, paths []string) bool {
	if !c.hist.Available() || len(paths) == 0 {
		return false
	}
	at, ok := c.hist.LineChanged(c.ctx, cl.Path, cl.Line)
	if !ok {
		return false
	}
	n := c.hist.CommitsAfter(c.ctx, at, paths)
	if n < c.opt.StaleCommits {
		return false
	}
	c.add(cl, Issue{
		Rule: RuleStaleClaim, Subject: path.Base(paths[0]), Confidence: model.ConfLow,
		Detail:     fmt.Sprintf("the line was last changed %s; %d later commits changed %s", day(at), n, strings.Join(paths[:min(3, len(paths))], ", ")),
		Suggestion: "Re-read the sentence against the current code.",
		Locations:  []Location{{Path: paths[0], Role: "code"}},
	})
	return true
}

func (c *checker) firstLine(p string) string {
	for i, l := range c.ix.Lines(p) {
		if strings.TrimSpace(l) != "" {
			return c.quoteLine(p, i+1)
		}
	}
	return ""
}

func day(cm history.Commit) string { return time.Unix(cm.Time, 0).UTC().Format("2006-01-02") }

func short(id string) string { return id[:min(7, len(id))] }

// nearRole keeps the identifiers written within 40 bytes of the "written by people" phrase.
func nearRole(cl *claim.Claim) []string {
	loc := claim.HumanRoleIndex(cl.Text)
	if loc == nil {
		return nil
	}
	var out []string
	for _, id := range cl.Idents {
		i := strings.Index(cl.Text, id)
		if i >= 0 && (abs(i-loc[1]) <= 40 || abs(loc[0]-(i+len(id))) <= 40) {
			out = append(out, id)
		}
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
