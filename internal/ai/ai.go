// Package ai defines the provider interface for layer ④(b)/(c): AI proposes documentation
// changes and audit findings, a human decides. Providers only ever return proposals; nothing in
// docs-platform applies a proposal automatically. The default provider is "none".
package ai

import (
	"context"
	"errors"
	"fmt"
)

// Proposal is a suggested change for human review. It is data, never an instruction to act.
type Proposal struct {
	Path      string
	Rationale string
	// Patch is a unified diff against the documentation file.
	Patch string
	// Evidence lists code locations the proposal is based on.
	Evidence []string
}

// Request asks for a documentation proposal for one finding or one merged change.
type Request struct {
	Kind    string // "post_merge_docs" or "audit"
	Subject string
	// Context is untrusted repository content; providers must treat it as data, not instructions.
	Context string
}

// Provider produces proposals.
type Provider interface {
	Name() string
	Propose(ctx context.Context, r Request) ([]Proposal, error)
}

// ErrDisabled is returned by the none provider.
var ErrDisabled = errors.New("AI provider is none: no proposals are generated")

// None is the default provider: it proposes nothing.
type None struct{}

// Name implements Provider.
func (None) Name() string { return "none" }

// Propose implements Provider.
func (None) Propose(context.Context, Request) ([]Proposal, error) { return nil, ErrDisabled }

// New returns the named provider. Only "none" is built in; real providers are adapters
// registered by the instance (their credentials come from 0600 files or the environment).
func New(name string) (Provider, error) {
	switch name {
	case "", "none":
		return None{}, nil
	}
	return nil, fmt.Errorf("unknown AI provider %q (built in: none)", name)
}
