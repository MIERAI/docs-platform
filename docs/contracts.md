# Data contracts

docs-platform parts are coupled only through versioned, language-neutral JSON Schemas in
`contracts/`. Rules (shared with security-platform and slo-platform):

- every document carries `schema_version: "<major>.<minor>"`;
- a minor version only adds optional fields or values; consumers ignore unknown fields;
- a breaking change is done in two steps: consumers accept both majors first, producers switch
  second, the old major is dropped last;
- metric names are part of the contract (rename = dual-write first, delete later).

| Contract | File | Producer → consumer |
|---|---|---|
| findings/v1 (shared) | `contracts/findings-v1.schema.json` | `docsp check` → findings ledger, scorecard, dashboards |
| extract/v1 | `contracts/extract-v1.schema.json` | extractor plugins (built in or external) → core |
| impact/v1 | `contracts/impact-v1.schema.json` | `docsp impact` → CI, SLI aggregation |
| config/v1 | `contracts/config-v1.schema.json` | `.docsp.yaml` → `docsp` |
| judge/v1 | `contracts/judge-v1-request.schema.json`, `contracts/judge-v1-response.schema.json` | core ↔ model provider (built in or any program) |
| judge-tools/v1 | `contracts/judge-tools-v1.schema.json` | agentic provider ↔ core (read-only worktree tools) |
| instance/v1 | `contracts/instance-v1.schema.json` | the operator's instance configuration → `docsp` |
| eval/v1 | `contracts/eval-v1.schema.json` | `docsp eval-model` → stored results, comparison over time |

## findings/v1

docs-platform emits the findings contract defined by security-platform (unmodified upstream copy:
`contracts/upstream/security-platform-findings-v1.2.schema.json`), at minor **1.3 (proposed)**.

### Proposed additive extension (1.3)

1. `findings[].layer` gains the value `docs`.
2. `metrics[].name` accepts the `docs_` prefix (`^(security|docs)_[a-z0-9_]+$`).
3. `findings[].attributes.docs` is documented (`$defs.docsAttributes`, including the optional
   semantic-audit fields `rule`, `sub_layer`, `claim`, `quote`, `model`, `prompt_hash`, `votes`
   and `detector_layer: semantic`); `attributes` was already a free object, so this changes
   nothing for existing consumers.

`TestContract` in `internal/check` validates real output against the 1.3 schema and against the
unmodified upstream 1.2 schema, and fails if any difference other than (1) and (2) appears.

Later, optional: promote `locations[]` to a typed top-level field (useful to security code
scanners as well) and register the `doc` and `code` asset types.

### Field mapping

| Field | docs-platform value |
|---|---|
| `collector.name` / `version` | `docs-check` (layer ③), `docs-audit` (layer ④c) or `docs-backlog` (import) / docsp version |
| `collector.tools` | `extract-<name>: <version>` for every extractor, `residue: <version>` when used |
| `coverage[]` | `doc:<repo>` and `code:<repo>` ok; `doc:<repo>/<file>` gap for a file no extractor could read |
| `id` | `sha1(layer + "|" + asset + "|" + check)[:12]` (as upstream) |
| `check` | `docs.reference.unresolved.<kind>`, `docs.symbol.undocumented.<kind>`, `docs.reference.outside_repo`, `docs.residue.term`; audit: `docs.semantic.<rule>` |
| `layer` | `docs` |
| `asset` | wrong/outside/residue: `doc:<repo>/<doc path>#<kind>:[<program> ]<subject>`; undocumented: `code:<repo>/<kind>:[<program> ]<subject>`; semantic: `doc:<repo>/<doc path>#semantic:<rule>:<subject>` - never a line number |
| `severity` | wrong/outside: high confidence → `medium`, medium → `low`, low → `info`; undocumented: high → `low`, else `info`; residue → `medium`; semantic: medium → `low`, low → `info` (never higher: proposals) |
| `kind` | `compliance` |
| `needs_human` | `true` unless confidence is high; always `true` for semantic findings |
| `owner` | `.docsp.yaml` `owner` (default `unassigned`) |
| `evidence` | `path:line` list of every location |
| `attributes.docs.finding_type` | `wrong_or_obsolete`, `undocumented`, `outside_repo`, `residue`, `semantic`, `backlog` (reserved: `impact_unaccompanied`) |
| `attributes.docs.detector_layer` | `reference` (check), `semantic` (audit), `review` (imported backlog); reserved: `generated`, `executable`, `impact`, `ai_audit` |
| `attributes.docs.actual`, `gt_type` | backlog only: the reviewer's description of the code and defect type |
| `attributes.docs.rule`, `sub_layer` | semantic only: the rule (`value_mismatch`, `list_incomplete`, `state_contradiction`, `model_contradiction`, ...) and its sub-layer (`value`, `state`, `staleness`, `model`) |
| `attributes.docs.claim`, `quote` | semantic only: the sentence checked, and the verbatim evidence line (for the judge: its quote, verified verbatim in the evidence) |
| `attributes.docs.model`, `prompt_hash`, `votes` | judge only: `provider:model`, hash of the exact prompt, self-consistency result (`2/3 yes`) |
| `attributes.docs.locations[]` | `{path, line, role: doc|code}` |
| `attributes.docs.suggestion` | nearest defined name of the same kind, if any |

OCSF export follows the upstream mapping (`class_uid 2003 Compliance Finding`, `compliance.control`
= check).

### Metrics

| Name | Labels | Meaning |
|---|---|---|
| `docs_references` | `repo, kind, status` | references by resolution status (`resolved`, `unresolved`, `external`, `outside`, `ignored`) |
| `docs_public_symbols` | `repo, kind, documented` | public code symbols and whether documentation mentions them |
| `docs_reference_resolution_ratio` | `repo` | resolved / (resolved + unresolved) |
| `docs_public_symbol_coverage_ratio` | `repo` | documented / public |
| `docs_impact_changes` (impact/v1) | `impacting, with_docs` | one sample per `docsp impact` run |
| `docs_semantic_findings` (audit) | `repo, rule, sub_layer` | semantic audit findings by rule |
| `docs_impact_sections` (impact/v1) | `status` | sections a change asks to re-read (`reread`) or already edits (`updated`) |

## extract/v1

What an extractor found. Built-in extractors produce it in memory; external extractors print it
on stdout (`docsp extract` prints the merged built-in result in the same shape).

```json
{
  "schema_version": "1.0",
  "extractor": {"name": "rust", "version": "0.1.0", "side": "code"},
  "symbols": [{"kind": "env_var", "name": "RUST_LOG", "program": "", "path": "src/main.rs", "line": 12,
               "public": true, "confidence": "high", "source": "rust"}],
  "references": [{"kind": "cli_flag", "text": "--fast", "program": "tool", "path": "README.md", "line": 3,
                  "confidence": "medium", "context": "code_block", "source": "markdown", "qualifier": ""}],
  "mentions": [{"token": "--fast", "in_code": true}],
  "suppressions": [{"path": "README.md", "line": 9, "reason": "kept for one release"}]
}
```

Kinds: `cli_command`, `cli_flag`, `env_var`, `config_key`, `metric`, `file_path`, `link`,
`anchor`, `make_target`, `workflow_input`, `program`, `term`. Flags are written with their
dashes; anchors as `<file>#<slug>`; links as repo-relative `<file>[#anchor]`.

## impact/v1 (1.1)

One `docsp impact` run: base, head, number of changed files, one entry per rule
(`not_triggered`, `docs_updated`, `missing_docs`, `waived` with the code and docs that changed),
the waiver reason if any, and the verdict (`pass`, `fail`, `waived`, `not_applicable`).

1.1 adds `sections[]` (optional): every doc section the change affects, derived from the
reference graph - `{path, heading, from, to, status: reread|updated, reasons[{kind:
path|identifier|rule, subject, changed, doc_line}]}`, plus `judgement {verdict, quote,
quote_rejected, model, prompt_hash, error}` and `draft` when a model is configured. Sections are
advisory: the verdict is decided by the explicit rules only.

## judge/v1 (1.1)

The only coupling between docs-platform and a model. Request: `id`, `task.type`
(`claim_contradiction`, `claim_incomplete`, `change_contradiction`, `draft_edit`), `claim`
(repo, path, line, text), `evidence[]` with ids `E1..`, `require_quote`, the exact `prompt`
(version, 16-hex hash, system, user, reply JSON Schema), `model`, `limits` (max output tokens,
timeout, remaining USD / tokens: the provider must refuse rather than exceed), `dry_run`, and
(1.1) `mode` (`snippet` | `agentic`) and `tools`. Response: `verdict` (yes/no/unknown), the
required `quote` field (one evidence line verbatim for a yes, else ""), `quote_evidence_id`,
`confidence`, `rationale`, `draft.text` (draft_edit), `model {provider, id}` (the model that
actually answered), `prompt_hash`, `usage` (input/output/cache tokens, `cost_usd`, `estimated`,
`billing: none|api|subscription`), `latency_ms`, `error {code, message}` and (1.1, agentic)
`evidence[{path, line, quote, rev}]` and `transcript[{n, tool, args, result_bytes, error}]`.

Transport for out-of-process providers: JSON Lines on stdin/stdout, one request → (agentic: any
number of judge-tools/v1 `tool_call` lines answered by `tool_result` lines) → one response. The
core validates every response against the schema, verifies quotes (snippet: verbatim in one
evidence line; agentic: at path:line±2 in the worktree or at `rev`) and downgrades a yes it
cannot verify to unknown.

Who may receive what is decided by the core, not the provider: a provider declares `Cloud()`
and its `modes`; the instance configuration classifies repositories and paths `public` /
`private` (default private); a cloud provider is refused any request or tool result touching
private content.

## judge-tools/v1

Read-only tools for the agentic mode, executed by the core: `list_dir {path}`, `read_file
{path, from_line, to_line}` (≤ 400 lines), `grep {pattern (RE2), path, max_results ≤ 200}`,
`git_log {path, max_count ≤ 50}`, `git_show {rev, path?}`. Paths are repository-relative;
absolute paths, `..`, `.git` and symlinks out of the worktree are refused (`os.Root`); results
are capped (default 32 KiB per result, 512 KiB and 20 steps per request); unknown arguments are
errors; revisions are validated (no option injection).

## instance/v1

The operator's settings (YAML): `ai.judge` and `ai.draft` (provider, model, effort, timeout,
concurrency, `budget {max_requests, max_usd, max_tokens}`, provider-specific blocks),
`classification {default, repos, paths}`, `eval {judge, draft}` switching thresholds, and
`prices` overriding the built-in list prices used for estimates.

## eval/v1

One `docsp eval-model` run of one task: the frozen set (`path`, `sha256`, item count), task,
provider, model, prompt template hash, start time, `dry_run`, `scores` (judge: tp/fp/fn/tn,
precision, recall, unknown rate, quote-rejection rate, per split; draft: drafted,
supported rate, mean similarity; both: tokens, cost, USD per item, p50 latency, a note that
strong-AI-labelled positives measure agreement, not accuracy), the `switch` decision
(`qualifies`, failed thresholds) and per-item results. The eval set itself is JSON Lines of
items (`id, source, label, repo, split, revision, task, task_type, expected, claim, evidence,
reference_fix, reference_actual`); prompts are rebuilt from the current templates at run time,
so a template change is visible as a different `prompt_template_hash`.
