package markdown

import (
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/refclass"
)

// Version of this extractor; bump on any behaviour change.
const Version = "0.1.0"

// Name of this extractor.
const Name = "markdown"

// Extractor is the Markdown docs extractor.
type Extractor struct{}

// Info implements plugin.Extractor.
func (Extractor) Info() plugin.Info {
	return plugin.Info{Name: Name, Version: Version, Side: plugin.SideDocs}
}

// Accepts implements plugin.Extractor.
func (Extractor) Accepts(p string) bool {
	ext := strings.ToLower(path.Ext(p))
	return ext == ".md" || ext == ".markdown"
}

// Extract implements plugin.Extractor.
func (Extractor) Extract(f plugin.File) (model.Result, error) {
	d := Parse(f.Content)
	var r model.Result
	r.Symbols = anchors(f.Path, d)
	r.References = append(r.References, links(f.Path, d)...)
	r.References = append(r.References, spans(f.Path, d)...)
	r.References = append(r.References, blocks(f.Path, d)...)
	r.References = append(r.References, prosePaths(f.Path, d)...)
	r.Mentions = mentions(d)
	for _, s := range d.Suppressions {
		for l := s.From; l <= s.To; l++ {
			r.Suppressions = append(r.Suppressions, model.Suppression{Path: f.Path, Line: l, Reason: s.Reason})
		}
	}
	return r, nil
}

func anchors(p string, d *Doc) []model.Symbol {
	sl := NewSlugger()
	out := make([]model.Symbol, 0, len(d.Headings)+len(d.Anchors))
	for _, h := range d.Headings {
		out = append(out, anchorSym(p, sl.Slug(h.Text), h.Line))
	}
	for _, a := range d.Anchors {
		out = append(out, anchorSym(p, a.ID, a.Line))
	}
	return out
}

func anchorSym(p, id string, line int) model.Symbol {
	return model.Symbol{Kind: model.KindAnchor, Name: p + "#" + id, Path: p, Line: line, Confidence: model.ConfHigh, Source: Name}
}

func ref(p string, line int, c refclass.Candidate, ctx string) model.Reference {
	return model.Reference{
		Kind: c.Kind, Text: c.Text, Program: c.Program, Path: p, Line: line,
		Confidence: c.Confidence, Context: ctx, Source: Name,
	}
}

// links resolves relative link destinations to repo-relative targets ("dir/file.md#anchor").
func links(p string, d *Doc) []model.Reference {
	var out []model.Reference
	for _, l := range d.Links {
		target, ok := resolveLink(p, l.Text)
		if !ok {
			continue
		}
		out = append(out, model.Reference{
			Kind: model.KindLink, Text: target, Path: p, Line: l.Line,
			Confidence: model.ConfHigh, Context: "link", Source: Name,
		})
	}
	return out
}

var reScheme = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)

func resolveLink(from, dest string) (string, bool) {
	if reScheme.MatchString(dest) || strings.HasPrefix(dest, "//") || strings.ContainsAny(dest, "{}<>$") {
		return "", false
	}
	file, anchor, _ := strings.Cut(dest, "#")
	file, _, _ = strings.Cut(file, "?")
	if dec, err := url.PathUnescape(file); err == nil && utf8.ValidString(dec) {
		file = dec
	}
	switch {
	case file == "":
		file = from
	case strings.HasPrefix(file, "/"):
		file = path.Clean(strings.TrimPrefix(file, "/"))
	default:
		file = path.Clean(path.Join(path.Dir(from), file))
	}
	if anchor != "" {
		return file + "#" + strings.ToLower(anchor), true
	}
	return file, true
}

func spans(p string, d *Doc) []model.Reference {
	prose := map[int]string{}
	for _, t := range d.Text {
		prose[t.Line] = t.Text
	}
	var out []model.Reference
	for _, s := range d.Spans {
		q := refclass.Qualifier(prose[s.Line], s.Col)
		for _, c := range refclass.Span(s.Text) {
			r := ref(p, s.Line, c, "code_span")
			r.Qualifier = q
			out = append(out, r)
		}
	}
	return out
}

func prosePaths(p string, d *Doc) []model.Reference {
	var out []model.Reference
	for _, t := range d.Text {
		text := reLinkSyntax.ReplaceAllStringFunc(t.Text, blankOut)
		for _, c := range refclass.ProsePaths(text) {
			r := ref(p, t.Line, c, "prose")
			r.Qualifier = refclass.Qualifier(text, c.Offset)
			out = append(out, r)
		}
	}
	return out
}

func blankOut(s string) string { return strings.Repeat(" ", len(s)) }

// reLinkSyntax removes link destinations from prose (they are checked as links).
var reLinkSyntax = regexp.MustCompile(`\]\([^)]*\)|<[a-z]+://[^>]*>|[a-z]+://\S+`)

func blocks(p string, d *Doc) []model.Reference {
	var out []model.Reference
	for _, b := range d.Blocks {
		for _, c := range refclass.Block(b.Lang, b.Lines) {
			out = append(out, ref(p, b.Line+c.Offset, c.Candidate, "code_block"))
		}
	}
	return out
}

var reToken = regexp.MustCompile(`-{0,2}[A-Za-z0-9_$][A-Za-z0-9_.\-/:]*`)

func mentions(d *Doc) []model.Mention {
	seen := map[model.Mention]bool{}
	add := func(s string, code bool) {
		for _, t := range reToken.FindAllString(s, -1) {
			t = strings.TrimRight(t, ".:/-")
			if t != "" {
				seen[model.Mention{Token: t, InCode: code}] = true
			}
		}
	}
	for _, t := range d.Text {
		add(t.Text, false)
	}
	for _, s := range d.Spans {
		add(s.Text, true)
	}
	for _, b := range d.Blocks {
		add(strings.Join(b.Lines, "\n"), true)
	}
	out := make([]model.Mention, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	return out
}
