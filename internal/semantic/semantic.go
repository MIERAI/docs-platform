package semantic

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/ai"
	"github.com/MIERAI/docs-platform/internal/extract/comments"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/semantic/evidence"
	"github.com/MIERAI/docs-platform/internal/semantic/history"
)

// Version of the semantic audit (recorded in collector.tools).
const Version = "0.1.0"

// DefaultMaxQuestions bounds the claims put to the judge per run (each costs 2-3 requests).
const DefaultMaxQuestions = 150

// Options tune one audit.
type Options struct {
	// StaleCommits is how many later commits to a claim's code make the claim stale.
	StaleCommits int
	// Judge is the local model; nil means deterministic checks only (provider none).
	Judge ai.Asker
	// MaxQuestions bounds how many claims are put to the judge (each costs 2-3 requests).
	MaxQuestions int
	// Ignore reports whether a finding on path:line is suppressed by the user.
	Suppressed map[string]bool
}

// Stats describe one audit run.
type Stats struct {
	Docs, Claims, CheckableClaims int
	GitCalls                      int
	Candidates                    int // claims that could be put to the judge
	Questions                     int // claims put to the judge
	ModelRequests                 int
	QuotesRejected                int // answers discarded because the quote was not in the evidence
	Answers                       int // parsed answers
	ModelErrors                   int
	ModelSeconds                  float64
	Seconds                       float64
	ByRule                        map[string]int
}

// Result of one audit.
type Result struct {
	Issues []Issue
	Stats  Stats
}

type checker struct {
	ctx      context.Context
	ix       *evidence.Index
	hist     *history.History
	opt      Options
	inv      *scan.Inventory
	resolved map[string][]target
	issues   []Issue
	pending  []question
	stats    *Stats
	tags     []semver
	docIDs   map[string][]string
	topDirs  map[string]bool
}

// Run audits a repository.
func Run(ctx context.Context, r *repo.Repo, inv *scan.Inventory, o Options) (*Result, error) {
	start := time.Now()
	if o.StaleCommits < 1 {
		o.StaleCommits = 2
	}
	if o.MaxQuestions <= 0 {
		o.MaxQuestions = DefaultMaxQuestions
	}
	res := &Result{Stats: Stats{ByRule: map[string]int{}}}
	c := &checker{
		ctx: ctx, ix: evidence.Build(r, inv), hist: history.Open(ctx, r.Root), opt: o, inv: inv,
		resolved: map[string][]target{}, stats: &res.Stats, docIDs: docIdents(inv), topDirs: topDirs(r),
	}
	c.tags = parseTags(c.hist.Tags(ctx))
	claims := c.collect(r, inv)
	for i := range claims {
		c.check(&claims[i])
	}
	res.Stats.Candidates = len(c.pending)
	if o.Judge != nil {
		if err := c.askAll(); err != nil {
			return nil, err
		}
	}
	res.Issues = dedup(c.issues)
	for _, is := range res.Issues {
		res.Stats.ByRule[is.Rule]++
	}
	res.Stats.GitCalls = c.hist.Calls()
	res.Stats.Seconds = time.Since(start).Seconds()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("semantic audit: %w", err)
	}
	return res, nil
}

// collect extracts claims from Markdown documents and from code comments.
func (c *checker) collect(r *repo.Repo, inv *scan.Inventory) []claim.Claim {
	var out []claim.Claim
	for _, d := range inv.DocFiles {
		ext := strings.ToLower(path.Ext(d))
		if ext != ".md" && ext != ".markdown" {
			continue
		}
		b, err := r.Read(d)
		if err != nil {
			continue
		}
		c.stats.Docs++
		out = append(out, claim.Markdown(d, b)...)
	}
	for _, f := range inv.CodeFiles {
		if !(comments.Extractor{}).Accepts(f) || c.ix.Generated[f] {
			continue
		}
		var ls []claim.Line
		for _, s := range comments.Segments(f, []byte(strings.Join(c.ix.Lines(f), "\n"))) {
			ls = append(ls, claim.Line{N: s.Line, Text: s.Text})
		}
		out = append(out, claim.Comments(f, ls)...)
	}
	var kept []claim.Claim
	for _, cl := range out {
		if !c.opt.Suppressed[fmt.Sprintf("%s:%d", cl.Path, cl.Line)] {
			kept = append(kept, cl)
		}
	}
	c.stats.CheckableClaims = len(kept)
	c.stats.Claims = len(out)
	return kept
}

// check runs every deterministic rule on one claim; what stays undecided may become a question.
func (c *checker) check(cl *claim.Claim) {
	before := len(c.issues)
	c.checkValues(cl)
	c.checkSchedules(cl)
	c.checkVersions(cl)
	c.checkLists(cl)
	c.checkStates(cl)
	c.checkRoles(cl)
	if len(c.issues) > before || cl.Status {
		return
	}
	paths := c.codePaths(cl.Idents)
	stale := c.checkStaleness(cl, paths)
	if !stale && len(cl.States) == 0 && len(cl.Values) == 0 && len(cl.Lists) == 0 {
		return
	}
	if len(paths) == 0 {
		paths = c.ix.Search(claimTokens(cl.Text), maxSnippets)
	}
	if len(paths) > 0 {
		c.addQuestion(cl, paths, stale)
	}
}

var reClaimToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,40}`)

// claimTokens are the identifier-like words of a claim, split like code identifiers.
func claimTokens(text string) []string {
	var out []string
	for _, w := range reClaimToken.FindAllString(text, -1) {
		out = append(out, strings.ToLower(w))
		for _, p := range evidence.Words(w) {
			if len(p) >= 4 && !stopKeywords[p] {
				out = append(out, p)
			}
		}
	}
	return out
}

func (c *checker) add(cl *claim.Claim, is Issue) {
	is.Path, is.Line, is.Claim = cl.Path, cl.Line, cl.Text
	is.Locations = append([]Location{{Path: cl.Path, Line: cl.Line, Role: "doc"}}, is.Locations...)
	c.issues = append(c.issues, is)
}

// docIdents collects, per document, every identifier layer ③ found in it.
func docIdents(inv *scan.Inventory) map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, r := range inv.References {
		k := r.Path + "\x00" + r.Text
		if seen[k] || r.Kind == model.KindLink || r.Kind == model.KindAnchor {
			continue
		}
		seen[k] = true
		out[r.Path] = append(out[r.Path], r.Text)
	}
	return out
}

func topDirs(r *repo.Repo) map[string]bool {
	out := map[string]bool{}
	for _, f := range r.AllFiles() {
		if d, _, ok := strings.Cut(f, "/"); ok {
			out[strings.ToLower(d)] = true
		}
	}
	return out
}

// dedup keeps one issue per (document line, rule), and drops the staleness signal on lines
// that already carry a stronger finding.
func dedup(in []Issue) []Issue {
	strong := map[string]bool{}
	for _, is := range in {
		if is.Rule != RuleStaleClaim {
			strong[fmt.Sprintf("%s:%d", is.Path, is.Line)] = true
		}
	}
	seen := map[string]bool{}
	var out []Issue
	for _, is := range in {
		at := fmt.Sprintf("%s:%d", is.Path, is.Line)
		k := at + "\x00" + is.Rule + "\x00" + is.Subject
		if seen[k] || is.Rule == RuleStaleClaim && strong[at] {
			continue
		}
		seen[k] = true
		out = append(out, is)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// quoteLine returns the trimmed text of a code line (a deterministic rule's verbatim quote).
func (c *checker) quoteLine(p string, n int) string {
	ls := c.ix.Lines(p)
	if n < 1 || n > len(ls) {
		return ""
	}
	return truncate(strings.TrimSpace(ls[n-1]), 300)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
