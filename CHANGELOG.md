# Changelog

## Unreleased

Direction (approved): keep docs consistent with code over time, not once - remove the docs'
"phantom inventory" with retail inventory control (record accuracy, cycle counting, ABC).
Design: docs/design.md §12; plan: docs/backlog.md.

### Changed
- `make lint` and `make vuln` build golangci-lint v2.15.0 and govulncheck v1.9.0 from
  source with `go run` (pinned, no binary download); the code base is gofumpt-formatted
  and passes the configured linters (behaviour unchanged).

### Added
- **Precision of docs-platform's own reports** (`docsp outcome`, outcome/v1, no model): an
  append-only stream of what happened to reported problems (reported, resolved and how,
  verdict flips on unchanged inputs, PR ticks, random-audit labels); `outcome report` gives
  the precision SLI from the random audit only (Wilson 95%, error budget once 10 are
  labelled) and the flip rate, with PR ticks and edited lines shown as biased supporting
  signals. The CI action's `main` task appends to it every run and keeps it with the ledger
  and as a 90-day artifact.
- **Contract layer** (`docsp contract`, contract/v1, docs/design.md §13): design-first specs
  vs the code that implements them, without a model. Adapters: `factory-design` /
  `factory-code` (a design-first generator's design language vs its escape hatches: SQL
  query files and compute functions), `openapi-graphql` (GraphQL embedded in OpenAPI paths)
  and `kotlin-graphql` (SmallRye code-first). Every mismatch gets a direction from git history
  (`design_behind_code`, `code_behind_design`, `diverged`, `undocumented`,
  `removed_in_code`) with the commits as evidence, an optional rename candidate, and an
  authority-dependent message; doc sets of a group take a `contract:` block (instance/v1,
  additive). `go run ./bench contract-lab` replays 28 injected changes on pinned public
  clones (22 drifts detected 22/22, direction 21/22, the 6 harmless edits quiet).
- **Reproducible from the repository alone** (REPRODUCE.md):
  - `bench/data`: the benchmark data about the three public repositories (147 ground-truth
    items, the agentic DEV and TEST eval sets with pinned revisions, the prototype's frozen
    answers and drift lists, the mutation set, the expected offline numbers), each file
    pinned by sha256 in `bench/data/manifest.json` with the repositories' full SHAs; the
    snippet eval set is regenerated from history instead of stored. `go test ./bench`
    checks the hashes and scans the data for private material (local paths, e-mail
    addresses, key and token patterns, SOPS ciphertext, and `DOCSP_DENYLIST` terms).
  - `bench fetch` (clones at the pinned SHAs, verified), `bench mutate` (the prototype's
    mutation-set generator ported to Go; byte-identical output and trees),
    `bench verify-offline` (compares 46 numbers with `bench/data/expected-offline.json`),
    `bench replay -manifest`.
  - `make repro-offline` (`scripts/repro-offline.sh`): unit tests, the frozen-answer replay
    (TEST recall 0.80, DEV 0.60), ledger replay (19/25, 23/25), history replay
    (TEST 32/61, 41/61), snippet eval set and mutation set; fails on any mismatch.
  - docs/experiments.md: the lab notebook of the model-layer experiments, with sources.
  - `.github/workflows/repro.yml`: test, repro-offline and lint (for when a remote exists).
- **Anthropic API provider in agentic mode**: `docsp-judge-anthropic` runs the tool-use loop
  over the Messages API with docsp's read-only judge-tools (each call goes back to docsp as a
  judge-tools/v1 `tool_call` and is executed by the core), structured output bound to the
  reply schema, per-turn worst-case checks against the run budget and the request's own cap
  (`max_request_usd` ends that request only), a step budget with a final no-tools turn,
  prompt caching, `-agentic-effort` (from `ai.judge.agentic.effort`; snippet effort is not
  inherited). Unit-tested against a fake HTTP server; not yet run against the live API.
- **Claim and evidence ledger** (B1; contract ledger/v1, `internal/ledger`): every judged
  line with the validated evidence its verdict rests on, addressed by git blob - quotes
  (file blob), drift entries (their files, the removed path), absence searches (pattern,
  glob, hash of the covered file set), path_missing targets. Stable claim ids (line moves keep
  the id). Mode 0600, merge by id (newer verification wins). Written by
  `docsp eval-model -mode agentic -ledger FILE` and `docsp audit -mode agentic -ledger FILE`.
- **`docsp stale -ledger FILE [-ref REV] [repo]`**: deterministic invalidation (no model):
  stale when the line was edited or the document removed, a cited file changed or vanished,
  a file in an absence scope was added/changed/removed, a missing path appeared, or a drift
  entry's removed path reappeared; claims without evidence → `recheck`. Contract stale/v1.
- **Footprint rules** (opt-in, off by default; `internal/ledger/footprint.go`): beyond the
  validated evidence, a claim can be re-opened (stale, never decided) by
  - `weak`: the path- or glob-scoped searches of its judgment (from the tool transcript; whole-
    repository searches do not count) - a file added, changed or removed inside one;
  - `siblings`: its document and quoted files with one directory level wildcarded - a newly
    added match (a statement about the lab goes stale when the laptop gets the same file);
  - `members`: for an enumerating claim, the directories it listed and globs it expanded - an
    entry added or removed.
  Recorded with `-footprint weak,members` on `eval-model` / `audit` (ledger/v1 `evidence.weak`,
  `evidence.members`), applied with `docsp stale -footprint weak,siblings,members|all`; reason
  kinds `weak_scope_changed`, `sibling_added`, `member_set_changed`. With no flag, recording
  and invalidation are unchanged (`make repro-offline`: all 46 numbers reproduced).
- `bench ledger-backtest` reports the ledger alone, with each footprint rule and with all,
  each with its per-commit stale cost, plus the impact selection and ledger OR impact on the
  same items (`-impact replay.json`); `-transcripts` / `-transcript-sets` add footprints to
  a ledger recorded without them. On the TEST backtest (20 scorable positives): ledger 8/12
  (first / any causing commit), +weak 12/16, +siblings 10/14, +members 8/12, all 14/18 at 1.14
  stale claims per commit (ledger 0.62); ledger OR impact 10/13. Still to be confirmed on DEV.
- `bench ledger-backtest`: scores invalidation without hindsight - claims judged at
  revisions before their causing commits, history replayed forward from each claim's own
  verified revision; reports first/any-causing catch rates, per-commit cost, and for every
  claim never staled its evidence and what the causing commits changed instead.
- `bench ledger-replay`: replays a ledger over real history (per-commit stale counts) and
  scores causing-commit recall of ground-truth positives.
- `ls-tree` joins the read-only git allowlist (tree snapshots).
- **Cycle counting and the consistency SLO** (B2; contracts schedule/v1 and slo/v1,
  `internal/consistency`, instance `consistency:` block):
  - `docsp schedule`: which claims to judge again this run - stale first, then due (older
    than the class's max age, churn in the evidence scope over a threshold, or a document
    whose error budget burned), no-evidence rechecks, then enough of the oldest that every
    claim is verified within its ABC class's max age; capped by `budget.max_claims` /
    `max_usd`, the cut reported as shortfall. ABC classes by document glob (importance) or
    90-day churn of the claim's footprint.
  - `docsp recheck -schedule FILE`: judges the picked claims (agentic, any configured
    provider), merges them into the ledger, retires claims whose line was edited.
  - `docsp sample`: the weekly accuracy sample (seeded, reproducible) as an eval set for an
    operator-run strong-model re-audit (`eval-model -mode agentic`).
  - `docsp slo`: SLI 1 accuracy (counting only the repository's own items of a sample that
    covers several) (from the sample's re-audit, with a 95% interval), SLI 2
    freshness, SLI 3 phantom rate (dangling references at medium confidence or better plus
    open dangling_reference claims); the accuracy error budget (initial objective 80%); a
    burned budget triggers a full re-audit of the document and a halved max age (`-apply`
    writes them to the `-state` file the scheduler reads); cost of the sample and the run.
- **Repository groups and declared revisions** (B3; instance `groups:`, `internal/group`,
  `docsp group plan|judge|stale`): repositories with roles (code, docs, api-spec,
  db-migration, infra); doc sets in one repository declaring the revisions they document
  (several allowed: design docs → develop, runbooks → prod); claims judged against a target
  carry it (`target`, `doc_ref` in ledger/v1; evidence addressed `repo@ref:path:line`) and are
  invalidated across repositories (`ledger.CheckGroup`: the document repository and the
  target repository are diffed separately); one drift scan per code `repo@commit`, shared by
  every doc set; generated documents (configured, or a generated marker in their first
  lines) are not judged on their text but at their generator input; submodules and the
  group's external globs never count as evidence. docsp never changes a checkout: a target
  needs a checkout at its revision (`-checkout repo@ref=DIR`), otherwise the plan says so.
- **No forced updates** (B4):
  - `docsp report` (report/v1): validated problems only, never an exit-code gate;
    contradicted / dangling_reference immediately, outdated_wording / incomplete_list in
    batches (`consistency.batch_days`, default 7; `-flush`); each finding once until its
    category changes (remembered in the state file); suppressions
    (`consistency.suppressions`) need a reason and an expiry date, expired ones are inactive
    and listed.
  - `docsp draft`: minimal-diff fix proposals from the draft provider, given only the facts
    validated as wrong and their evidence, told to prefer deleting or linking over restating
    volatile detail; the harness rejects drafts that change nothing, rewrite more than
    max(6, half the line's) tokens, or add a number/version the evidence does not show or
    more volatile tokens than the line had. Proposals are logged (JSON Lines, 0600), never
    applied.
  - `docsp drafts`: tracks the logged drafts from later revisions (accepted, superseded,
    reverted), reports acceptance rate, revert rate, mean diff size, and recommends
    generating or linking for documents with `recommend_after` (default 3) drafts in 90 days.
  - The continuous loop never asks "code changed, docs untouched"; `docsp impact`'s explicit
    rules remain opt-in per repository.

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
