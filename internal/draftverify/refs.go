package draftverify

import (
	"fmt"
	"path"
	"strings"

	"github.com/MIERAI/docs-platform/internal/check"
	"github.com/MIERAI/docs-platform/internal/config"
	"github.com/MIERAI/docs-platform/internal/match"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/plugin"
	"github.com/MIERAI/docs-platform/internal/repo"
	"github.com/MIERAI/docs-platform/internal/scan"
	"github.com/MIERAI/docs-platform/internal/secret"
)

// referenceItems extracts the document again from its old and its new text with the
// repository's configured extractors and matches both against the repository (as docsp
// check does): a reference on a changed line that does not resolve, and did not fail before,
// fails the draft.
func referenceItems(in *Input, changes []Change) ([]Item, error) {
	changed := changedNew(changes)
	if len(changed) == 0 {
		return nil, nil
	}
	sc, err := scanRepo(in)
	if err != nil {
		return nil, err
	}
	oldRes := extractText(sc.exts, in.Path, in.Old, sc.isDoc)
	newRes := extractText(sc.exts, in.Path, in.New, sc.isDoc)
	before := map[string]bool{}
	for _, is := range match.Run(withDoc(sc.inv, in.Path, oldRes), sc.r, sc.opts).Issues {
		before[string(is.Kind)+"\x00"+is.Subject] = true
	}
	failed := map[string]bool{}
	var out []Item
	for _, is := range match.Run(withDoc(sc.inv, in.Path, newRes), sc.r, sc.opts).Issues {
		key := string(is.Kind) + "\x00" + is.Subject
		line, on := onChanged(is, in.Path, changed)
		if !on || before[key] {
			continue
		}
		failed[key] = true
		it := Item{Line: line, Kind: "reference", Subject: string(is.Kind) + " " + is.Subject, Status: Fail, Detail: "does not resolve in the repository"}
		if is.Type == match.TypeOutside {
			it.Status, it.Detail = NeedsModel, "points outside the repository: cannot be verified here"
		}
		out = append(out, it)
	}
	seen := map[string]bool{}
	for _, ref := range newRes.References {
		key := string(ref.Kind) + "\x00" + ref.Text
		if !changed[ref.Line] || failed[key] || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Item{Line: ref.Line, Kind: "reference", Subject: string(ref.Kind) + " " + ref.Text, Status: Pass, Detail: "resolves"})
	}
	return out, nil
}

// scanned is the repository as docsp check sees it.
type scanned struct {
	r     *repo.Repo
	exts  []plugin.Extractor
	inv   *scan.Inventory
	opts  match.Options
	isDoc bool
}

// scanRepo loads the repository's configuration and runs its extractors (as docsp check).
func scanRepo(in *Input) (*scanned, error) {
	cfg, err := config.Load(in.Root, "")
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	minConf, ok := model.ParseConfidence(cfg.MinConfidence)
	if !ok {
		minConf = model.ConfLow
	}
	r, err := repo.Open(in.Root, cfg.Exclude)
	if err != nil {
		return nil, err //nolint:wrapcheck // already descriptive
	}
	reg, err := plugin.NewRegistry(check.Builtins(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("extractors: %w", err)
	}
	disabled := map[string]bool{}
	for _, d := range cfg.Extractors.Disable {
		disabled[d] = true
	}
	exts := reg.Enabled(disabled)
	return &scanned{
		r: r, exts: exts, inv: scan.Run(r, exts, scan.Options{Docs: cfg.Docs}), isDoc: repo.MatchAny(cfg.Docs, in.Path),
		opts: match.Options{
			MinConfidence: minConf, Ignore: cfg.Ignore, Reverse: config.Reverse{Disabled: true},
			GitIgnore: gitignore(r), RelatedRepos: cfg.RelatedRepos,
		},
	}, nil
}

func onChanged(is match.Issue, p string, changed map[int]bool) (int, bool) {
	for _, l := range is.Locations {
		if l.Path == p && changed[l.Line] {
			return l.Line, true
		}
	}
	return 0, false
}

// extractText runs the extractors that would read the document on the given text.
func extractText(exts []plugin.Extractor, p, text string, isDoc bool) model.Result {
	var res model.Result
	f := plugin.File{Path: p, Content: []byte(text)}
	for _, e := range exts {
		if (e.Info().Side == plugin.SideDocs) != isDoc || !e.Accepts(p) {
			continue
		}
		if r, err := safeExtract(e, f); err == nil {
			res.Merge(r)
		}
	}
	return res
}

func safeExtract(e plugin.Extractor, f plugin.File) (r model.Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("extractor %s panicked: %v", e.Info().Name, p)
		}
	}()
	return e.Extract(f) //nolint:wrapcheck // the caller drops the error
}

// withDoc is the inventory with the document's own extraction replaced by res.
func withDoc(inv *scan.Inventory, p string, res model.Result) *scan.Inventory {
	out := &scan.Inventory{DocFiles: inv.DocFiles, CodeFiles: inv.CodeFiles, CodeTokens: inv.CodeTokens, Gaps: inv.Gaps}
	out.Mentions = inv.Mentions
	for _, s := range inv.Symbols {
		if s.Path != p {
			out.Symbols = append(out.Symbols, s)
		}
	}
	for _, r := range inv.References {
		if r.Path != p {
			out.References = append(out.References, r)
		}
	}
	for _, s := range inv.Suppressions {
		if s.Path != p {
			out.Suppressions = append(out.Suppressions, s)
		}
	}
	out.Merge(res)
	return out
}

// gitignore collects the patterns of every .gitignore relative to the root (as docsp check
// does: an ignored path is generated, not missing).
func gitignore(r *repo.Repo) []string {
	var pats []string
	for _, f := range r.ByBase(".gitignore") {
		b, err := r.Read(f)
		if err != nil {
			continue
		}
		dir := path.Dir(f)
		for _, l := range secret.Lines(b) {
			if l[0] == '!' {
				continue
			}
			switch {
			case dir == ".":
				pats = append(pats, l)
			case !strings.Contains(strings.TrimSuffix(l, "/"), "/"):
				pats = append(pats, dir+"/**/"+l)
			default:
				pats = append(pats, dir+"/"+strings.TrimPrefix(l, "/"))
			}
		}
	}
	return pats
}
