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

// JudgeRequest is docs-platform/judge/v1 (request): one question for a model provider.
//
//go:embed judge-v1-request.schema.json
var JudgeRequest []byte

// JudgeResponse is docs-platform/judge/v1 (response): a provider's answer.
//
//go:embed judge-v1-response.schema.json
var JudgeResponse []byte

// Instance is docs-platform/instance/v1: per-installation settings (providers, budgets,
// classification, switching thresholds).
//
//go:embed instance-v1.schema.json
var Instance []byte

// EvalResult is docs-platform/eval/v1: the scores of one provider on a frozen eval set.
//
//go:embed eval-v1.schema.json
var EvalResult []byte

// JudgeTools is docs-platform/judge-tools/v1: the read-only tools of the agentic judge mode.
//
//go:embed judge-tools-v1.schema.json
var JudgeTools []byte

// Ledger is docs-platform/ledger/v1: judged claims and the validated evidence they rest on.
//
//go:embed ledger-v1.schema.json
var Ledger []byte

// Stale is docs-platform/stale/v1: the ledger's claims checked against a newer revision.
//
//go:embed stale-v1.schema.json
var Stale []byte

// Schedule is docs-platform/schedule/v1: one cycle-count run of the consistency scheduler.
//
//go:embed schedule-v1.schema.json
var Schedule []byte

// SLO is docs-platform/slo/v1: documentation consistency SLIs, error budget and cost.
//
//go:embed slo-v1.schema.json
var SLO []byte

// Report is docs-platform/report/v1: validated documentation problems for people.
//
//go:embed report-v1.schema.json
var Report []byte
