package evalmodel

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MIERAI/docs-platform/internal/agentic"
	"github.com/MIERAI/docs-platform/internal/instance"
	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/judgev1"
)

// Scores of one run. Judge fields are set for the judge task, draft fields for the draft
// task; cost and latency for both. Rates are 0..1; nil means "no denominator".
type Scores struct {
	Items     int `json:"items"`
	Answered  int `json:"answered"`
	Errors    int `json:"errors"`
	Refused   int `json:"classification_refused"`
	BudgetCut int `json:"not_run_budget"`

	TruePositives      int      `json:"true_positives,omitempty"`
	FalsePositives     int      `json:"false_positives,omitempty"`
	FalseNegatives     int      `json:"false_negatives,omitempty"`
	TrueNegatives      int      `json:"true_negatives,omitempty"`
	Precision          *float64 `json:"precision,omitempty"`
	Recall             *float64 `json:"recall,omitempty"`
	UnknownRate        *float64 `json:"unknown_rate,omitempty"`
	QuoteRejectionRate *float64 `json:"quote_rejection_rate,omitempty"`
	BySplit            []Slice  `json:"by_split,omitempty"`

	// Agentic (1.1): items per derived category; unverifiable_in_repo is reported here and
	// in unverifiable_rate, not as unknown and not as an error; evidence_rejection_rate is
	// the share of the model's evidence the harness could not validate; parse failures (no
	// answer object at all) and fallbacks (tolerant parser) are counted apart from errors.
	Categories            map[string]int `json:"categories,omitempty"`
	UnverifiableRate      *float64       `json:"unverifiable_rate,omitempty"`
	EvidenceRejectionRate *float64       `json:"evidence_rejection_rate,omitempty"`
	ParseFailures         int            `json:"parse_failures,omitempty"`
	ParseFallbacks        int            `json:"parse_fallbacks,omitempty"`

	Drafted        int      `json:"drafted,omitempty"`
	SupportedRate  *float64 `json:"supported_rate,omitempty"`
	MeanSimilarity *float64 `json:"mean_similarity,omitempty"`

	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	USDPerItem   float64 `json:"usd_per_item"`
	P50Latency   int64   `json:"p50_latency_ms"`
	Estimated    bool    `json:"estimated"`
	Note         string  `json:"note"`
}

// Slice is recall on positives and false-positive rate on negatives of one split.
type Slice struct {
	Split     string   `json:"split"`
	Positives int      `json:"positives"`
	Negatives int      `json:"negatives"`
	Recall    *float64 `json:"recall,omitempty"`
	FPRate    *float64 `json:"false_positive_rate,omitempty"`
}

const scoreNote = "positives are labelled by a strong-AI audit: recall/precision measure AGREEMENT with that audit, not accuracy (strong models score inflated); negatives assume the doc was consistent when its line was written"

func ratio(a, b int) *float64 {
	if b == 0 {
		return nil
	}
	v := float64(a) / float64(b)
	return &v
}

func score(task string, rs []ItemResult, st judge.Stats, dry bool) Scores {
	s := Scores{
		Items: len(rs), InputTokens: st.InputTokens + st.CacheRead, OutputTokens: st.OutputTokens,
		CostUSD: st.CostUSD, P50Latency: st.P50LatencyMS(), Estimated: dry || st.Estimated, Note: scoreNote,
	}
	if len(rs) > 0 {
		s.USDPerItem = st.CostUSD / float64(len(rs))
	}
	for _, r := range rs {
		switch {
		case r.Error == judge.ErrBudget.Error():
			s.BudgetCut++
		case strings.HasPrefix(r.Error, judge.ErrClassified.Error()):
			s.Refused++
		case r.Error != "":
			s.Errors++
		default:
			s.Answered++
		}
	}
	if dry {
		return s
	}
	if task == TaskDraft {
		scoreDraft(&s, rs)
		return s
	}
	scoreJudge(&s, rs)
	s.ParseFailures, s.ParseFallbacks = st.ParseFailures, st.ParseFallbacks
	scoreCategories(&s, rs)
	return s
}

func scoreCategories(s *Scores, rs []ItemResult) {
	cats := map[string]int{}
	unver, ev, evOK := 0, 0, 0
	for _, r := range rs {
		if r.Category == "" {
			continue
		}
		cats[r.Category]++
		if r.Category == agentic.UnverifiableInRepo {
			unver++
		}
		ev += r.Evidence
		evOK += r.EvidenceOK
	}
	if len(cats) == 0 {
		return
	}
	s.Categories = cats
	s.UnverifiableRate = ratio(unver, len(rs))
	s.EvidenceRejectionRate = ratio(ev-evOK, ev)
}

func scoreJudge(s *Scores, rs []ItemResult) {
	unknown, yesAnswers, rejected := 0, 0, 0
	for _, r := range rs {
		if r.Verdict == judgev1.Yes || r.QuoteRejected {
			yesAnswers++
		}
		if r.QuoteRejected {
			rejected++
		}
		pos := r.Expected == judgev1.Yes
		switch {
		case r.Verdict == judgev1.Yes && pos:
			s.TruePositives++
		case r.Verdict == judgev1.Yes:
			s.FalsePositives++
		case pos:
			s.FalseNegatives++
		default:
			s.TrueNegatives++
		}
		if r.Verdict == judgev1.Unknown && r.Category != agentic.UnverifiableInRepo {
			unknown++
		}
	}
	s.Precision = ratio(s.TruePositives, s.TruePositives+s.FalsePositives)
	s.Recall = ratio(s.TruePositives, s.TruePositives+s.FalseNegatives)
	s.UnknownRate = ratio(unknown, len(rs))
	s.QuoteRejectionRate = ratio(rejected, yesAnswers)
	s.BySplit = slices(rs)
}

func slices(rs []ItemResult) []Slice {
	by := map[string]*[4]int{} // positives, tp, negatives, fp
	for _, r := range rs {
		c := by[r.Split]
		if c == nil {
			c = &[4]int{}
			by[r.Split] = c
		}
		yes := r.Verdict == judgev1.Yes
		if r.Expected == judgev1.Yes {
			c[0]++
			if yes {
				c[1]++
			}
		} else {
			c[2]++
			if yes {
				c[3]++
			}
		}
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Slice, 0, len(keys))
	for _, k := range keys {
		c := by[k]
		out = append(out, Slice{Split: k, Positives: c[0], Negatives: c[2], Recall: ratio(c[1], c[0]), FPRate: ratio(c[3], c[2])})
	}
	return out
}

func scoreDraft(s *Scores, rs []ItemResult) {
	supported := 0
	sum := 0.0
	for _, r := range rs {
		if r.Draft == "" {
			continue
		}
		s.Drafted++
		if len(r.Unsupported) == 0 {
			supported++
		}
		if r.Similarity != nil {
			sum += *r.Similarity
		}
	}
	s.SupportedRate = ratio(supported, s.Drafted)
	if s.Drafted > 0 {
		m := sum / float64(s.Drafted)
		s.MeanSimilarity = &m
	}
}

// decide applies the switching rule of the instance configuration.
func decide(task string, s Scores, t instance.Eval) *Switch {
	sw := &Switch{Failed: []string{}}
	minRate := func(name string, v *float64, limit float64) {
		if limit > 0 && (v == nil || *v < limit) {
			sw.Failed = append(sw.Failed, fmt.Sprintf("%s %s < %.2f", name, fmtRate(v), limit))
		}
	}
	maxRate := func(name string, v *float64, limit float64) {
		if limit > 0 && v != nil && *v > limit {
			sw.Failed = append(sw.Failed, fmt.Sprintf("%s %s > %.2f", name, fmtRate(v), limit))
		}
	}
	maxCost := func(usd float64, p50 int64, limUSD float64, limMS int) {
		if limUSD > 0 && usd > limUSD {
			sw.Failed = append(sw.Failed, fmt.Sprintf("usd_per_item %.4f > %.4f", usd, limUSD))
		}
		if limMS > 0 && p50 > int64(limMS) {
			sw.Failed = append(sw.Failed, fmt.Sprintf("p50_latency_ms %d > %d", p50, limMS))
		}
	}
	if task == TaskDraft {
		minRate("supported_rate", s.SupportedRate, t.Draft.MinSupportedRate)
		minRate("mean_similarity", s.MeanSimilarity, t.Draft.MinSimilarity)
		maxCost(s.USDPerItem, s.P50Latency, t.Draft.MaxUSDPerItem, t.Draft.MaxP50LatencyMS)
	} else {
		minRate("precision", s.Precision, t.Judge.MinPrecision)
		minRate("recall", s.Recall, t.Judge.MinRecall)
		maxRate("unknown_rate", s.UnknownRate, t.Judge.MaxUnknownRate)
		maxRate("quote_rejection_rate", s.QuoteRejectionRate, t.Judge.MaxQuoteRejectionRate)
		maxCost(s.USDPerItem, s.P50Latency, t.Judge.MaxUSDPerItem, t.Judge.MaxP50LatencyMS)
	}
	if s.Errors+s.BudgetCut+s.Refused > 0 {
		sw.Failed = append(sw.Failed, fmt.Sprintf("%d items without an answer (errors %d, budget %d, refused %d)", s.Errors+s.BudgetCut+s.Refused, s.Errors, s.BudgetCut, s.Refused))
	}
	sw.Qualifies = len(sw.Failed) == 0
	return sw
}

func fmtRate(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f", *v)
}

var (
	reFactual = regexp.MustCompile("`[^`]{2,80}`|[A-Za-z_][A-Za-z0-9_]*[._/-][A-Za-z0-9_./-]*[A-Za-z0-9_]|[A-Za-z_]*[0-9][A-Za-z0-9_.]*|[a-z]+[A-Z][A-Za-z0-9]*")
	reWordSim = regexp.MustCompile(`[A-Za-z0-9_]{3,}|\p{Han}|\p{Hiragana}+|\p{Katakana}+`)
)

// Unsupported returns the identifier-, path- and number-like tokens of a draft that appear
// in neither the claim nor the evidence: facts the draft invented.
func Unsupported(draft, claimText string, ev []judgev1.Evidence) []string {
	var hay strings.Builder
	hay.WriteString(claimText)
	for _, e := range ev {
		hay.WriteString("\n" + e.Text + "\n" + e.Path)
	}
	h := hay.String()
	seen := map[string]bool{}
	var out []string
	for _, t := range reFactual.FindAllString(draft, -1) {
		t = strings.Trim(t, "`.")
		if len(t) < 2 || seen[t] || strings.Contains(h, t) {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// Similarity is the Jaccard overlap of word and Han-character sets (a cheap acceptance
// proxy: how close a draft is to the ground truth's fix and description).
func Similarity(a, b string) float64 {
	sa, sb := wordSet(a), wordSet(b)
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range reWordSim.FindAllString(strings.ToLower(s), -1) {
		m[w] = true
	}
	return m
}
