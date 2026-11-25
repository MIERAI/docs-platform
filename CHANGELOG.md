# Changelog

## 0.4.0 - 2026-11-26

Direction: the per-line snippet judge starved a strong model (27% recall on a held-out TEST
set); a redesigned agentic judge scored 80% with one adjudicated control false positive in a
prototype. This release ports that design into the product, behind the same provider and
judge contracts, so the model stays swappable by configuration.

### Added
- Agentic mode of layer ④c (`internal/agentic`; `docsp audit -mode agentic`,
  `docsp eval-model -mode agentic`):
  - **repository drift scan**, once per repository and HEAD (cached by HEAD SHA, prompt and
    model; mode 0600, outside the repository): the model lists the history changes that make
    older docs wrong; the harness keeps an entry only if its commits are ancestors of HEAD,
    its quotes are verbatim, and a path it calls removed is really gone. Only validated
    entries (D1, D2, ...) are shared with the per-line judgments.
  - **per-line judgment** (default) or per-document groups of up to six lines (`-group doc`,
    cheaper): the model answers facts with a status (`contradicted`, `incomplete_list`,
    `outdated_wording`, `dangling_reference`, `unverifiable_in_repo`, `confirmed`) and
    evidence; the harness re-checks every piece of evidence - `quote` (path:line±2,
    verbatim), `absence` (the exact search re-run; accepted only without a match),
    `drift` (a validated drift entry), `path_missing` (new: the path exists nowhere) - and
    derives the verdict from validated evidence only, in the priority contradicted >
    dangling_reference > incomplete_list > outdated_wording. `unverifiable_in_repo` is
    reported separately, never as an error.
  - audit rules `agentic_contradicted`, `agentic_dangling_reference`,
    `agentic_incomplete_list`, `agentic_outdated_wording` (sub-layer model, medium).
- judge/v1 **1.2** (additive): task types `repo_drift`, `claim_facts`, `doc_facts` (agentic
  only), `claims[]` (doc_facts), `limits.max_turns`, `limits.max_request_usd` (ends one
  request, never the run), `tools.definitions`; response `answer` (the structured answer,
  passed through) and `answer_parse` (`schema` / `fallback` / `repaired` / `failed`).
  `judgev1.ParseObject` is the shared tolerant parser; `judgev1.NewStructuredResponse` fills
  a response.
- judge-tools/v1 **1.1**: `search` (the search absence evidence re-runs), `glob`,
  `git_changes` (deleted / renamed / added paths with their commits), `changes_since`,
  `find_references`, `check_quote`; tool definitions for models.
- Providers in agentic mode: `claude-code` (no built-in tools at all; its only tools are
  docsp's judge-tools, served over MCP stdio by a docsp child process, `docsp
  judge-tools-mcp`; `--json-schema` structured output; `--max-turns`), `ollama` (native tool
  calls, then the final answer with the reply schema as `format`), `exec` (as before).
- Instance configuration `ai.judge.agentic` (`max_steps`, `drift_max_steps`,
  `max_request_usd`, `drift_max_request_usd`, `group`).
- eval/v1 **1.1**: `mode`, `group`, per-repository `drift` summaries; per item `category`,
  `claimed`, `parse`, `evidence` / `evidence_ok`, `facts` with every evidence check; scores
  `categories`, `unverifiable_rate`, `evidence_rejection_rate`, `parse_failures`,
  `parse_fallbacks`. `eval-model -mode agentic -worktree NAME=DIR` refuses a checkout that is
  not at the items' revision. Eval items may use task type `claim_facts`; source
  `verified_control`.
- `docsp-judge-replay` (`cmd/docsp-judge-replay`): an offline exec provider answering from
  canned answers, for end-to-end tests and for re-scoring a frozen run through the current
  harness at no cost.
- Guard statistics `parse_failures` and `parse_fallbacks`; cache-aware agentic dry-run
  estimates.

### Changed
- Path confinement refuses any path segment named `.git` (not only a leading one); `.github`
  and `.gitignore` stay readable. `CleanPath` also refuses `./-x` (it cleaned to `-x`; found
  by fuzzing).
- `blame` and `merge-base` join the read-only git allowlist (changes_since, drift
  validation).
- An empty directory counts as absent (git does not track one).

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
