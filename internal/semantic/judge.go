package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MIERAI/docs-platform/internal/judge"
	"github.com/MIERAI/docs-platform/internal/model"
	"github.com/MIERAI/docs-platform/internal/semantic/claim"
	"github.com/MIERAI/docs-platform/judgev1"
)

// Judge protocol limits.
const (
	maxSnippets     = 3
	maxSnippetBytes = 2400 // ≤ 3 snippets ≈ 3k tokens in total
	snippetRadius   = 6
	minQuoteBytes   = 10
	votesNeeded     = 2 // majority of 3 orderings
	maxOrderings    = 3
	promptVersion   = "docsp-judge/1"
)

type question struct {
	cl       claim.Claim
	paths    []string
	priority int
}

type snippet struct {
	Path     string
	From, To int
	Text     string
}

// PromptHash identifies the judge prompt templates.
func PromptHash() string { return judge.TemplateHash() }

// addQuestion queues a claim for the judge. Priority (lower first): stale claims with a state
// word or a value, then state words, then other stale claims, then values and lists.
func (c *checker) addQuestion(cl *claim.Claim, paths []string, stale bool) {
	state := hasState(cl, claim.StatePlanned, claim.StateNotYet, claim.StateInProgress, claim.StateTodo, claim.StateRemoved)
	prio := 3
	switch {
	case stale && (state || len(cl.Values) > 0 || len(cl.Lists) > 0):
		prio = 0
	case state:
		prio = 1
	case stale:
		prio = 2
	}
	c.pending = append(c.pending, question{cl: *cl, paths: paths, priority: prio})
}

// askAll puts the pending questions to the judge (bounded), concurrently up to the provider's
// own concurrency limit.
func (c *checker) askAll() error {
	qs := c.pending
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].priority < qs[j].priority })
	if len(qs) > c.opt.MaxQuestions {
		qs = qs[:c.opt.MaxQuestions]
	}
	results := make([]*Issue, len(qs))
	var mu sync.Mutex
	var refused []claim.Claim
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	start := time.Now()
	for i := range qs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			is, st := c.ask(qs[i])
			mu.Lock()
			defer mu.Unlock()
			results[i] = is
			c.stats.merge(st)
			refused = append(refused, st.refused...)
		}(i)
	}
	wg.Wait()
	c.stats.ModelSeconds = time.Since(start).Seconds()
	js := c.opt.Judge.Stats()
	c.stats.ModelRequests = js.Requests
	c.stats.ModelUSD = js.CostUSD
	c.stats.ModelTokens = js.InputTokens + js.OutputTokens + js.CacheRead
	for i, is := range results {
		if is != nil {
			c.add(&qs[i].cl, *is)
		}
	}
	c.refusals(refused)
	if err := c.ctx.Err(); err != nil {
		return fmt.Errorf("judge: %w", err)
	}
	return nil
}

type askStats struct {
	questions, answers, rejected, errors int
	refused                              []claim.Claim
}

func (s *Stats) merge(a askStats) {
	s.Questions += a.questions
	s.Answers += a.answers
	s.QuotesRejected += a.rejected
	s.ModelErrors += a.errors
	s.Refused += len(a.refused)
}

// ask runs the self-consistency protocol for one claim.
func (c *checker) ask(q question) (*Issue, askStats) {
	st := askStats{}
	snips := c.snippets(q)
	if len(snips) == 0 {
		return nil, st
	}
	st.questions = 1
	yes, other := 0, 0
	var quote judge.QuoteCheck
	var firstHash string
	for k := 0; k < maxOrderings && yes < votesNeeded && other < votesNeeded; k++ {
		req := judge.NewRequest(fmt.Sprintf("%s-%d-%d", hashID(q.cl.Path), q.cl.Line, k), judgev1.TaskClaimContradiction,
			judgev1.Claim{Repo: c.opt.Repo, Path: q.cl.Path, Line: q.cl.Line, Text: q.cl.Text}, evidenceOf(c.opt.Repo, rotate(snips, k)), "")
		if firstHash == "" {
			firstHash = req.Prompt.Hash
		}
		o, err := c.opt.Judge.Judge(c.ctx, req)
		if err != nil {
			st.errors++
			if errors.Is(err, judge.ErrClassified) {
				st.refused = append(st.refused, q.cl)
				break
			}
			if errors.Is(err, judge.ErrBudget) || c.ctx.Err() != nil {
				break
			}
			other++
			continue
		}
		st.answers++
		switch {
		case o.Verdict == judgev1.Yes:
			yes++
			quote = o.Quote
		case o.QuoteRejected:
			st.rejected++
			other++
		default:
			other++
		}
	}
	if yes < votesNeeded {
		return nil, st
	}
	return c.modelIssue(quote, yes, yes+other, firstHash), st
}

func (c *checker) modelIssue(q judge.QuoteCheck, yes, asked int, promptHash string) *Issue {
	conf := model.ConfLow
	if yes == asked {
		conf = model.ConfMedium
	}
	return &Issue{
		Rule: RuleModelContradiction, Subject: truncate(q.Quote, 80), Confidence: conf,
		Detail:     fmt.Sprintf("judge %s answered yes %d/%d with a quote verified in %s:%d", c.opt.Judge.Name(), yes, asked, q.Evidence.Path, q.Line),
		Suggestion: "Check the sentence against the quoted code and fix whichever is wrong.",
		Quote:      q.Quote, Model: c.opt.Judge.Name(), PromptHash: promptHash,
		Votes: fmt.Sprintf("%d/%d yes", yes, asked), Locations: []Location{{Path: q.Evidence.Path, Line: q.Line, Role: "code"}},
	}
}

func evidenceOf(repo string, snips []snippet) []judgev1.Evidence {
	out := make([]judgev1.Evidence, 0, len(snips))
	for _, s := range snips {
		out = append(out, judgev1.Evidence{Repo: repo, Path: s.Path, From: s.From, To: s.To, Text: s.Text})
	}
	return out
}

func hashID(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:8]
}

func rotate(s []snippet, k int) []snippet {
	out := make([]snippet, 0, len(s))
	switch k % 3 {
	case 1:
		for i := len(s) - 1; i >= 0; i-- {
			out = append(out, s[i])
		}
	case 2:
		out = append(out, s[len(s)/2:]...)
		out = append(out, s[:len(s)/2]...)
	default:
		out = append(out, s...)
	}
	return out
}

// refusals turns claims the cloud judge was not allowed to see into findings, one per
// document, so the operator sees what classification kept back.
func (c *checker) refusals(cls []claim.Claim) {
	seen := map[string]bool{}
	for i := range cls {
		cl := cls[i]
		if seen[cl.Path] {
			continue
		}
		seen[cl.Path] = true
		c.add(&cl, Issue{
			Rule: RuleClassificationRefused, Subject: cl.Path, Confidence: model.ConfHigh,
			Detail:     fmt.Sprintf("not sent to %s: the content is classified private and the provider is a cloud provider", c.opt.Judge.Name()),
			Suggestion: "Use a local provider for private content, or classify the path public in the instance configuration if it really is public.",
		})
	}
}
