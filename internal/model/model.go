// Package model holds the language-neutral data model shared by every part of docs-platform.
// It mirrors contracts/extract-v1.schema.json: extractor plugins (built in or external) produce
// these values, the matcher consumes them. Field names are part of the contract.
package model

import "fmt"

// Kind is the category of an identifier that documentation can mention and code can define.
type Kind string

// Identifier kinds. Adding a kind is a minor (additive) contract change.
const (
	KindCLICommand    Kind = "cli_command"
	KindCLIFlag       Kind = "cli_flag"
	KindEnvVar        Kind = "env_var"
	KindConfigKey     Kind = "config_key"
	KindMetric        Kind = "metric"
	KindFilePath      Kind = "file_path"
	KindLink          Kind = "link"
	KindAnchor        Kind = "anchor"
	KindMakeTarget    Kind = "make_target"
	KindWorkflowInput Kind = "workflow_input"
	KindProgram       Kind = "program"
	KindTerm          Kind = "term"
)

// Confidence says how sure an extractor is that a token really is what it classified it as.
type Confidence int

// Confidence levels, ordered.
const (
	ConfLow Confidence = iota + 1
	ConfMedium
	ConfHigh
)

// String renders the level as used in contracts.
func (c Confidence) String() string {
	switch c {
	case ConfHigh:
		return "high"
	case ConfMedium:
		return "medium"
	case ConfLow:
		return "low"
	default:
		return "unknown"
	}
}

// ParseConfidence is the inverse of Confidence.String; ok is false for unknown input.
func ParseConfidence(s string) (Confidence, bool) {
	switch s {
	case "high":
		return ConfHigh, true
	case "medium":
		return ConfMedium, true
	case "low":
		return ConfLow, true
	default:
		return 0, false
	}
}

// MarshalText encodes the level as its contract string.
func (c Confidence) MarshalText() ([]byte, error) {
	if _, ok := ParseConfidence(c.String()); !ok {
		return nil, fmt.Errorf("invalid confidence %d", int(c))
	}
	return []byte(c.String()), nil
}

// UnmarshalText decodes a contract string.
func (c *Confidence) UnmarshalText(b []byte) error {
	v, ok := ParseConfidence(string(b))
	if !ok {
		return fmt.Errorf("invalid confidence %q", string(b))
	}
	*c = v
	return nil
}

// Symbol is something code defines: a flag, an env var it reads, a metric it exports, a file.
type Symbol struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
	// Program scopes flags and commands to the CLI that defines them ("" = unknown program).
	Program string `json:"program,omitempty"`
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	// Public symbols are part of the user-facing surface and must be documented.
	Public     bool       `json:"public"`
	Confidence Confidence `json:"confidence"`
	Source     string     `json:"source"`
}

// Reference is a mention of an identifier in documentation.
type Reference struct {
	Kind Kind   `json:"kind"`
	Text string `json:"text"`
	// Program is the command a flag or sub-command was written with ("" = standalone mention).
	Program    string     `json:"program,omitempty"`
	Path       string     `json:"path"`
	Line       int        `json:"line"`
	Confidence Confidence `json:"confidence"`
	Context    string     `json:"context"`
	Source     string     `json:"source"`
	// Qualifier is the text just before the reference on its line ("MIERAI/other docs/x.md"):
	// it lets the matcher recognise references explicitly qualified with another repository.
	Qualifier string `json:"qualifier,omitempty"`
}

// Mention is a token that appears somewhere in documentation; used for the reverse check.
type Mention struct {
	Token  string `json:"token"`
	InCode bool   `json:"in_code"`
}

// Suppression is an inline, reasoned opt-out written in a document.
type Suppression struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// Result is what one extractor returns for one file.
type Result struct {
	Symbols      []Symbol      `json:"symbols,omitempty"`
	References   []Reference   `json:"references,omitempty"`
	Mentions     []Mention     `json:"mentions,omitempty"`
	Suppressions []Suppression `json:"suppressions,omitempty"`
}

// Merge appends o into r.
func (r *Result) Merge(o Result) {
	r.Symbols = append(r.Symbols, o.Symbols...)
	r.References = append(r.References, o.References...)
	r.Mentions = append(r.Mentions, o.Mentions...)
	r.Suppressions = append(r.Suppressions, o.Suppressions...)
}
