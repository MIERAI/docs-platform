package semantic

import (
	"regexp"
	"sort"
	"strings"
)

var reToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_.-]{2,}|\d+(?:\.\d+)?`)

// anchors are the tokens of a claim that retrieval looks for in code.
func anchors(text string, ids []string) map[string]int {
	out := map[string]int{}
	for _, id := range ids {
		if n := symbolName(id); len(n) >= 3 {
			out[strings.ToLower(n)] = 3
		}
	}
	for _, t := range reToken.FindAllString(stripCode(text), -1) {
		t = strings.ToLower(strings.Trim(t, ".-"))
		if len(t) >= 3 && !stopKeywords[t] && out[t] == 0 {
			out[t] = 1
		}
	}
	return out
}

func stripCode(s string) string { return strings.ReplaceAll(s, "`", " ") }

// snippets retrieves ≤ maxSnippets small windows of code around the best-matching lines of
// the files a claim resolves to.
func (c *checker) snippets(q question) []snippet {
	anc := anchors(q.cl.Text, q.cl.Idents)
	var cands []snippet
	var scores []int
	for _, p := range c.expand(q.paths) {
		s, score := c.window(p, anc)
		if score > 0 {
			cands = append(cands, s)
			scores = append(scores, score)
		}
	}
	idx := make([]int, len(cands))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	var out []snippet
	for _, i := range idx[:min(maxSnippets, len(idx))] {
		out = append(out, cands[i])
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

// expand replaces directories by their code files (bounded).
func (c *checker) expand(paths []string) []string {
	var out []string
	for _, p := range paths {
		if !c.ix.Repo.IsDir(p) {
			out = append(out, p)
			continue
		}
		files, _ := c.under(p)
		out = append(out, files[:min(20, len(files))]...)
	}
	return out
}

// window returns the window around the line with the most anchor hits, and its score.
func (c *checker) window(p string, anc map[string]int) (snippet, int) {
	ls := c.ix.Lines(p)
	best, bestScore := -1, 0
	for i, l := range ls {
		ll := strings.ToLower(l)
		score := 0
		for a, w := range anc {
			if strings.Contains(ll, a) {
				score += w
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return snippet{}, 0
	}
	from, to := max(0, best-snippetRadius), min(len(ls), best+snippetRadius+1)
	text := strings.Join(ls[from:to], "\n")
	if len(text) > maxSnippetBytes {
		text = text[:maxSnippetBytes]
		if i := strings.LastIndexByte(text, '\n'); i > 0 {
			text = text[:i]
		}
	}
	return snippet{Path: p, From: from + 1, To: from + strings.Count(text, "\n") + 1, Text: text}, bestScore
}
