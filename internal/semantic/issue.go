// Package semantic is layer ④(c), the semantic audit: it extracts checkable claims from the
// documentation, retrieves evidence deterministically, decides what it can with deterministic
// rules (unit-normalized values, schedules, versions, enumerations, state words, file roles,
// git staleness) and only then asks a small local judge model a yes/no question about what
// remains, accepting an answer only with a verbatim quote from the evidence and a majority over
// several orderings. Every result is a proposal for a human (needs_human).
package semantic

import (
	"github.com/MIERAI/docs-platform/internal/model"
)

// Sub-layers, for reporting and for the findings contract.
const (
	SubValue     = "value"     // deterministic value, schedule, version and list checks
	SubState     = "state"     // state words and file roles vs code state
	SubStaleness = "staleness" // git staleness
	SubModel     = "model"     // judge model
)

// Rules.
const (
	RuleValueMismatch      = "value_mismatch"
	RuleScheduleMismatch   = "schedule_mismatch"
	RuleScheduleDisabled   = "schedule_disabled"
	RuleVersionUnreleased  = "version_unreleased"
	RuleVersionMismatch    = "version_mismatch"
	RuleListIncomplete     = "list_incomplete"
	RuleStateContradiction = "state_contradiction"
	RuleStatusStale        = "status_stale"
	RuleRoleContradiction  = "role_contradiction"
	RuleStaleClaim         = "stale_claim"
	RuleModelContradiction = "model_contradiction"
	// RuleClassificationRefused: private content was not sent to a cloud judge.
	RuleClassificationRefused = "classification_refused"
)

var subLayerOf = map[string]string{
	RuleValueMismatch: SubValue, RuleScheduleMismatch: SubValue, RuleScheduleDisabled: SubValue,
	RuleVersionUnreleased: SubValue, RuleVersionMismatch: SubValue, RuleListIncomplete: SubValue,
	RuleStateContradiction: SubState, RuleStatusStale: SubState, RuleRoleContradiction: SubState,
	RuleStaleClaim: SubStaleness, RuleModelContradiction: SubModel, RuleClassificationRefused: SubModel,
}

// SubLayer returns the sub-layer of a rule.
func SubLayer(rule string) string { return subLayerOf[rule] }

// Location is a doc or code position.
type Location struct {
	Path string
	Line int
	Role string // "doc" or "code"
}

// Issue is one semantic finding candidate.
type Issue struct {
	Rule       string
	Path       string // the document
	Line       int
	Claim      string // the claim sentence
	Subject    string // short stable key of what is wrong (asset id part)
	Detail     string // what the evidence says
	Suggestion string
	Confidence model.Confidence
	Locations  []Location // doc first, then code evidence
	// Quote is a verbatim line of evidence (deterministic rules) or the model's verified quote.
	Quote string
	// Model and PromptHash identify the judge (model rule only).
	Model      string
	PromptHash string
	Votes      string // e.g. "3/3 yes"
}

// SubLayer of the issue.
func (is Issue) SubLayer() string { return SubLayer(is.Rule) }
