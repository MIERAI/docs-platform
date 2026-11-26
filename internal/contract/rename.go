package contract

import (
	"strings"
	"unicode"
)

// renameThreshold is the lowest similarity reported as a rename candidate.
const renameThreshold = 0.75

// addRenameCandidates pairs every "missing in code" item with the most similar "missing in
// design" item of the same category (operations; or inputs / outputs / rules of the same
// operation), and back. Similarity is the best of a normalized edit ratio and the overlap of
// the words of the two names. A candidate is only a hint for a human.
func addRenameCandidates(items []Item) {
	pairs := map[string][2]string{
		KindOpMissingInCode:    {KindOpMissingInDesign, ""},
		KindInMissingInCode:    {KindInMissingInDesign, "inputs"},
		KindOutMissingInCode:   {KindOutMissingInDesign, "outputs"},
		KindRuleMissingInCode:  {KindRuleMissingInDesign, "rules"},
		KindOpMissingInDesign:  {KindOpMissingInCode, ""},
		KindInMissingInDesign:  {KindInMissingInCode, "inputs"},
		KindOutMissingInDesign: {KindOutMissingInCode, "outputs"},
	}
	for i := range items {
		other, ok := pairs[items[i].Kind]
		if !ok || items[i].Severity == SevInfo && items[i].Kind == KindOutMissingInDesign {
			continue
		}
		best, score := "", 0.0
		for j := range items {
			if items[j].Kind != other[0] || !sameScope(items[i], items[j]) {
				continue
			}
			if s := similarity(itemName(items[i]), itemName(items[j])); s > score {
				best, score = itemName(items[j]), s
			}
		}
		if score >= renameThreshold {
			items[i].Rename = &Rename{Name: best, Score: float64(int(score*100+0.5)) / 100}
		}
	}
}

func sameScope(a, b Item) bool {
	if strings.HasPrefix(a.Kind, "operation.") {
		return true
	}
	return a.Operation == b.Operation && a.Group == b.Group && parentOf(a.Subject) == parentOf(b.Subject)
}

func parentOf(subject string) string {
	if i := strings.LastIndexByte(subject, '.'); i >= 0 {
		return subject[:i]
	}
	return ""
}

func itemName(it Item) string {
	if it.Subject == "" {
		return it.Operation
	}
	return leaf(it.Subject)
}

// similarity is in [0, 1].
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	na, nb := normName(a), normName(b)
	if na == nb {
		return 0.95
	}
	return max(editRatio(na, nb), wordOverlap(words(a), words(b)))
}

func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// words splits camelCase, snake_case and kebab-case into lower-case words.
func words(s string) []string {
	return strings.FieldsFunc(snake(s), func(r rune) bool { return r == '_' || r == '-' || r == ' ' || r == '.' })
}

func wordOverlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range a {
		set[w] = true
	}
	common := 0
	for _, w := range b {
		if set[w] {
			common++
			delete(set, w)
		}
	}
	return 2 * float64(common) / float64(len(a)+len(b))
}

// editRatio is 1 - levenshtein / max length.
func editRatio(a, b string) float64 {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return 1 - float64(prev[len(rb)])/float64(max(len(ra), len(rb)))
}
