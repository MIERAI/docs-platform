package contract

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MIERAI/docs-platform/internal/repo"
)

// Direction classes.
const (
	DesignBehindCode = "design_behind_code" // the code changed later
	CodeBehindDesign = "code_behind_design" // the design changed later, or code never implemented it
	Diverged         = "diverged"           // both changed since they last agreed
	Undocumented     = "undocumented"       // only in code, never in the design
	RemovedInCode    = "removed_in_code"    // the design still lists what the code deleted
	DirUnknown       = "unknown"            // no history to tell
)

// worktreeCommit marks an uncommitted change of a working-tree source.
const worktreeCommit = "WORKTREE"

// Direction is the history-based classification of one item, with its evidence.
type Direction struct {
	Class string `json:"class"`
	// Reason says which rule decided, in words.
	Reason string `json:"reason"`
	// DesignLast / CodeLast are the last commits whose diff touched a line naming the item
	// on each side (git log -G); RemovedBy is the commit that removed it from that side
	// (git log -S: the last change of its occurrence count, which left none).
	DesignLast *Evidence `json:"design_last,omitempty"`
	CodeLast   *Evidence `json:"code_last,omitempty"`
	RemovedBy  *Evidence `json:"removed_by,omitempty"`
	AddedBy    *Evidence `json:"added_by,omitempty"`
	// DesignEver: the design ever named the item (only computed when the design lacks it).
	DesignEver *bool `json:"design_ever,omitempty"`
}

// Evidence is one commit.
type Evidence struct {
	Commit string   `json:"commit"`
	Date   string   `json:"date"`
	Paths  []string `json:"paths,omitempty"`
	Side   string   `json:"side"`
	Term   string   `json:"term"`
}

func (e *Evidence) time() time.Time {
	if e == nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, e.Date)
	return t
}

// History answers pickaxe questions on both sides (cached; safe for concurrent use).
type History struct {
	Design, Code *Source
	now          time.Time
	mu           sync.Mutex
	cache        map[string][]*Evidence
}

// NewHistory prepares history lookups; now dates uncommitted working-tree changes.
func NewHistory(design, code *Source, now time.Time) *History {
	return &History{Design: design, Code: code, now: now, cache: map[string][]*Evidence{}}
}

// Classify sets the direction of every item, with up to workers concurrent git calls.
func (h *History) Classify(ctx context.Context, items []Item, workers int) {
	sem := make(chan struct{}, max(workers, 1))
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(it *Item) {
			defer wg.Done()
			defer func() { <-sem }()
			it.Direction = h.classify(ctx, it)
		}(&items[i])
	}
	wg.Wait()
}

func (h *History) classify(ctx context.Context, it *Item) *Direction {
	dPaths, cPaths := pathsOr(it.designFiles, h.Design.Paths), pathsOr(it.codeFiles, h.Code.Paths)
	d := &Direction{}
	dl := h.touches(ctx, h.Design, SideDesign, it.designTerm, dPaths, 2)
	cl := h.touches(ctx, h.Code, SideCode, it.codeTerm, cPaths, 2)
	d.DesignLast, d.CodeLast = at(dl, 0), at(cl, 0)
	switch it.Kind {
	case KindOpMissingInCode, KindInMissingInCode, KindOutMissingInCode, KindRuleMissingInCode:
		h.designOnly(ctx, it, d, h.Code.Paths)
	case KindOpMissingInDesign, KindInMissingInDesign, KindOutMissingInDesign, KindRuleMissingInDesign:
		h.codeOnly(ctx, it, d, h.Design.Paths)
	default:
		bothPresent(d, dl, cl)
	}
	return d
}

// designOnly: the code lacks the item. Did the code ever have it (in the operation's code
// files, or anywhere on the code side for an operation-level item)?
func (h *History) designOnly(ctx context.Context, it *Item, d *Direction, codeScope []string) {
	scope := pathsOr(it.codeFiles, codeScope)
	if it.Subject == "" {
		scope = codeScope
	}
	ch := h.changes(ctx, h.Code, SideCode, it.codeTerm, scope)
	if len(ch) == 0 {
		d.Class, d.Reason = CodeBehindDesign, "not implemented: the code never named it"
		return
	}
	d.AddedBy = ch[len(ch)-1]
	if it.Subject != "" && h.Code.Names(ctx, it.codeTerm, scope) {
		d.Class, d.Reason = DesignBehindCode, "the code still names it but no longer as the design declares it; the code changed after the design"
		if d.DesignLast != nil && d.CodeLast != nil && d.DesignLast.time().After(d.CodeLast.time()) {
			d.Class, d.Reason = CodeBehindDesign, "the design changed it after the code last did"
		}
		return
	}
	d.RemovedBy = ch[0]
	d.Class, d.Reason = RemovedInCode, "the code had it and removed it; the design still lists it"
	if d.DesignLast != nil && d.DesignLast.time().After(d.RemovedBy.time()) {
		d.Reason += " (the design was edited after the removal)"
	}
}

// codeOnly: the design lacks the item. Did the design ever name it?
func (h *History) codeOnly(ctx context.Context, it *Item, d *Direction, designScope []string) {
	scope := pathsOr(it.designFiles, designScope)
	if it.Subject == "" {
		scope = designScope
	}
	ch := h.changes(ctx, h.Design, SideDesign, it.designTerm, scope)
	ever := len(ch) > 0
	d.DesignEver = &ever
	if !ever {
		d.Class, d.Reason = Undocumented, "only in the code; the design never named it"
		return
	}
	if it.Subject != "" && h.Design.Names(ctx, it.designTerm, scope) {
		d.Class, d.Reason = DesignBehindCode, "the design names it elsewhere but does not declare it here; the code changed after the design"
		if d.DesignLast != nil && d.CodeLast != nil && d.DesignLast.time().After(d.CodeLast.time()) {
			d.Class, d.Reason = CodeBehindDesign, "the design changed after the code last did"
		}
		return
	}
	d.RemovedBy = ch[0]
	if d.CodeLast != nil && d.CodeLast.time().After(d.RemovedBy.time()) {
		d.Class, d.Reason = DesignBehindCode, "the design dropped it, then the code changed it again"
		return
	}
	d.Class, d.Reason = CodeBehindDesign, "the design removed it; the code still has it"
}

// bothPresent: the item is on both sides but differs. The side that touched it last moved;
// when the other side also moved after the mover's previous touch, both moved: diverged.
func bothPresent(d *Direction, dl, cl []*Evidence) {
	d1, c1, d2, c2 := at(dl, 0), at(cl, 0), at(dl, 1), at(cl, 1)
	switch {
	case d1 == nil && c1 == nil:
		d.Class, d.Reason = DirUnknown, "no history names it on either side"
	case c1 != nil && (d1 == nil || c1.time().After(d1.time())):
		if d1 != nil && c2 != nil && d1.time().After(c2.time()) {
			d.Class, d.Reason = Diverged, "the design changed after the code's previous change, then the code changed again"
			return
		}
		d.Class, d.Reason = DesignBehindCode, "the code changed it after the design last did"
	case d1 != nil && (c1 == nil || d1.time().After(c1.time())):
		if c1 != nil && d2 != nil && c1.time().After(d2.time()) {
			d.Class, d.Reason = Diverged, "the code changed after the design's previous change, then the design changed again"
			return
		}
		d.Class, d.Reason = CodeBehindDesign, "the design changed it after the code last did"
	default:
		d.Class, d.Reason = Diverged, "both sides changed it in the same instant"
	}
}

func at(es []*Evidence, i int) *Evidence {
	if i < len(es) {
		return es[i]
	}
	return nil
}

func pathsOr(files, globs []string) []string {
	if len(files) > 0 {
		return files
	}
	return globs
}

// termRegex is the POSIX ERE git uses: the term as a whole identifier, in its snake_case,
// lowerCamel and UpperCamel spellings (a design's total_amount is a Go TotalAmount).
func termRegex(term string) string {
	var alts []string
	seen := map[string]bool{}
	for _, v := range []string{term, camel(term, false), camel(term, true)} {
		if v != "" && !seen[v] {
			seen[v] = true
			alts = append(alts, regexp.QuoteMeta(v))
		}
	}
	return `(^|[^A-Za-z0-9_])(` + strings.Join(alts, "|") + `)([^A-Za-z0-9_]|$)`
}

// camel converts snake_case to lowerCamel / UpperCamel ("" when term has no underscore and
// upper is false).
func camel(term string, upper bool) string {
	parts := strings.Split(term, "_")
	if len(parts) == 1 && !upper {
		return ""
	}
	var b strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 || upper {
			p = strings.ToUpper(p[:1]) + p[1:]
		}
		b.WriteString(p)
	}
	return b.String()
}

// touches returns the newest n commits (newest first) whose diff adds or removes a line
// naming term within paths (git log -G), preceded by an uncommitted change when the source
// is a working tree.
func (h *History) touches(ctx context.Context, s *Source, side, term string, paths []string, n int) []*Evidence {
	if term == "" || s.HistoryRef() == "" {
		return nil
	}
	var out []*Evidence
	if s.Ref == "" && h.uncommitted(ctx, s, term, paths) {
		out = append(out, &Evidence{Commit: worktreeCommit, Date: h.now.UTC().Format(time.RFC3339), Side: side, Term: term})
	}
	out = append(out, h.log(ctx, s, side, term, paths, "-G", n)...)
	return out
}

// changes returns every commit (newest first) that changed the number of occurrences of
// term within paths (git log -S --pickaxe-regex): additions and removals.
func (h *History) changes(ctx context.Context, s *Source, side, term string, paths []string) []*Evidence {
	if term == "" || s.HistoryRef() == "" {
		return nil
	}
	var out []*Evidence
	if s.Ref == "" && h.uncommitted(ctx, s, term, paths) {
		out = append(out, &Evidence{Commit: worktreeCommit, Date: h.now.UTC().Format(time.RFC3339), Side: side, Term: term})
	}
	return append(out, h.log(ctx, s, side, term, paths, "-S", 0)...)
}

func (h *History) log(ctx context.Context, s *Source, side, term string, paths []string, mode string, n int) []*Evidence {
	key := side + "\x00" + mode + "\x00" + term + "\x00" + strings.Join(paths, "\x00")
	h.mu.Lock()
	if v, ok := h.cache[key]; ok {
		h.mu.Unlock()
		return limit(v, n)
	}
	h.mu.Unlock()
	args := []string{"log", "--format=%x1e%H%x1f%cI", "--name-only", mode + termRegex(term)}
	if mode == "-S" {
		args = append(args, "--pickaxe-regex")
	}
	args = append(args, s.HistoryRef(), "--")
	args = append(args, pathspecs(paths)...)
	out, err := repo.Git(ctx, s.Dir, args...)
	var ev []*Evidence
	if err == nil {
		ev = parseLog(out, side, term)
	}
	h.mu.Lock()
	h.cache[key] = ev
	h.mu.Unlock()
	return limit(ev, n)
}

func limit(ev []*Evidence, n int) []*Evidence {
	if n > 0 && len(ev) > n {
		return ev[:n]
	}
	return ev
}

// pathspecs turns globs into git pathspecs (":(glob)" magic); plain files stay literal.
func pathspecs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.ContainsAny(p, "*?[") {
			out = append(out, ":(glob)"+p)
		} else {
			out = append(out, ":(literal)"+p)
		}
	}
	return out
}

func parseLog(out, side, term string) []*Evidence {
	var ev []*Evidence
	for _, rec := range strings.Split(out, "\x1e") {
		lines := strings.Split(strings.TrimSpace(rec), "\n")
		head := strings.Split(lines[0], "\x1f")
		if len(head) != 2 {
			continue
		}
		e := &Evidence{Commit: head[0], Date: head[1], Side: side, Term: term}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				e.Paths = append(e.Paths, l)
			}
		}
		ev = append(ev, e)
	}
	return ev
}

// uncommitted reports whether the working tree's uncommitted diff adds or removes a line
// naming term within paths.
func (h *History) uncommitted(ctx context.Context, s *Source, term string, paths []string) bool {
	args := append([]string{"diff", "--no-color", "--no-ext-diff", "-U0", "HEAD", "--"}, pathspecs(paths)...)
	out, err := repo.Git(ctx, s.Dir, args...)
	if err != nil || out == "" {
		return false
	}
	re := regexp.MustCompile(termRegex(term))
	for _, l := range strings.Split(out, "\n") {
		if (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")) && !strings.HasPrefix(l, "+++") && !strings.HasPrefix(l, "---") && re.MatchString(l[1:]) {
			return true
		}
	}
	return false
}
