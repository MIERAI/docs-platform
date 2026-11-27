package outcome

import (
	"strconv"
	"strings"
	"time"

	"github.com/MIERAI/docs-platform/internal/factorypr"
	"github.com/MIERAI/docs-platform/internal/ledger"
)

// tickLabel is what a box of the PR comment says about the report: the document is out of
// date or the code is wrong - either way the inconsistency was real - or there is none.
var tickLabel = map[string]string{
	factorypr.OptionDesignStale: LabelTP,
	factorypr.OptionCodeWrong:   LabelTP,
	factorypr.OptionNotAProblem: LabelFP,
}

// FromFeedback turns the boxes ticked in a pull request's review comment into human events.
// A box names its claim by path:line at the PR's head (Feedback.Rule); Match links it to a
// ledger claim when one is at hand. Boxes of unknown options are ignored.
func FromFeedback(repo string, pr int, ref string, fb []factorypr.Feedback, at time.Time) []Event {
	ts := at.UTC().Format(time.RFC3339)
	var out []Event
	for _, f := range fb {
		label, ok := tickLabel[f.Option]
		if !ok {
			continue
		}
		p, line := splitRule(f.Rule)
		out = append(out, Event{
			SchemaVersion: SchemaVersion, At: ts, Repo: repo, Path: p, Line: line, Category: f.Category,
			Kind: KindHuman, Label: label, Source: SourcePRTick, Ref: ref, PR: pr, Option: f.Option,
		})
	}
	return out
}

func splitRule(rule string) (string, int) {
	i := strings.LastIndex(rule, ":")
	if i < 0 {
		return rule, 0
	}
	n, err := strconv.Atoi(rule[i+1:])
	if err != nil {
		return rule, 0
	}
	return rule[:i], n
}

// Match fills the claim id and text of events that have none from the ledger claim of the
// same repository, path and line - the one with the event's category when there are several
// (a line documenting several targets is several claims).
func Match(events []Event, l *ledger.Ledger) {
	for i := range events {
		e := &events[i]
		if e.ClaimID != "" {
			continue
		}
		var hit *ledger.Claim
		for j := range l.Claims {
			c := &l.Claims[j]
			if c.Repo != e.Repo || c.Path != e.Path || c.Line != e.Line {
				continue
			}
			if hit == nil || c.Category == e.Category {
				hit = c
			}
		}
		if hit != nil {
			e.ClaimID, e.Text = hit.ID, hit.Text
		}
	}
}

// key identifies what a person said once: a tick (pull request, box) or an audit label
// (claim, label). Other kinds are never deduplicated: each run's events are new facts.
func key(e Event) string {
	switch e.Kind {
	case KindHuman:
		return strings.Join([]string{e.Kind, e.Repo, strconv.Itoa(e.PR), e.Path, strconv.Itoa(e.Line), e.Option, e.Category}, "\x00")
	case KindAudit:
		return strings.Join([]string{e.Kind, e.Repo, e.ClaimID, e.Label}, "\x00")
	}
	return ""
}

// Unseen keeps the human and audit events the stream does not already hold, so reading the
// same ticks or importing the same audit sheet twice appends nothing.
func Unseen(stream, fresh []Event) []Event {
	have := map[string]bool{}
	for _, e := range stream {
		if k := key(e); k != "" {
			have[k] = true
		}
	}
	var out []Event
	for _, e := range fresh {
		k := key(e)
		if k != "" && have[k] {
			continue
		}
		if k != "" {
			have[k] = true
		}
		out = append(out, e)
	}
	return out
}
