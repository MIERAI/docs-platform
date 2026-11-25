// Package contracts embeds the versioned, language-neutral JSON Schemas that are the only
// coupling between docs-platform parts (extractors, core, consumers such as a findings ledger).
package contracts

import _ "embed"

// Findings is findings/v1 as docs-platform emits it (the shared contract plus the proposed
// additive 1.3 extension).
//
//go:embed findings-v1.schema.json
var Findings []byte

// UpstreamFindings is the unmodified upstream findings/v1 (1.2) schema, kept to prove that the
// extension is additive.
//
//go:embed upstream/security-platform-findings-v1.2.schema.json
var UpstreamFindings []byte

// Extract is docs-platform/extract/v1.
//
//go:embed extract-v1.schema.json
var Extract []byte

// Impact is docs-platform/impact/v1.
//
//go:embed impact-v1.schema.json
var Impact []byte

// Config is docs-platform/config/v1.
//
//go:embed config-v1.schema.json
var Config []byte
