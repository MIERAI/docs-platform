// Package draftverify machine-verifies a documentation draft (docs-platform/draft-verify/v1)
// before anyone applies it. A draft counts as verified only when every gate passes:
//
//  1. format: the edited document still parses (Markdown with the repository's own parser,
//     YAML/JSON/TOML, and the fenced blocks of known languages);
//  2. claims: the changed lines are extracted again with the repository's extractors (their
//     references must resolve) and re-verified against the ledger's evidence for the claim,
//     deterministically; whatever would need a model to judge is "needs_model", never a pass;
//  3. examples: runnable examples the draft changes are executed in a real sandbox (internal/
//     sandbox); without one they are "skipped", never run on the host;
//  4. guard: the document is still the blob the draft was made against.
//
// No model is called and nothing is written: Apply is the only writer, and it refuses a
// document whose blob moved.
package draftverify

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/MIERAI/docs-platform/internal/ledger"
	"github.com/MIERAI/docs-platform/internal/sandbox"
)

// SchemaVersion of docs-platform/draft-verify/v1.
const SchemaVersion = "1.0"

// Schema names the contract.
const Schema = "docs-platform/draft-verify/v1"

// Status of a gate or item.
type Status string

// Statuses. Only pass and not_applicable count as verified.
const (
	Pass          Status = "pass"
	Fail          Status = "fail"
	NeedsModel    Status = "needs_model"
	Skipped       Status = "skipped"
	NotApplicable Status = "not_applicable"
	Error         Status = "error"
)

// Gates, in the order they run.
const (
	GateGuard    = "guard"
	GateFormat   = "format"
	GateClaims   = "claims"
	GateExamples = "examples"
)

// rank orders statuses from best to worst (the gate takes its worst item).
var rank = map[Status]int{NotApplicable: 0, Pass: 1, NeedsModel: 2, Skipped: 3, Error: 4, Fail: 5}

// Item is one checked thing of a gate: a parse error, a reference, a claim, an example.
type Item struct {
	Line     int    `json:"line,omitempty"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject,omitempty"`
	Status   Status `json:"status"`
	Detail   string `json:"detail,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
}

// Gate is one gate's outcome.
type Gate struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Items  []Item `json:"items,omitempty"`
}

// Result is docs-platform/draft-verify/v1 for one draft.
type Result struct {
	SchemaVersion string `json:"schema_version"`
	Schema        string `json:"schema"`
	Repo          string `json:"repo"`
	Path          string `json:"path"`
	ClaimID       string `json:"claim_id,omitempty"`
	BaseBlob      string `json:"base_blob"`
	CurrentBlob   string `json:"current_blob"`
	ChangedLines  []int  `json:"changed_lines"`
	Verified      bool   `json:"verified"`
	Sandbox       string `json:"sandbox"`
	Gates         []Gate `json:"gates"`
}

// Input is one draft to verify: the document as it is now (Old, read from the checkout)
// and as the draft would make it (New).
type Input struct {
	Root     string
	RepoName string
	Path     string
	Old, New string
	// BaseBlob is the document's blob the draft was made against.
	BaseBlob string
	// ClaimID is the ledger claim the draft corrects ("" = find claims by line text).
	ClaimID string
	Ledger  *ledger.Ledger
	// Ref is the revision the ledger's evidence is checked against (default HEAD).
	Ref string
	// Sandbox runs examples; nil = no sandbox: examples are skipped.
	Sandbox sandbox.Runner
	Limits  sandbox.Limits
	Image   string
}

// Verify runs every gate on one draft.
func Verify(ctx context.Context, in *Input) *Result {
	if in.Ref == "" {
		in.Ref = "HEAD"
	}
	changes := diffLines(splitLines(in.Old), splitLines(in.New))
	changed := changedNew(changes)
	r := &Result{
		SchemaVersion: SchemaVersion, Schema: Schema, Repo: in.RepoName, Path: in.Path, ClaimID: in.ClaimID,
		BaseBlob: in.BaseBlob, ChangedLines: sortedKeys(changed), Sandbox: "none",
	}
	if in.Sandbox != nil {
		r.Sandbox = in.Sandbox.Name()
	}
	guard, cur := guardGate(ctx, in.Root, in.Old, in.BaseBlob)
	r.CurrentBlob = cur
	format := formatGate(ctx, in.Path, in.Old, in.New, changed)
	claims := claimsGate(ctx, in, changes)
	var examples Gate
	if format.Status == Fail {
		examples = Gate{Name: GateExamples, Status: Skipped, Detail: "not run: the document does not parse"}
	} else {
		examples = examplesGate(ctx, in, touchedNew(changes))
	}
	r.Gates = []Gate{guard, format, claims, examples}
	r.Verified = len(changes) > 0
	for _, g := range r.Gates {
		if g.Status != Pass && g.Status != NotApplicable {
			r.Verified = false
		}
	}
	return r
}

// worst is the worst status of the items (empty: def).
func worst(items []Item, def Status) Status {
	if len(items) == 0 {
		return def
	}
	w := items[0].Status
	for _, it := range items[1:] {
		if rank[it.Status] > rank[w] {
			w = it.Status
		}
	}
	return w
}

func sortedKeys(m map[int]bool) []int {
	return slices.Sorted(maps.Keys(m))
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
