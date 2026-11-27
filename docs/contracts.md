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
| contract/v1 | `contracts/contract-v1.schema.json` | `docsp contract` → reviewers, a findings importer, dashboards (design/code surfaces, mismatch items, direction from history) |
| outcome/v1 | `contracts/outcome-v1.schema.json` | `docsp outcome diff/ticks/label` → `docsp outcome report` (precision of docs-platform's own problem reports) |

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

## judge/v1 (1.3)

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

1.2 (additive) brings the agentic judge of layer ④c. Three task types, agentic mode only:
`repo_drift` (list the history changes that make older docs wrong), `claim_facts` (check one
line fact by fact) and `doc_facts` (the same for up to a few lines of one document, listed in
the new `claims[]`). Their answer is a structured object, not a verdict: the provider passes
it through in the new response field `answer` and says in `answer_parse` how it got it
(`schema` = schema-enforced structured output, `fallback` = found in free text by the
tolerant parser `judgev1.ParseObject`, `repaired` = stray closing braces removed, `failed` =
none; the response then carries error `bad_reply`). `verdict` stays `unknown`: the core
validates every piece of evidence in the answer and derives the verdict itself. Reply schemas
(in `prompt.reply_schema`):

- `repo_drift`: `{drift: [{kind, old, new, summary, commits[], evidence[{path, line, quote}]}]}`
- `claim_facts`: `{facts: [{fact, status, evidence[]}], reason}` with status one of
  `confirmed`, `unverifiable_in_repo`, `contradicted`, `dangling_reference`,
  `incomplete_list`, `outdated_wording`, and evidence `{kind: quote, path, line, quote}`,
  `{kind: absence, search_pattern, search_scope, search_regex}`, `{kind: drift, drift_id}` or
  `{kind: path_missing, path}`
- `doc_facts`: `{lines: [{line, facts, reason}]}`

`limits.max_turns` bounds the model turns of one request and `limits.max_request_usd` its
cost (reaching it ends that request without an answer, never the run); `tools.definitions`
describes each allowed tool (name, description, JSON Schema of the arguments) so a provider
can hand them to its model. A consumer of 1.1 that ignores unknown fields is unaffected; a
1.1 provider never receives the new task types (it does not declare `agentic`, or the core's
Guard refuses the mode).

1.3 (additive) adds three structured task types in snippet mode (the files are in the
prompt), for projects of a design-first API factory whose proposals the factory itself checks
(`factory try`): `rule_mutant` (`{path, old, new, rationale}`: the smallest edit of
hand-written code that breaks one business rule; `path` "" = none), `draft_example`
(`{inserts: [{anchor, text}], rationale}`: a new example inserted after exact anchor lines of
the design) and `draft_rule` (`{edits: [{old, new}], rationale}`: the smallest rewording of a
rule - and of the examples encoding the old behaviour - that the code contradicts). The core
applies each as an exact text edit and keeps it only when the factory's run proves it; the
model never decides an outcome.

Who may receive what is decided by the core, not the provider: a provider declares `Cloud()`
and its `modes`; the instance configuration classifies repositories and paths `public` /
`private` (default private); a cloud provider is refused any request or tool result touching
private content.

## judge-tools/v1 (1.2)

Read-only tools for the agentic mode, executed by the core: `list_dir {path}`, `read_file
{path, from_line, to_line}` (≤ 400 lines), `grep {pattern (RE2), path, max_results ≤ 200}`,
`git_log {path, max_count ≤ 50}`, `git_show {rev, path?}`; 1.1 adds `search {pattern, scope,
regex, max_results}` (case-insensitive fixed string unless regex; the scope glob is broad:
`*` crosses `/`; absence evidence re-runs exactly this search), `glob {pattern}`,
`git_changes {kind (A/D/R/M letters, default DR), path, max_count ≤ 150}` (paths with the
commit that added/deleted/renamed them), `changes_since {path, line}` (blame of the line,
the files it names, later commits to them), `find_references {name}` and `check_quote {path,
line, text}`. Paths are repository-relative; absolute paths, `..`, any path segment named
`.git` (`.github` is fine) and symlinks out of the worktree are refused (`os.Root`); results
are capped (default 32 KiB per result, 512 KiB and 20 steps per request; the agentic judge
uses 16 steps per line and 45 / 1 MiB for the drift scan); unknown arguments are errors;
revisions are validated (no option injection).

1.2 adds `outline {path}` and `config_value {key}`, and `tool_result.text`: the result
rendered for a model (file lines numbered `12: text`, `path:line: text` matches, one entry
per line, nothing escaped). Line numbers must be read, never counted: a model given the raw
JSON array cited lines several lines off. MCP and Ollama transports show `text`; exec
providers should hand `text` to their model.

Transports: exec providers exchange `tool_call` / `tool_result` lines (above). For a CLI
provider (claude-code) the core starts `docsp judge-tools-mcp -spec FILE` as the CLI's only
MCP server (stdio JSON-RPC: `initialize`, `tools/list`, `tools/call`); the spec file (mode
0600, in the run's private temporary directory) fixes the worktree, the classification
policy, the limits and the transcript file. Ollama gets the definitions as native tools and
the core runs each call in process.

## ledger/v1 and stale/v1

ledger/v1 (`contracts/ledger-v1.schema.json`): `claims[]` - `id` (sha256 of repository,
document path and normalised line text: stable across line moves), `repo`, `path`, `line`,
`text`, `doc_blob`, `category`, `verdict`, `verified_at`, `verified_ref` (a commit), `model`,
`prompt_hash`, `facts[{fact, status, valid}]`, `private`, and `evidence` (validated only):
`quotes[{repo, path, line, blob}]`, `drift[{id, commits, files[{path, blob}], removed_path}]`,
`absence[{pattern, scope, regex, skip_file, files_hash, files}]` (the covered file set),
`path_missing[{path, candidates, name}]`; optional footprints (not evidence; recorded with
`-footprint`): `weak[{tool, scope}]`, `members[{dir | glob}]`; `pinned[{path, blob}]` (always
on, kept across re-judging: the files a generator's manifest says the claim governs, written by
`docsp factory-rules`). Repository groups add `target {repo, ref}` (the
declared revision the claim was judged against; `verified_ref` is then that target's
commit) and `doc_ref` (the document repository's commit).

stale/v1 (`contracts/stale-v1.schema.json`): one repository at one revision - `counts` and
`claims[{id, repo, path, line, category, status: fresh|stale|recheck, reasons[{kind, path,
detail}], new_line, verified_ref}]`; reason kinds `doc_removed`, `doc_line_changed`,
`evidence_changed`, `evidence_removed`, `drift_evidence_changed`,
`removed_path_reappeared`, `absence_scope_changed`, `path_appeared`, `no_evidence`, and with
footprint rules `weak_scope_changed`, `sibling_added`, `member_set_changed`; `pinned_changed`
when a pinned file changed (a removed one is `evidence_removed`).

ledger/v1 1.1 and stale/v1 1.1 (additive; 1.0 documents are read unchanged): each quote may
carry `scope {kind: go_decl|yaml_key|lines, name, start, end}` - the part of the quoted file
the claim depends on at the quote's blob (the enclosing Go declaration, value spec or literal
table entry; the YAML key path with its subtree; otherwise the line ±3) - and, once
re-anchored without judging, `carried` (`evidence_moved`, `evidence_reformatted`,
`evidence_outside_scope`) and `carried_ref`. A quote with a scope is stale only when the change
of its file touches the scope (its text, bounds or name), the file is largely rewritten (under
half of its old lines survive) or it cannot be located; a quote without one is stale on any
change, as in 1.0, and so is every quote of an enumerating claim (a list: its completeness
depends on the rest of the file). stale/v1 adds `carried_forward[{kind, path, line, new_line, blob, scope,
ref}]` on fresh claims; `docsp recheck` writes them back to the ledger (verdict and
`verified_ref` unchanged). Drift, absence, path-missing and pinned evidence are unchanged.

## pr-review/v1 (`docsp pr-comment`)

`schema_version`, `repo`, `head`, `github`, `rechecked` (claims judged at the head), `problems
[{id, path, line, text, category, before, facts[], code[{path, line, text}]}]` - claims judged at
the head that are problems now and were not in the default branch's ledger (`before`: their
category there, "" = absent) - and `feedback[]` (as factory-pr/v1). Every judge also appends
`<provider:model> <usd>` to the file `DOCSP_SPEND_LOG` names when it closes.

## factory-pr/v1 and rulecover/v1 (API factory projects)

Both read a factory's `factory.api-manifest/v1` and run its `factory try`
(`factory.try-request/v1` → `factory.try-result/v1`: the given file changes applied to a
temporary copy, deterministic regeneration, build, unit tests and acceptance; cases labelled
`example`, `logic_example`, `auto`, `differential` or `hand_test`). Neither applies anything.

factory-pr/v1 (`docsp factory-pr`): `base`, `head`, `rules[{api, path, line, text, touched[],
category, facts[], quotes[], code[{path, line, text}], evidence: executed|read, error, draft
{edits[{path, old, new}], patch, rationale, summary, question, counterexample, verified,
problem}}]` - the rules with hand-written code a change touches (their code or their own
document), judged at the head; a draft is `verified` only when the factory accepts the design
with it; a problem's `evidence` is `executed` when the design's examples of its API fail at the
head, `read` when only the judge says so - and `acceptance{api: problem}` (the head as it is;
"" = passes), `failing{api: [case]}` (the design's cases failing at the head; a problem is `executed` only when its verified draft rewrites that very rule), `block` per rule, `diffs{path:
hunk}`, `github` (owner/name, for links), `feedback[{rule, option: design_stale|code_wrong|
not_a_problem, category, evidence}]` (the boxes ticked in the posted comment before
the run; `-feedback-only` prints just these as `{pr, comment, feedback}`), `usd`.

rulecover/v1 (`docsp factory-coverage`): `rules[{api, path, line, text, key, status:
covered|uncovered|undetermined|unmeasured|baseline_failing, mutant {path, old, new, rationale},
caught_by[] (design cases), caught_elsewhere[] (auto, differential, hand-written tests),
proposal {inserts[{path, anchor, text}], rationale, verified, equivalent, catches[], problem},
problem}]` and `summary {rules, measured, covered, uncovered, undetermined, verified_proposals,
coverage, reused}`: `coverage` = covered / (covered + uncovered), the rule executable coverage;
`uncovered` needs proof that the surviving mutant is observable (a verified example or a
factory-derived case catching it), else the rule is `undetermined` and not counted. `key` hashes the rule, its
design and its code: a later run reuses the outcome while it is unchanged.

## schedule/v1 and slo/v1

schedule/v1: one scheduler run - `counts {claims, stale, due, recheck, cycle_quota, picked,
shortfall}`, `estimated_usd`, `freshness`, `picked[]` and `shortfall[]` of `{id, path, line,
class, priority (0 stale, 1 due, 2 recheck, 3 cycle), reason, age_days, max_age_days,
churn_commits, churn_lines, required}`.

slo/v1: `accuracy`, `freshness`, `phantom_rate` as `{value, target, n, bad, met, low, high,
note}`; `error_budget {allowed, used, remaining, burned}`; `by_doc[]`; `actions[{path,
action: full_reaudit, max_age_days, why}]`; `cost {sample_usd, sample_billing,
scheduled_usd_estimate, scheduled_claims}`.

## outcome/v1 (`docsp outcome`)

An append-only JSON Lines stream, one event per line (private data, mode 0600): `{schema_version,
at, repo, claim_id, path, line, text, category, kind, how, label, source, ref, pr, option,
note}`. `kind`: `reported` (`how`: `new`, `code_changed`, `flip`), `resolved` (`code_changed`,
`flip`, `doc_removed`, `doc_edited`, `dropped`; `category` is the problem that was resolved),
`rejudged` (`stable`: judged again on unchanged inputs, the flip rate's denominator), `human`
(a PR tick: `pr`, `option`), `audit` (a random-audit label, `note`). `label`: `tp`, `fp`,
`skip`; `source`: `audit` (the only source of the precision SLI), `pr_tick`, `proxy` (a
reported line edited afterwards; both self-selected). A flip is a change between problem and
no problem while the line and every evidence blob are unchanged; evidence that blobs cannot
confirm (absence searches, bare file names, other repositories) counts as changed. A tick is
recorded once per pull request, line, option and category; an audit label once per claim and
label; a later label of the same subject replaces an earlier one in the report.

The report (`docsp outcome report -format json`): `target`, `max_flip_rate`, `events`, `total` and `repos[]` of
`{repo, reported, open, resolved {how: n}, flips, rejudged_unchanged, labels {source: {tp, fp,
skip}}, precision, flip_rate, error_budget}` - `precision` and `flip_rate` in slo/v1's SLI
shape, `error_budget` as slo/v1's (only with 10 or more audit labels).

## report/v1

`immediate[]` and `batched[]` findings `{id, repo, path, line, text, category, severity:
immediate|batched, target, verified_at, facts}`, `batch_due`, `next_batch`,
`held_for_next_batch`, `suppressed[]` (finding + reason, expires),
`expired_suppressions[]`, `recommendations[{repo, path, drafts_90d, advice}]`. The drafts
log (`docsp draft -log`) is a private JSON Lines file of `{claim_id, repo, path, line,
category, original, draft, action: replace|delete|link, diff_tokens, model, proposed_at,
proposed_ref, status: proposed|rejected_by_harness|accepted|superseded|reverted, status_at,
status_ref, problems}`.

## instance/v1

The operator's settings (YAML): `ai.judge` and `ai.draft` (provider, model, effort, timeout,
concurrency, `budget {max_requests, max_usd, max_tokens}`, provider-specific blocks, and
(additive) `agentic {max_steps, drift_max_steps, max_request_usd, drift_max_request_usd,
group}`),
`classification {default, repos, paths}`, `eval {judge, draft}` switching thresholds, and
`prices` overriding the built-in list prices used for estimates.

## eval/v1 (1.1)

One `docsp eval-model` run of one task: the frozen set (`path`, `sha256`, item count), task,
provider, model, prompt template hash, start time, `dry_run`, `scores` (judge: tp/fp/fn/tn,
precision, recall, unknown rate, quote-rejection rate, per split; draft: drafted,
supported rate, mean similarity; both: tokens, cost, USD per item, p50 latency, a note that
strong-AI-labelled positives measure agreement, not accuracy), the `switch` decision
(`qualifies`, failed thresholds) and per-item results. The eval set itself is JSON Lines of
items (`id, source, label, repo, split, revision, task, task_type, expected, claim, evidence,
reference_fix, reference_actual`); prompts are rebuilt from the current templates at run time,
so a template change is visible as a different `prompt_template_hash`.

1.1 (additive, agentic mode): `mode` (`snippet` / `agentic`), `group` (`line` / `doc`),
`drift[{repo, head, raw, validated, cached, parse, cost_usd, error}]`; per item `category`
(derived by the harness), `claimed` (the model's own statuses before validation), `parse`,
`evidence` / `evidence_ok`, `group_size`, `tool_steps`, `facts[{fact, status, valid,
evidence[{evidence, ok, reason}]}]` and `reason`; scores `categories`, `unverifiable_rate`
(`unverifiable_in_repo` is not counted in `unknown_rate`), `evidence_rejection_rate`,
`parse_failures`, `parse_fallbacks`. Eval items may have task type `claim_facts` (agentic
only: claim + revision, no snippets) and source `verified_control` (a line a reviewer
verified true at the set's revision).
