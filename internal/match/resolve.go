package match

import (
	"path"
	"regexp"
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/repo"
)

// Status of a reference after matching.
type Status string

// Statuses.
const (
	Resolved   Status = "resolved"
	Unresolved Status = "unresolved"
	// External: the reference points outside what this repository can answer for (another
	// tool's flags, an absolute host path); it is neither right nor wrong here.
	External Status = "external"
	// Outside: the documentation points at a path outside the repository (~/..., /home/...).
	Outside Status = "outside"
	Ignored Status = "ignored"
)

// ambientEnv are variables provided by every shell; documentation may mention them freely.
var ambientEnv = map[string]bool{
	"HOME": true, "PATH": true, "USER": true, "PWD": true, "SHELL": true, "TMPDIR": true, "EDITOR": true,
	"LANG": true, "TERM": true, "CI": true, "GOPATH": true, "GOBIN": true, "GOFLAGS": true, "KUBECONFIG": true,
}

var codeExt = map[string]bool{".go": true, ".py": true, ".sh": true, ".tf": true, ".js": true, ".ts": true}

var (
	reLineSuffix = regexp.MustCompile(`:\d+(?::\d+)?$`)
	rePlacehold  = regexp.MustCompile(`[<>{}$\[\]]|\.\.\.`)
	reHomePath   = regexp.MustCompile(`^(~|/home/|/Users/|[A-Z]:\\)`)
)

// resolve decides the status of one reference and the confidence that an unresolved one is
// really wrong (which can be lower than the extractor's classification confidence).
func (ix *index) resolve(r model.Reference) (Status, model.Confidence) {
	switch r.Kind {
	case model.KindLink:
		return ix.link(r)
	case model.KindFilePath:
		return ix.filePath(r)
	case model.KindEnvVar:
		return ix.envVar(r)
	case model.KindCLIFlag:
		return ix.flag(r)
	case model.KindCLICommand:
		return ix.command(r)
	case model.KindMakeTarget:
		return ix.makeTarget(r)
	case model.KindMetric:
		return ix.metric(r)
	case model.KindConfigKey:
		return ix.configKey(r)
	default:
		return External, r.Confidence
	}
}

func (ix *index) link(r model.Reference) (Status, model.Confidence) {
	file, anchor, _ := strings.Cut(r.Text, "#")
	if file == ".." || strings.HasPrefix(file, "../") {
		return Outside, model.ConfMedium
	}
	if !ix.repo.Exists(file) && !ix.generated(file) {
		return Unresolved, model.ConfHigh
	}
	if anchor == "" || !isMarkdown(file) || ix.has(model.KindAnchor, file+"#"+anchor) {
		return Resolved, r.Confidence
	}
	return Unresolved, model.ConfMedium
}

func isMarkdown(p string) bool {
	e := strings.ToLower(path.Ext(p))
	return e == ".md" || e == ".markdown"
}

var reOwnerRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// qualifiedElsewhere: the reference is written right after another repository's name.
func (ix *index) qualifiedElsewhere(q string) bool {
	q = strings.Trim(q, "`'\"*_[]()")
	if q == "" {
		return false
	}
	if reOwnerRepo.MatchString(q) && !strings.Contains(q, ".") && !ix.repo.Exists(q) {
		return true
	}
	for _, name := range ix.related {
		if strings.EqualFold(q, name) || strings.HasSuffix(strings.ToLower(q), "/"+strings.ToLower(name)) {
			return true
		}
	}
	return false
}

func (ix *index) filePath(r model.Reference) (Status, model.Confidence) {
	p := reLineSuffix.ReplaceAllString(strings.TrimSuffix(r.Text, "/"), "")
	if st, ok := ix.notInRepo(p, r); ok {
		if st == Outside {
			return st, model.ConfLow // ~/.kube/config is normal in instructions; only a denylist can say "private"
		}
		return st, r.Confidence
	}
	if strings.Contains(p, "*") {
		return ix.glob(p, r.Confidence)
	}
	clean := path.Clean(p)
	if strings.HasPrefix(clean, "../") {
		if ix.repo.Exists(path.Clean(path.Join(path.Dir(r.Path), p))) {
			return Resolved, r.Confidence
		}
		return Outside, model.ConfLow
	}
	dir := strings.HasSuffix(r.Text, "/")
	if ix.pathExists(clean, r.Path, dir) {
		return Resolved, r.Confidence
	}
	if dir && !strings.Contains(clean, "/") {
		return Unresolved, model.ConfMedium // a top-level directory the docs name does not exist
	}
	return Unresolved, ix.pathConfidence(clean, r)
}

// notInRepo recognises paths this repository cannot answer for: another repository's path,
// a home or absolute path, a placeholder, or a suffix pattern such as .pb.go.
func (ix *index) notInRepo(p string, r model.Reference) (Status, bool) {
	switch {
	case ix.qualifiedElsewhere(r.Qualifier):
		return External, true
	case reHomePath.MatchString(p):
		return Outside, true
	case p == "" || p == "." || p == ".." || strings.HasPrefix(p, "/"):
		return External, true
	case rePlacehold.MatchString(p) || isPlaceholderPath(p):
		return External, true
	case strings.HasPrefix(p, ".") && !strings.HasPrefix(p, "./") && !strings.HasPrefix(p, "../") && !strings.Contains(p, "/"):
		return External, true
	}
	return "", false
}

var rePlaceholderSeg = regexp.MustCompile(`^(?:[A-Za-z]|[A-Za-z]*[a-z][A-Z]|v?X(?:\.Y)*(?:\.Z)?|N+|[0-9]*NN[0-9A-Z]*_?.*|x+|foo|bar|baz|example|my-?[a-z]*|your-?[a-z]*)$`)

// isPlaceholderPath recognises example paths such as design/x/Y.yaml or runtime/vX.Y.Z.
func isPlaceholderPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		stem := seg
		if i := strings.IndexByte(seg, '.'); i > 0 && !strings.HasPrefix(seg, "v") {
			stem = seg[:i]
		}
		if rePlaceholderSeg.MatchString(stem) {
			return true
		}
	}
	return false
}

func (ix *index) pathExists(p, from string, dir bool) bool {
	if ix.repo.Exists(p) || ix.repo.Exists(path.Join(path.Dir(from), p)) || ix.generated(p) {
		return true
	}
	if !strings.Contains(p, "/") && (len(ix.repo.ByBase(p)) > 0 || ix.repo.HasDirNamed(p)) {
		return true
	}
	if ix.suffixExists(p) {
		return true
	}
	// a path spelled out in code is probably an output the code creates
	return !dir && (strings.Contains(p, "/") || path.Ext(p) != "") && ix.inCode(p, "./"+p)
}

// suffixExists accepts a path written relative to some sub-directory ("modules/x" for
// "infra/modules/x").
func (ix *index) suffixExists(p string) bool {
	if !strings.Contains(p, "/") {
		return false
	}
	for _, f := range ix.repo.AllFiles() {
		if strings.HasSuffix(f, "/"+p) || strings.Contains(f, "/"+p+"/") {
			return true
		}
	}
	return false
}

// pathConfidence: a path whose first segment exists in the repository but whose rest does not
// is almost certainly stale; a bare file name or an unknown top-level directory may well refer
// to another repository.
func (ix *index) pathConfidence(p string, r model.Reference) model.Confidence {
	first, _, nested := strings.Cut(p, "/")
	switch {
	case strings.HasPrefix(r.Text, "./"):
		return model.ConfHigh
	case nested && ix.repo.TopLevel(first):
		return model.ConfHigh
	case nested && ix.repo.Exists(path.Join(path.Dir(r.Path), first)) && path.Dir(r.Path) != ".":
		return model.ConfHigh
	case !nested && r.Context == "comment" && codeExt[path.Ext(p)]:
		return model.ConfMedium // a source comment naming a sibling source file that is gone
	}
	return model.ConfLow
}

func (ix *index) glob(p string, c model.Confidence) (Status, model.Confidence) {
	p = strings.TrimPrefix(p, "./")
	for _, f := range ix.repo.AllFiles() {
		if repo.Match(p, f) {
			return Resolved, c
		}
	}
	return Unresolved, model.ConfLow
}

// generated reports whether p is matched by a root .gitignore pattern (an output path that
// legitimately does not exist in a checkout).
func (ix *index) generated(p string) bool {
	return repo.MatchAny(ix.gitignore, p) || repo.MatchAny(ix.gitignore, p+"/")
}
