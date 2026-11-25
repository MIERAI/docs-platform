// Package evidence builds a deterministic index of what the code says: configuration values
// (YAML, HCL, Go constants and flag defaults, shell and Make defaults, Dockerfile images),
// schedules, named sets (directory children, YAML sibling keys, distinct values of a key,
// commands of a program, make targets), generated-file markers and the layer-③ symbol table.
// The semantic checks compare claims against it; the judge model only sees small snippets
// retrieved from it.
package evidence

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/internal/yamlkeys"
)

// Fact is one key = value the code defines.
type Fact struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Key   string `json:"key"`  // full key path ("spec.retention", "RETENTION_DAYS")
	Leaf  string `json:"leaf"` // last key segment
	Value string `json:"value"`
	// Vals are the parsed values (durations, sizes, percents, versions, cron, numbers).
	Vals []claim.Value `json:"vals,omitempty"`
	// Num is set when the raw value is a plain number.
	Num   float64 `json:"num,omitempty"`
	IsNum bool    `json:"is_num,omitempty"`
	// Commented is true when the fact was read from a commented-out line.
	Commented bool `json:"commented,omitempty"`
}

var (
	reGoAssign  = regexp.MustCompile(`^\s*(?:const\s+|var\s+)?([A-Za-z_]\w*)\s*(?:[A-Za-z_.]+\s*)?=\s*(.+?)\s*(?://.*)?$`)
	reGoFlag    = regexp.MustCompile(`\.(?:Int|Int64|Uint|Duration|String|Float64|Bool)(?:Var)?\(\s*(?:&[\w.]+,\s*)?"([\w.-]+)",\s*([^,]+),`)
	reGoEnvDef  = regexp.MustCompile(`\(\s*"([A-Z][A-Z0-9_]{2,})"\s*,\s*([^,()]+)\)`)
	reShDefault = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):?[-=]([^}]*)\}`)
	reShAssign  = regexp.MustCompile(`^\s*(?:export\s+|readonly\s+|local\s+)?([A-Z_][A-Z0-9_]*)=("[^"]*"|'[^']*'|\S+)`)
	reMakeVar   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*[:?+]?=\s*(.+)$`)
	reHCL       = regexp.MustCompile(`^\s*([A-Za-z_][\w-]*)\s*=\s*("[^"]*"|[\d.]+[a-zA-Z]*|true|false)\s*(?:#.*|//.*)?$`)
	reFrom      = regexp.MustCompile(`(?i)^\s*FROM\s+(?:--\S+\s+)*(\S+)`)
	reGoDur     = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*\*\s*time\.(Millisecond|Second|Minute|Hour)$|^time\.(Millisecond|Second|Minute|Hour)\s*\*\s*(\d+)$`)
	reImageRef  = regexp.MustCompile(`^([\w.-]+(?::\d+)?/)?([\w./-]*?)([\w.-]+):([\w][\w.-]{0,127})$`)
)

var goDurUnit = map[string]float64{"Millisecond": 0.001, "Second": 1, "Minute": 60, "Hour": 3600}

func newFact(p string, line int, key, value string) Fact {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	f := Fact{Path: p, Line: line, Key: key, Leaf: leafOf(key), Value: value}
	if n, err := strconv.ParseFloat(value, 64); err == nil {
		f.Num, f.IsNum = n, true
	}
	if m := reGoDur.FindStringSubmatch(value); m != nil {
		f.Vals = append(f.Vals, goDuration(m))
	}
	if c, ok := claim.ParseCron(value); ok {
		f.Vals = append(f.Vals, claim.Value{Kind: claim.Cron, Raw: value, Text: c})
	}
	if len(value) <= 200 {
		f.Vals = append(f.Vals, claim.Values(" "+value+" ")...)
	}
	return f
}

func goDuration(m []string) claim.Value {
	n, unit := m[1], m[2]
	if n == "" {
		n, unit = m[4], m[3]
	}
	v, _ := strconv.ParseFloat(n, 64)
	return claim.Value{Kind: claim.Duration, Raw: m[0], Num: v * goDurUnit[unit]}
}

func leafOf(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 && i+1 < len(key) {
		return key[i+1:]
	}
	return key
}

// factsOf extracts facts from one file by its type.
func factsOf(p string, b []byte) ([]Fact, []yamlkeys.Doc) {
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(p))
	switch {
	case ext == ".yaml" || ext == ".yml":
		docs, _ := yamlkeys.Parse(b)
		return yamlFacts(p, docs), docs
	case ext == ".go":
		return goFacts(p, b), nil
	case ext == ".tf" || ext == ".hcl" || ext == ".tfvars":
		return lineFacts(p, b, reHCL), nil
	case ext == ".sh" || ext == ".bash" || ext == ".env":
		return shellFacts(p, b), nil
	case base == "Makefile" || ext == ".mk":
		return lineFacts(p, b, reMakeVar), nil
	case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") || ext == ".dockerfile":
		return dockerFacts(p, b), nil
	}
	return nil, nil
}

func yamlFacts(p string, docs []yamlkeys.Doc) []Fact {
	var out []Fact
	for _, d := range docs {
		for _, k := range d.Keys {
			if k.Scalar == "" {
				continue
			}
			out = append(out, newFact(p, k.Line, k.Path, k.Scalar))
			if img := imageFact(p, k.Line, k.Scalar); k.Leaf == "image" && img != nil {
				out = append(out, *img)
			}
		}
		out = append(out, chartFacts(p, d.Keys)...)
	}
	return out
}

// chartFacts turns sibling keys {chart|name: X, version|targetRevision: V} (Helm releases,
// Argo CD sources, Chart.yaml dependencies) into a version fact keyed by X.
func chartFacts(p string, keys []yamlkeys.Key) []Fact {
	type pair struct {
		name, ver string
		line      int
	}
	groups := map[string]*pair{}
	for _, k := range keys {
		parent := strings.TrimSuffix(strings.TrimSuffix(k.Path, k.Leaf), ".")
		g := groups[parent]
		if g == nil {
			g = &pair{}
			groups[parent] = g
		}
		switch k.Leaf {
		case "chart", "name":
			if g.name == "" || k.Leaf == "chart" {
				g.name = k.Scalar
			}
		case "version", "targetRevision", "appVersion":
			if g.ver == "" {
				g.ver, g.line = k.Scalar, k.Line
			}
		}
	}
	var out []Fact
	for _, g := range groups {
		if g.name == "" || g.ver == "" {
			continue
		}
		if _, ok := claim.ParseVersion(g.ver); ok {
			f := Fact{Path: p, Line: g.line, Key: g.name, Leaf: g.name, Value: g.ver}
			f.Vals = []claim.Value{{Kind: claim.Version, Raw: g.ver, Text: strings.TrimPrefix(g.ver, "v")}}
			out = append(out, f)
		}
	}
	return out
}

// imageFact turns "registry/name:tag" into a version fact keyed by the image name.
func imageFact(p string, line int, ref string) *Fact {
	m := reImageRef.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return nil
	}
	f := Fact{Path: p, Line: line, Key: m[3], Leaf: m[3], Value: m[4]}
	f.Vals = []claim.Value{{Kind: claim.Version, Raw: m[4], Text: strings.TrimPrefix(m[4], "v")}}
	return &f
}

func lines(b []byte) []string { return strings.Split(string(b), "\n") }

func goFacts(p string, b []byte) []Fact {
	var out []Fact
	for i, l := range lines(b) {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "//") {
			continue
		}
		for _, m := range reGoFlag.FindAllStringSubmatch(l, -1) {
			out = append(out, newFact(p, i+1, m[1], m[2]))
		}
		for _, m := range reGoEnvDef.FindAllStringSubmatch(l, -1) {
			out = append(out, newFact(p, i+1, m[1], m[2]))
		}
		if m := reGoAssign.FindStringSubmatch(l); m != nil && isLiteral(m[2]) {
			out = append(out, newFact(p, i+1, m[1], m[2]))
		}
	}
	return out
}

func isLiteral(v string) bool {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		return true
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return true
	}
	return reGoDur.MatchString(v)
}

func lineFacts(p string, b []byte, re *regexp.Regexp) []Fact {
	var out []Fact
	for i, l := range lines(b) {
		commented := false
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			t, commented = strings.TrimLeft(t, "#/ "), true
		}
		if m := re.FindStringSubmatch(t); m != nil {
			f := newFact(p, i+1, m[1], m[2])
			f.Commented = commented
			out = append(out, f)
		}
	}
	return out
}

func shellFacts(p string, b []byte) []Fact {
	var out []Fact
	for i, l := range lines(b) {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		for _, m := range reShDefault.FindAllStringSubmatch(l, -1) {
			out = append(out, newFact(p, i+1, m[1], m[2]))
		}
		if m := reShAssign.FindStringSubmatch(l); m != nil {
			out = append(out, newFact(p, i+1, m[1], m[2]))
		}
	}
	return out
}

func dockerFacts(p string, b []byte) []Fact {
	var out []Fact
	for i, l := range lines(b) {
		if m := reFrom.FindStringSubmatch(l); m != nil {
			if f := imageFact(p, i+1, m[1]); f != nil {
				out = append(out, *f)
			}
		}
	}
	return out
}
