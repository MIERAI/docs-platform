// Package findings builds documents in the shared findings/v1 contract (OCSF-mapped, consumed
// by the same ledger, scorecard and dashboards as security-platform). See docs/contracts.md for
// the field mapping and the proposed additive 1.3 extension (layer "docs", docs_ metrics).
package findings

import (
	"crypto/sha1" //nolint:gosec // not a security use: the contract defines id = sha1(layer|asset|check)[:12]
	"encoding/hex"
	"time"
)

// SchemaVersion is the findings/v1 minor this producer emits.
const SchemaVersion = "1.3"

// Layer is the findings layer for everything docs-platform reports.
const Layer = "docs"

// Document is one findings/v1 document (one run).
type Document struct {
	SchemaVersion string     `json:"schema_version"`
	Collector     Collector  `json:"collector"`
	Run           Run        `json:"run"`
	Coverage      []Coverage `json:"coverage"`
	Findings      []Finding  `json:"findings"`
	Metrics       []Metric   `json:"metrics,omitempty"`
}

// Collector identifies the producer.
type Collector struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Tools   map[string]string `json:"tools,omitempty"`
}

// Run is the time window of the run.
type Run struct {
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Instance   string `json:"instance,omitempty"`
}

// Coverage says what was (or could not be) inspected; it is what lets a ledger mark a finding
// fixed only when its asset was really covered.
type Coverage struct {
	Asset         string   `json:"asset"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason,omitempty"`
	CompleteScope string   `json:"complete_scope,omitempty"`
	Checks        []string `json:"checks,omitempty"`
}

// Finding is one finding.
type Finding struct {
	ID          string         `json:"id"`
	Check       string         `json:"check"`
	Layer       string         `json:"layer"`
	Asset       string         `json:"asset"`
	Title       string         `json:"title"`
	Severity    string         `json:"severity"`
	Evidence    string         `json:"evidence"`
	Remediation string         `json:"remediation"`
	NeedsHuman  bool           `json:"needs_human"`
	Owner       string         `json:"owner"`
	ObservedAt  string         `json:"observed_at"`
	Kind        string         `json:"kind,omitempty"`
	Refs        []string       `json:"refs,omitempty"`
	Attributes  map[string]any `json:"attributes,omitempty"`
}

// Metric is one SLI sample carried in the document.
type Metric struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
	Help   string            `json:"help,omitempty"`
}

// DocsAttributes is attributes.docs (contracts/findings-v1.schema.json $defs.docsAttributes).
type DocsAttributes struct {
	FindingType   string     `json:"finding_type"`
	RefKind       string     `json:"ref_kind"`
	Subject       string     `json:"subject"`
	Program       string     `json:"program,omitempty"`
	Confidence    string     `json:"confidence"`
	DetectorLayer string     `json:"detector_layer"`
	Locations     []Location `json:"locations"`
	Suggestion    string     `json:"suggestion,omitempty"`
	// Semantic audit (detector_layer "semantic") only.
	Rule       string `json:"rule,omitempty"`
	SubLayer   string `json:"sub_layer,omitempty"`
	Claim      string `json:"claim,omitempty"`
	Quote      string `json:"quote,omitempty"`
	Model      string `json:"model,omitempty"`
	PromptHash string `json:"prompt_hash,omitempty"`
	Votes      string `json:"votes,omitempty"`
}

// Location is a doc or code position.
type Location struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Role string `json:"role"`
}

// ID is the contract's stable finding id: sha1(layer|asset|check), first 12 hex digits.
func ID(layer, asset, check string) string {
	h := sha1.Sum([]byte(layer + "|" + asset + "|" + check)) //nolint:gosec // contract-defined id, not security
	return hex.EncodeToString(h[:])[:12]
}

// Timestamp formats t as the contract's date-time.
func Timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
