# Changelog

## 0.4.2 - 2026-11-26

The 0.4.1 rerun validated every drift entry (22/22, 17/17) but recall fell to 19/30 with
3.0 tool steps per line (prototype 5.4): the model still stopped early. A byte-level
comparison of what each run sent the model found the per-line user prompts nearly equal
(the case block with ±3 lines identical) and the differences elsewhere.

### Fixed
- Agentic claude-code runs inherited `ai.judge.effort` (`medium` in the operator's
  configuration); the prototype passed no effort, i.e. the CLI's default. Agentic runs no
  longer inherit the snippet effort; `ai.judge.agentic.effort` sets one explicitly.
- The appended system prompt ended with "Reply with one JSON object ..., nothing else";
  the prototype appended nothing, and the reply schema enforces the format. Only the
  untrusted-data rule is appended now.
- The prompt drifted from the evaluated text in three places (working-directory wording,
  the absence sentence, the closing "do not explain away a mismatch" sentence), and 0.4.1
  added an untested hint to the tool list. The prototype's text is restored verbatim;
  the only addition remains the path_missing evidence kind.

### Added
- Repository context in the system prompt (repository, checked-out revision, branch, five
  recent commits): the prototype's CLI ran inside the checkout and told the model this
  itself. docsp states it instead; the CLI still never runs inside the inspected
  repository (its environment snapshot would bypass data classification).
- `ai.judge.agentic.effort`; prompt `docsp-agentic/3`; a test pinning the per-line request
  (prompt texts, context block, drift block, limits) and the agentic CLI argv.

## 0.4.1 - 2026-11-26

The first paid TEST run of 0.4.0 (Sonnet) scored recall 22/30 against the prototype's 24/30,
with 2.8 tool steps per line against 5.4. Cause: the tools showed the model something
different from what the prototype's model saw.

### Fixed
- `read_file` returned file lines as an unnumbered JSON array, so the model had to count
  lines: its quotes landed on the wrong line and were rejected (every one of the 5 rejected
  drift entries of the run had the right text at a line 4-29 off, among them the two
  watcher entries two lost positives depended on). Tool results now reach the model as
  plain text the way its own file tools show them: `12: text` lines, `path:line: text`
  matches, one entry per line, "[more: read from_line N]" and a truncation note.
- Results were JSON with HTML escaping, so `<name>`, `&&` and `->` arrived as `\u003c...`:
  no quote of such a line could match. Results are encoded without escaping.
- Lines were cut at 400 characters (now 2000, as a model's own reader).

### Added
- judge-tools/v1 **1.2**: `tool_result.text`, the model-ready rendering (MCP and Ollama
  show it; exec providers should); tools `outline` and `config_value` (ported from the
  prototype's helpers).
- Agentic prompt `docsp-agentic/2` (tool list with the new tools; drift caches of 0.4.0 are
  not reused).
- eval/v1: per item `transcript` (tool, arguments, result size, error - never contents),
  per drift scan `tool_steps` and `transcript`; `eval-model` prints the mean tool steps.

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
