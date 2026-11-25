package match

import (
	"path"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/scan"
)

type aggregator struct {
	byKey map[string]*Issue
	order []string
}

func newAggregator() *aggregator { return &aggregator{byKey: map[string]*Issue{}} }

func (a *aggregator) addRef(r model.Reference, st Status, conf model.Confidence, ix *index) {
	typ := TypeWrong
	if st == Outside {
		typ = TypeOutside
	}
	key := strings.Join([]string{typ, r.Path, string(r.Kind), r.Text, r.Program}, "\x00")
	is, ok := a.byKey[key]
	if !ok {
		is = &Issue{Type: typ, Kind: r.Kind, Subject: r.Text, Program: r.Program, Confidence: conf, Anchor: r.Path}
		if typ == TypeWrong {
			is.Suggestion = ix.nearest(r.Kind, r.Text)
		}
		a.byKey[key] = is
		a.order = append(a.order, key)
	}
	is.Confidence = max(is.Confidence, conf)
	is.Locations = append(is.Locations, Location{Path: r.Path, Line: r.Line, Role: "doc"})
}

func (a *aggregator) issues() []Issue {
	out := make([]Issue, 0, len(a.order))
	for _, k := range a.order {
		is := a.byKey[k]
		sort.Slice(is.Locations, func(i, j int) bool { return is.Locations[i].Line < is.Locations[j].Line })
		out = append(out, *is)
	}
	return out
}

// defaultReverseKinds are the public symbol kinds that must be documented by default.
var defaultReverseKinds = []model.Kind{
	model.KindCLICommand, model.KindCLIFlag, model.KindEnvVar, model.KindMetric,
	model.KindWorkflowInput, model.KindConfigKey,
}

// docMentions are the tokens documentation mentions, split by whether they were in code.
type docMentions struct {
	any, code map[string]bool
	// programs are program/script names the documentation mentions (by name or path).
	programs map[string]bool
}

func mentionSets(ms []model.Mention) docMentions {
	d := docMentions{any: map[string]bool{}, code: map[string]bool{}, programs: map[string]bool{}}
	for _, m := range ms {
		b := path.Base(m.Token)
		d.programs[strings.TrimSuffix(b, path.Ext(b))] = true
		for _, t := range variants(m.Token) {
			d.any[t] = true
			if m.InCode {
				d.code[t] = true
			}
		}
	}
	return d
}

func variants(t string) []string {
	t = strings.TrimPrefix(t, "$")
	out := []string{t}
	if s, _, ok := strings.Cut(t, "="); ok {
		out = append(out, s)
	}
	if b := metricBase(t); b != t {
		out = append(out, b)
	}
	for i := strings.IndexByte(t, '.'); i > 0; i = nextDot(t, i) {
		out = append(out, t[:i]) // image.tag documents image
	}
	return out
}

func nextDot(t string, i int) int {
	j := strings.IndexByte(t[i+1:], '.')
	if j < 0 {
		return -1
	}
	return i + 1 + j
}

func documented(s model.Symbol, d docMentions) bool {
	switch s.Kind {
	case model.KindCLIFlag:
		key := strings.TrimLeft(s.Name, "-")
		return d.any["--"+key] || d.any["-"+key]
	case model.KindCLICommand:
		return d.code[s.Name]
	case model.KindMetric:
		return d.any[s.Name] || d.any[metricBase(s.Name)]
	case model.KindConfigKey:
		leaf := s.Name[strings.LastIndexByte(s.Name, '.')+1:]
		return d.any[s.Name] || d.code[leaf] || (d.any[leaf] && strings.Contains(leaf, "_"))
	default:
		return d.any[s.Name]
	}
}

func reverseKinds(o Options) map[model.Kind]bool {
	kinds := map[model.Kind]bool{}
	for _, k := range defaultReverseKinds {
		kinds[k] = len(o.Reverse.Kinds) == 0
	}
	for _, k := range o.Reverse.Kinds {
		kinds[model.Kind(k)] = true
	}
	return kinds
}

// reverser accumulates undocumented-symbol issues and documentation coverage counts.
type reverser struct {
	o       Options
	d       docMentions
	agg     map[string]*Issue
	order   []string
	counted map[string]bool
	stats   map[[2]string]int
}

func reverse(inv *scan.Inventory, o Options) ([]Issue, []SymStat) {
	kinds := reverseKinds(o)
	rv := &reverser{o: o, d: mentionSets(inv.Mentions), agg: map[string]*Issue{}, counted: map[string]bool{}, stats: map[[2]string]int{}}
	for _, s := range inv.Symbols {
		if s.Public && kinds[s.Kind] && (s.Kind != model.KindEnvVar || !ambientEnv[s.Name]) {
			rv.symbol(s)
		}
	}
	out := make([]Issue, 0, len(rv.order))
	for _, k := range rv.order {
		out = append(out, *rv.agg[k])
	}
	return out, symStats(rv.stats)
}

func (rv *reverser) symbol(s model.Symbol) {
	key := string(s.Kind) + "\x00" + s.Program + "\x00" + norm(s.Kind, s.Name)
	doc := documented(s, rv.d)
	if !rv.counted[key] {
		rv.counted[key] = true
		rv.stats[[2]string{string(s.Kind), boolStr(doc)}]++
	}
	conf := s.Confidence
	if s.Program != "" && !rv.d.programs[s.Program] {
		conf = model.ConfLow // the program itself is undocumented (internal tool): its options need not be
	}
	if doc || conf < rv.o.MinConfidence || ignored(rv.o.Ignore, s.Kind, s.Name, s.Path, "docs.symbol.undocumented."+string(s.Kind)) {
		return
	}
	if rv.agg[key] == nil {
		rv.agg[key] = &Issue{Type: TypeUndocumented, Kind: s.Kind, Subject: s.Name, Program: s.Program, Confidence: conf, Anchor: s.Path}
		rv.order = append(rv.order, key)
	}
	rv.agg[key].Locations = append(rv.agg[key].Locations, Location{Path: s.Path, Line: s.Line, Role: "code"})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func symStats(m map[[2]string]int) []SymStat {
	out := make([]SymStat, 0, len(m))
	for k, n := range m {
		out = append(out, SymStat{Kind: model.Kind(k[0]), Documented: k[1] == "true", Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return !out[i].Documented && out[j].Documented
	})
	return out
}
