# Changelog

## 0.3.0 - 2026-11-25

Direction: detect drift at change time, measure by history replay, models are swappable
assistants behind a contract.

### Added
- `docsp impact` derives the doc sections a change affects from layer ③'s reference graph
  (Markdown heading sections and comment blocks → paths and identifiers they mention), lists
  each with its reason (`reread` / `updated`); explicit `.docsp.yaml` rules stay the gate.
  impact/v1 1.1 adds `sections[]` and the metric `docs_impact_sections`.
- Contract judge/v1 (request/response JSON Schemas, public Go package `judgev1`) and provider
  plugins: `none`, `ollama` (moved behind the contract), `claude-code` (Claude Code CLI with the
  operator's login), `anthropic` (separate module `providers/anthropic`, official Go SDK) and
  `exec` (any program speaking judge/v1 JSON Lines).
- Per-task model routing (`ai.judge`, `ai.draft`) in the new instance configuration
  (instance/v1); a guard enforcing data classification (cloud providers only receive `public`
  content), per-run budgets (requests, tokens, USD), contract validation and quote verification.
- Optional judgement ("does this change contradict this section?") and draft edit per affected
  section in `docsp impact`; proposals only.
- judge/v1 1.1 and judge-tools/v1: `mode: agentic`, evidence lists verified at path:line±2,
  tool transcripts, and a confined read-only tool executor (`list_dir`, `read_file`, `grep`,
  `git_log`, `git_show`) run by the core.
- `docsp eval-model`: per-task scores on a frozen eval set (eval/v1 results), switching rule
  from instance thresholds, `--dry-run` cost estimate.
- `bench replay`: history replay benchmark (primary metric) and frozen eval-set builder.
- `docsp import-findings`: reviewed backlog (JSON Lines) → findings/v1 (`docs.backlog.<type>`).
- findings: `finding_type: backlog`, `detector_layer: review`, `attributes.docs.actual`,
  `gt_type`; semantic rule `classification_refused`.

### Changed
- Model settings moved from `.docsp.yaml` (`audit.ai`, now rejected) to the instance
  configuration; `docsp audit --ai` replaced by `--instance-config` / `--no-model`.
- `show` joins the read-only git allowlist.
