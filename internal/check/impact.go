package check

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/extract/comments"
	"github.com/MIERAI/docs-platform/internal/impact"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/judgev1"
)

// DefaultMaxJudgedSections bounds how many affected sections are put to the judge per change.
const DefaultMaxJudgedSections = 20

// Evidence limits for change judgements.
const (
	maxSectionChars = 8000
	maxHunkChars    = 6000
	maxHunkFiles    = 3
)

// ImpactOptions configure the derivation of affected doc sections for one change.
type ImpactOptions struct {
	// Tree is the directory whose documentation and code are read (the head of the change;
	// normally the working tree of Root).
	Tree       string
	ConfigFile string
	RepoName   string
	// Diffs is the change (from repo.DiffHunks).
	Diffs []repo.FileDiff
	// Judge and Draft are the guarded providers (nil = none).
	Judge, Draft *judge.Guard
	MaxJudge     int
}

// DeriveImpact lists the doc sections a change affects, from layer ③'s reference graph
// (section → paths and identifiers it mentions), plus the explicit .docsp.yaml rules as an
// override, and — when a judge is configured — asks it whether the change contradicts each
// section and, for a verified yes, asks the draft provider for a proposed edit.
func DeriveImpact(ctx context.Context, o ImpactOptions) (*ImpactResult, error) {
	cfg, err := config.Load(o.Tree, o.ConfigFile)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	r, inv, err := scanTree(o.Tree, cfg)
	if err != nil {
		return nil, err
	}
	secs, text := Sections(r, inv)
	res := impact.Derive(impact.Attach(secs, inv.References), o.Diffs)
	res = append(res, ruleSections(cfg, r, inv.DocFiles, o.Diffs, res)...)
	if o.Judge != nil {
		judgeSections(ctx, o, res, text)
	}
	return &ImpactResult{Sections: res, Total: len(secs), Lines: text}, nil
}

// ImpactResult is the derived impact of one change.
type ImpactResult struct {
	Sections []impact.SectionResult
	// Total is the number of sections in the tree (the denominator of "how much to re-read").
	Total int
	// Lines are the lines of every sectioned file.
	Lines map[string][]string
}

// ScanTree opens a directory read-only and runs the built-in extractors on it.
func ScanTree(dir, configFile string) (*repo.Repo, *scan.Inventory, error) {
	cfg, err := config.Load(dir, configFile)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // already descriptive
	}
	return scanTree(dir, cfg)
}

func scanTree(dir string, cfg *config.Config) (*repo.Repo, *scan.Inventory, error) {
	r, err := repo.Open(dir, cfg.Exclude)
	if err != nil {
		return nil, nil, err //nolint:wrapcheck // already descriptive
	}
	exts, err := extractors(cfg)
	if err != nil {
		return nil, nil, err
	}
	return r, scan.Run(r, exts, scan.Options{Docs: cfg.Docs}), nil
}

// Sections splits every Markdown document into heading sections and every code file with
// comment references into comment blocks. It also returns each file's lines.
func Sections(r *repo.Repo, inv *scan.Inventory) ([]impact.Section, map[string][]string) {
	text := map[string][]string{}
	var secs []impact.Section
	for _, d := range inv.DocFiles {
		ext := strings.ToLower(path.Ext(d))
		if ext != ".md" && ext != ".markdown" {
			continue
		}
		b, err := r.Read(d)
		if err != nil {
			continue
		}
		text[d] = strings.Split(string(b), "\n")
		secs = append(secs, impact.MarkdownSections(d, text[d])...)
	}
	commented := map[string]bool{}
	for _, ref := range inv.References {
		if ref.Context == "comment" {
			commented[ref.Path] = true
		}
	}
	files := make([]string, 0, len(commented))
	for f := range commented {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := r.Read(f)
		if err != nil {
			continue
		}
		text[f] = strings.Split(string(b), "\n")
		var lines []int
		first := map[int]string{}
		for _, s := range comments.Segments(f, b) {
			lines = append(lines, s.Line)
			first[s.Line] = s.Text
		}
		secs = append(secs, impact.CommentSections(f, dedupInts(lines), first)...)
	}
	return secs, text
}

func dedupInts(in []int) []int {
	sort.Ints(in)
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// ruleSections adds one whole-document entry for every explicit impact rule the change
// triggers (the declarative map stays authoritative), unless the document is already listed.
// Only scanned documentation files (configured docs, minus excludes) are candidates.
func ruleSections(cfg *config.Config, r *repo.Repo, docs []string, diffs []repo.FileDiff, have []impact.SectionResult) []impact.SectionResult {
	listed := map[string]bool{}
	for _, s := range have {
		listed[s.Path] = true
	}
	changed := make([]string, 0, len(diffs))
	for _, d := range diffs {
		changed = append(changed, d.Name())
	}
	var out []impact.SectionResult
	for _, rule := range cfg.Impact.Rules {
		res := impact.Evaluate(impact.Input{Changed: changed, Cfg: config.Impact{Rules: []config.ImpactRule{rule}}})
		if len(res.Rules) == 0 || res.Rules[0].Status == impact.NotTriggered {
			continue
		}
		for _, f := range docs {
			if listed[f] || !repo.MatchAny(rule.Docs, f) {
				continue
			}
			listed[f] = true
			lines := 1
			if b, err := r.Read(f); err == nil {
				lines = max(1, strings.Count(string(b), "\n"))
			}
			st := impact.SectionReread
			if res.Rules[0].Status == impact.DocsUpdated && slices.Contains(res.Rules[0].DocsChanged, f) {
				st = impact.SectionUpdated
			}
			out = append(out, impact.SectionResult{
				Section: impact.Section{Path: f, Heading: "(whole document)", From: 1, To: lines},
				Status:  st,
				Reasons: []impact.Reason{{Kind: impact.ReasonRule, Subject: rule.Name, Changed: strings.Join(res.Rules[0].CodeChanged, ", ")}},
			})
		}
	}
	return out
}

// judgeSections asks the judge about each re-read section (bounded), then the draft provider
// for the verified yes answers. Budget exhaustion or refusal stops quietly: the deterministic
// list stands on its own.
func judgeSections(ctx context.Context, o ImpactOptions, res []impact.SectionResult, text map[string][]string) {
	limit := o.MaxJudge
	if limit <= 0 {
		limit = DefaultMaxJudgedSections
	}
	for i := range res {
		s := &res[i]
		if s.Status != impact.SectionReread || limit == 0 {
			continue
		}
		limit--
		cl := judgev1.Claim{Repo: o.RepoName, Path: s.Path, Line: s.From, Text: sectionText(text[s.Path], s.Section)}
		ev := hunkEvidence(o.RepoName, o.Diffs, s.Reasons)
		if cl.Text == "" || len(ev) == 0 {
			continue
		}
		req := judge.NewRequest(fmt.Sprintf("impact-%d", i), judgev1.TaskChangeContradiction, cl, ev, "")
		out, err := o.Judge.Judge(ctx, req)
		s.Judgement = &impact.Judgement{Model: o.Judge.Name(), PromptHash: req.Prompt.Hash, Verdict: out.Verdict, QuoteRejected: out.QuoteRejected}
		if err != nil {
			s.Judgement.Verdict, s.Judgement.Error = judgev1.Unknown, err.Error()
			continue
		}
		s.Judgement.Quote = out.Quote.Quote
		if out.Verdict == judgev1.Yes && o.Draft != nil {
			s.Draft = draft(ctx, o.Draft, cl, req.Evidence, out.Quote.Quote, i)
		}
	}
}

func draft(ctx context.Context, g *judge.Guard, cl judgev1.Claim, ev []judgev1.Evidence, quote string, i int) string {
	req := judge.NewRequest(fmt.Sprintf("draft-%d", i), judgev1.TaskDraftEdit, cl, ev, quote)
	out, err := g.Judge(ctx, req)
	if err != nil || out.Response == nil || out.Response.Draft == nil {
		return ""
	}
	return out.Response.Draft.Text
}

func sectionText(lines []string, s impact.Section) string {
	if s.From < 1 || s.From > len(lines) {
		return ""
	}
	to := min(s.To, len(lines))
	t := strings.Join(lines[s.From-1:to], "\n")
	if len(t) > maxSectionChars {
		t = strings.ToValidUTF8(t[:maxSectionChars], "")
	}
	return t
}

// hunkEvidence renders the changed lines of the files named in the reasons as evidence.
func hunkEvidence(repoName string, diffs []repo.FileDiff, reasons []impact.Reason) []judgev1.Evidence {
	want := map[string]bool{}
	for _, r := range reasons {
		f, _, _ := strings.Cut(r.Changed, ":")
		want[f] = true
	}
	var out []judgev1.Evidence
	for _, d := range diffs {
		if !want[d.Name()] || len(out) == maxHunkFiles {
			continue
		}
		var b strings.Builder
		for _, l := range d.Removed {
			fmt.Fprintf(&b, "-%s\n", l.Text)
		}
		for _, l := range d.Added {
			fmt.Fprintf(&b, "+%s\n", l.Text)
		}
		t := b.String()
		if len(t) > maxHunkChars {
			t = strings.ToValidUTF8(t[:maxHunkChars], "")
		}
		out = append(out, judgev1.Evidence{Repo: repoName, Path: d.Name(), Text: t})
	}
	return out
}
