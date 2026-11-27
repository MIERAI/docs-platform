# docs-platform

Keep documentation true to the code, the SRE way: **detect drift → findings → ledger → SLOs →
remediation loop**.

Documentation rots silently. A flag is renamed, an environment variable is dropped, a script
moves, and the README keeps describing the old world until a newcomer trips over it.
docs-platform treats that drift like any other reliability problem: a deterministic detector
emits findings in a shared, versioned contract; a ledger tracks each finding from first seen to
fixed; SLOs ("drift fixed within N days", "share of doc-impacting PRs that touch docs") make the
state visible; AI may *propose* fixes, humans decide.

`docsp` is the command-line tool. The direction is **detect drift at change time** (the moment a
change makes a document stale), measured by **replaying history**; models are assistants behind
a swappable contract. Implemented so far:

| Layer | What | Status |
|---|---|---|
| ① generated reference docs + CI drift check | docs rendered from code, CI fails when stale | design |
| ② executable docs | shell blocks run in CI | design |
| ③ reference check | every identifier a document mentions exists in code; every public symbol is documented | **implemented** |
| ④a change-time impact | the doc sections a change affects, derived from layer ③'s reference graph (no hand-written map); explicit rules and reasoned waivers still decide the gate | **implemented** (`docsp impact`) |
| ④c semantic audit | claims in the docs (values, schedules, versions, lists, state words, file roles) checked against the code: deterministic rules first, then an optional judge model - snippet mode, or agentic mode (repository drift scan + fact-by-fact checking with re-validated evidence) | **implemented** (`docsp audit`, `-mode agentic`) |
| ④b model judgement + draft | a configured model judges "does this change contradict this section?" and drafts an edit (proposal only) | **implemented** (behind `judge/v1`) |
| model evaluation | per-task scores of any provider on a frozen eval set; config-driven switching rule | **implemented** (`docsp eval-model`) |
| backlog import | a reviewed defect backlog becomes findings the ledger tracks | **implemented** (`docsp import-findings`) |
| contract layer | design-first specs vs the code that implements them: interface diff (operations, inputs, outputs, types, declared rules) and, from git history, which side moved; no model | **implemented** (`docsp contract`) |

The design (in Chinese) is [docs/design.md](docs/design.md); the data contracts are described in
[docs/contracts.md](docs/contracts.md).

## Quick start

```sh
go build -o bin/docsp ./cmd/docsp
bin/docsp check -format text /path/to/repo          # human summary
bin/docsp check -out findings.json /path/to/repo    # findings/v1 JSON (mode 0600)
bin/docsp impact -base origin/main /path/to/repo    # sections to re-read + rule gate
bin/docsp audit -format text /path/to/repo          # semantic audit (deterministic checks only)
```

`docsp` never writes into the repository it inspects, never runs code from it, and only calls
the read-only git sub-commands `diff`, `log`, `rev-parse`, `ls-files`, `show`, `blame` and
`merge-base`.

## Commands

### `docsp check [flags] [repo]`

Extracts symbols from code and references from documentation, matches them, and prints a
findings/v1 document. Two kinds of drift are reported:

- **wrong or obsolete** (`docs.reference.unresolved.<kind>`): the docs mention something the code
  does not define - a flag, command, environment variable, metric, make target, file path, link
  or anchor. A near miss comes with a suggestion ("did you mean `--color`?").
- **undocumented** (`docs.symbol.undocumented.<kind>`): a public flag, command, environment
  variable, metric, workflow input or configuration key that no document mentions.

It also reports references that leave the repository (`docs.reference.outside_repo`, for example
a path under a home directory) and, when a denylist is configured, terms that must never appear
(`docs.residue.term`).

Every finding carries a confidence (`high`, `medium`, `low`) that drives its severity, so CI can
gate on the reliable part and leave the rest for triage.

| Flag | Meaning |
|---|---|
| `--config FILE` | configuration file (default `<repo>/.docsp.yaml`) |
| `--repo-name NAME` | logical repository name used in finding assets (default: directory name) |
| `--instance NAME` | instance name recorded in `run.instance` |
| `--out FILE` | write the findings JSON to FILE (mode 0600) instead of stdout |
| `--format json\|text\|none` | what to print on stdout |
| `--min-confidence low\|medium\|high` | report findings at or above this confidence (default from config: `medium`) |
| `--fail-on info\|low\|medium\|high\|none` | exit 1 when a finding has this severity or higher (default `none`) |
| `--denylist FILE` | private file (mode 0600) of terms that must not appear; also `DOCSP_DENYLIST` |

### `docsp impact --base REF [--head REF] [flags] [repo]`

Layer ④a, run on every change (CI). Two parts:

1. **Derived sections (advisory).** Layer ③ already knows, for every document section (a
   Markdown heading section or a block of code comments), which paths and identifiers it
   mentions. From `git diff base...head` docsp lists every section that mentions a changed file
   or directory, or an identifier (env var, flag, command, config key, metric, make target,
   identifier-shaped term) that appears on a changed line - with the reason ("`APP_PORT`
   changed in `cmd/tool.go:5`"). Status `reread` means the section did not change and should be
   re-read; `updated` means the change already edits it. No map needs to be written.
2. **Explicit rules (the gate).** `.docsp.yaml` may still map code paths to documents; a change
   touching mapped code but none of its documents fails - unless a commit message or the PR
   description carries `Docs-Not-Needed: <reason>` (the key is configurable; a trailer without
   a reason is not a waiver). Rule-matched documents are added to the section list (reason
   `rule`).

When the instance configuration names a judge, each `reread` section (up to `--max-judge`) is
put to it - "does this change make a statement of the section wrong?" - with the changed lines
as evidence; a verified yes is passed to the draft provider for a proposed edit. Both are
proposals in the report, never applied. The result is an impact/v1 document.

| Flag | Meaning |
|---|---|
| `--base REF` | base revision of the change (required) |
| `--head REF` | head revision (default `HEAD`) |
| `--config FILE` | configuration file |
| `--pr-body-file FILE` | the PR description, searched for the waiver trailer |
| `--repo-name NAME` | logical repository name (data classification is per repository) |
| `--instance-config FILE` | instance configuration (judge/draft providers); none = deterministic only |
| `--max-judge N` | sections put to the judge (default 20) |
| `--out FILE` | write the impact JSON to FILE (mode 0600) |

### `docsp audit [flags] [repo]`

Layer ④c, meant to run periodically (not as a CI gate). It extracts **claims** from Markdown and
code comments - sentences carrying a number with a unit (`3 days`, `512Mi`, `2%`), a schedule
(`daily at 09:00 UTC`, `every 2 minutes`, a cron expression), a version, an enumeration of
names, a state word ("planned", "not yet", "in progress", "TODO", a `Status:` line) or a file
role ("humans write `docs/`") - and checks them:

| Sub-layer | Rule (`docs.semantic.<rule>`) | Evidence |
|---|---|---|
| value | `value_mismatch` | the configuration key named next to the value (YAML, HCL, Go constants and flag defaults, shell/Make defaults), unit-normalized |
| value | `schedule_mismatch`, `schedule_disabled` | the cron of the workflow / CronJob the sentence names (time zones converted; a commented-out cron is a disabled schedule) |
| value | `version_unreleased`, `version_mismatch` | git tags reachable from HEAD; image tags, chart versions |
| value | `list_incomplete` | the code set the list enumerates: commands of a program, make targets, children of a directory, sibling keys, values of a key, generated paths |
| state | `state_contradiction`, `status_stale`, `role_contradiction` | git: the thing called "not yet" was added after the sentence; a status line older than the code it describes; "written by people" vs generated-file markers |
| staleness | `stale_claim` | git: the code a sentence names changed in N commits after the sentence last changed (weak signal, low) |
| model | `model_contradiction` | the configured judge answers "does this evidence contradict the claim?" |
| model | `classification_refused` | a claim was NOT sent: its content is private and the judge is a cloud provider |

The judge is optional and comes from the instance configuration (see *Models*). It gets the
claim and at most three small code snippets retrieved deterministically, must answer with a
quote copied from the snippets (an answer whose quote is not found verbatim is discarded), and
is asked up to three times with the snippets in different orders (majority wins; anything else
is "unknown" and not reported).

Every audit finding is a proposal: `needs_human: true`, severity `low` (medium confidence) or
`info`, collector `docs-audit`, `attributes.docs.detector_layer: semantic` with the rule, the
claim, the quote and, for the judge, the model and the prompt hash.

**Agentic mode** (`-mode agentic`) replaces the snippet questions with a model that explores
the repository through docsp's read-only tools (see *Agentic judge* below). Problems it can
back with validated evidence become `docs.semantic.agentic_contradicted`,
`agentic_dangling_reference`, `agentic_incomplete_list` or `agentic_outdated_wording`
(sub-layer `model`, confidence medium); lines it finds `unverifiable_in_repo` are counted,
not reported.

| Flag | Meaning |
|---|---|
| `--config FILE` | configuration file |
| `--instance-config FILE` | instance configuration (judge provider, model, budget, classification) |
| `--no-model` | deterministic checks only, whatever the instance configuration says |
| `--max-questions N` | claims put to the judge per run (default 150) |
| `--mode snippet\|agentic` | judge mode (default snippet) |
| `--group line\|doc` | agentic: one request per line (default) or per document, up to six lines (cheaper) |
| `--drift-cache DIR`, `--no-drift-cache`, `--no-drift` | agentic: where drift lists are cached by HEAD SHA (default: the user cache directory), no cache, or no drift scan |
| `--out`, `--format`, `--repo-name`, `--instance` | as for `check` |

### `docsp eval-model --set FILE [flags]`

Scores a provider, **per task**, on a frozen eval set (built by `bench replay --eval-out`;
its sha256 is recorded in every result): judge → precision, recall, unknown rate, quote-rejection
rate; draft → share of drafts with no invented identifier/path/number (every one must appear in
the claim or the evidence) and word overlap with the reference fix (a cheap acceptance proxy);
both → tokens, cost, p50 latency. `--dry-run` calls no model and estimates calls, tokens and
cost. Results are eval/v1 JSON files (`--out-dir`) for comparison over time. The switching rule:
a candidate replaces the current provider of a task when it meets every threshold of that task
in the instance configuration (`eval.judge`, `eval.draft`) on the frozen set; exit code 1 when
it does not.

Positives in the set come from a ground truth that a strong AI produced: their scores measure
**agreement with a strong-AI audit, not accuracy** (strong models score inflated). Negatives
are the same claims against the code at the commit that wrote them (assumed consistent then).

| Flag | Meaning |
|---|---|
| `--set FILE` | the frozen eval set (JSON Lines) |
| `--instance-config FILE` | providers, budgets, classification, thresholds |
| `--task judge\|draft\|both` | which task (default judge) |
| `--model ID`, `--provider KIND` | evaluate a candidate without editing the live instance configuration |
| `--split dev\|test`, `--limit N`, `--concurrency N` | subset and parallelism |
| `--dry-run` | estimate only |
| `--out-dir DIR` | write eval/v1 results here |
| `--mode agentic`, `--worktree NAME=DIR` | evaluate the agentic judge: one drift scan per repository, then each item's line against a checkout of its repository (repeatable; it must be at the items' revision) |
| `--group`, `--drift-cache`, `--no-drift-cache`, `--no-drift` | as for `audit` |

In agentic mode the scores add the derived categories, `unverifiable_rate` (not counted as
unknown), `evidence_rejection_rate`, `parse_failures` and `parse_fallbacks`, and each item
carries its facts with the check of every piece of evidence.

### `docsp stale -ledger FILE [-ref REV] [-repo-name NAME] [repo]`

Checks the claim ledger (contract ledger/v1, written by `audit` / `eval-model` in agentic
mode with `-ledger FILE`) against a revision, without any model. A claim is `stale` - judge
it again - when its line was edited or its document removed, a file it quotes changed or
vanished, a file inside one of its absence searches was added, changed or removed, a path it
calls missing appeared, or a drift entry's removed path reappeared. A claim with no evidence
(unknown, unverifiable) is `recheck`: only the periodic recheck can renew it. Everything else
is `fresh`. Output: stale/v1 JSON (`-format json`, `-out FILE`, mode 0600) or a text summary.
`-footprint weak,siblings,members` (or `all`) also re-opens claims by footprint rules - the
scoped searches of the judgment (recorded with `-footprint weak` on `eval-model` / `audit`), a
newly added sibling of the document or a quoted file (one directory level wildcarded), an
entry added to or removed from a set an enumerating claim listed (recorded with `-footprint
members`). Footprints only re-open a claim; they never decide a verdict. Off by default.
Pinned files (`docsp factory-rules`: the hand-written code a generator's manifest says a rule
governs) are a footprint that is always on and survives re-judging (`pinned_changed`).
The ledger is private data (documentation text and paths): mode 0600, never inside an
inspected repository.

### Continuous consistency: `docsp schedule`, `recheck`, `sample`, `slo`

Cycle counting for documentation (docs/design.md §12), driven by the ledger and the
instance configuration's `consistency:` block:

```yaml
consistency:
  max_age_days: 30          # every claim is verified again within this many days
  churn_commits: 10         # ... or sooner when its evidence scope churns this much
  churn_lines: 300
  classes:                  # ABC: first match wins; others are C with max_age_days
    - {name: A, max_age_days: 7, docs: ["docs/runbooks/**", "README.md"]}
    - {name: B, max_age_days: 14, min_commits_90d: 20}
  run_interval_days: 1
  budget: {max_claims: 50, max_usd: 2, usd_per_claim: 0.04}
  slo: {accuracy: 0.8, freshness: 0.95, phantom_rate: 0.01}
  sample_size: 30
```

- `docsp schedule -ledger L -instance-config I [-state S] [repo]`: the claims to judge again
  this run (schedule/v1) - stale, due, rechecks, then the cycle quota - within the budget; the
  rest is reported as shortfall.
- `docsp recheck -schedule FILE -ledger L -instance-config I [repo]`: judges them (agentic) and
  updates the ledger.
- `docsp sample -ledger L -out set.jsonl [-n N] [-seed S] [repo]`: the accuracy sample, an eval
  set to re-audit with `docsp eval-model -mode agentic` (a paid run the operator starts).
- `docsp slo -ledger L -instance-config I [-sample-result eval.json] [-state S -apply] [repo]`:
  accuracy, freshness and phantom rate against their objectives, the error budget, the
  actions a burned budget triggers (full re-audit, halved max age), and the cost (slo/v1).

### No forced updates: `docsp report`, `draft`, `drafts`

- `docsp report -ledger L -instance-config I -state S [-flush] [-drafts LOG] [repo]` (report/v1):
  the ledger's validated problems for people; exit code 0 whatever it finds.
  `contradicted` / `dangling_reference` are reported at once, `outdated_wording` /
  `incomplete_list` in batches every `consistency.batch_days` (default 7); a finding is
  reported once until its category changes. Suppressions live in the instance configuration
  and need a reason and an expiry:
  `consistency.suppressions: [{path: "legacy/**", reason: "being rewritten", expires: 2027-01-31}]`
  (or `claim: <id>`); expired ones stop silencing and are listed.
- `docsp draft -ledger L -instance-config I -log LOG [-max N] [repo]`: asks the draft provider for the
  smallest correction of each problem line, given only the facts validated as wrong; the harness
  rejects drafts that change nothing, rewrite more than the wrong facts, or add volatile detail
  (numbers, versions) the evidence does not show; deleting the line (`DELETE`) or linking to the
  source is preferred. Logged, never applied.
- `docsp drafts -log LOG [-recommend-after N] [repo]`: what became of the drafts at the current revision (accepted,
  superseded, reverted): acceptance rate, revert rate, mean diff size, and documents to generate
  or link instead.

The continuous loop never gates on "code changed, docs untouched"; `docsp impact`'s explicit
rules stay opt-in.

### Repository groups: `docsp group plan|judge|stale`

```yaml
groups:
  - name: shop
    repos:
      - {name: api, role: code}
      - {name: docs, role: docs, path: /srv/checkouts/docs}
    doc_sets:
      - {name: design, repo: docs, docs: ["design/**"], targets: [{repo: api, ref: develop}]}
      - name: runbooks
        repo: docs
        docs: ["runbooks/**"]
        targets: [{repo: api, ref: prod}]
        generated: [{docs: "runbooks/cli.md", input: "cmd/gen-cli/main.go"}]
    external: ["vendor/", "third_party/"]
```

- `docsp group plan -instance-config I [-group NAME] -checkout repo[@ref]=DIR ...`: each doc
  set's documents (generated ones listed with their input), its targets resolved to commits
  and checkouts, the drift scans needed (one per code `repo@commit`, shared), and problems
  (a target without a checkout at its revision: docsp never changes a checkout).
- `docsp group judge ... -doc-set NAME -line PATH:LINE ... -ledger L`: judges the lines
  against every declared target (agentic; the tools explore the target, the statement comes
  from the docs repository) and records claims with their target.
- `docsp group stale ... -ledger L`: invalidates the group's claims across repositories.

### `docsp import-findings --repo-name NAME [--repo owner/name] FILE.jsonl`

Turns a reviewed backlog (the ground-truth JSON Lines format: `repo, file, line, claim, actual,
type, fix, evidence_paths`) into findings/v1 (collector `docs-backlog`, check
`docs.backlog.<type>`, `detector_layer: review`) so the shared ledger tracks it. Ids are stable
(re-importing is idempotent); the document has empty coverage, so an import never marks
anything fixed. `--owner` sets the finding owner, `--out FILE` writes the JSON (mode 0600).

### `docsp contract [flags]`

The contract layer, for design-first teams: the spec is written first, the code follows, and
later the code changes while the spec does not. `docsp contract` reduces both sides to a
normalized interface surface - operations, inputs (type, presence, the column a filter
compares), outputs (type, source column), declared machine-readable rules (table, filters,
fixed conditions, ordering, row limit, function signature, columns a function reads) -
diffs them, and classifies every mismatch from git history:

| Class | Meaning | Evidence |
|---|---|---|
| `design_behind_code` | the code changed it after the design last did | last commit on each side naming the item (`git log -G`) |
| `code_behind_design` | the design changed it later, or the code never implemented it | same; `git log -S` finds no code commit for "never" |
| `diverged` | both sides changed it since they last agreed | the two newest touches on each side |
| `undocumented` | only in the code; the design never named it | `git log -S` on the design paths is empty |
| `removed_in_code` | the design still lists what the code deleted | the deletion and the addition commits (`git log -S`) |

History is searched in the files that define the operation on each side, for the item's
name in its snake_case, lowerCamel and UpperCamel spellings. A fuzzy name match on the other
side is attached as `rename_candidate` - a hint, never a fact. No model is called.

Adapters (`-design-adapter`, `-code-adapter`):

| Adapter | Side | Reads |
|---|---|---|
| `factory-design` | design | a design-first generator's design language (`design/<domain>/<API>.yaml` with the domain file and the schema SQL; hand-written `specs/**/spec.yaml`, including the escape hatch `datasource.sql`) |
| `factory-code` | code | the files the generator cannot keep in step: SQL query files (`db/queries/*.sql`, simple parsing: parameters and casts, SELECT list through derived tables, tables, WHERE comparisons, ORDER BY, LIMIT) and compute functions (`internal/handler/**/*_logic.go`, go/parser: signature, response fields set, row and request fields read) |
| `openapi-graphql` | design | OpenAPI documents whose paths embed GraphQL: every example `query` and every fenced graphql block; arguments typed from the variable definitions and `components.schemas` |
| `kotlin-graphql` | code | GraphQL code-first Kotlin (SmallRye): `@Query` / `@Mutation` functions of `@GraphQLApi` classes (`@Name`, default values, the `get` prefix rule), argument types and the fields of the return type's class |

**With a generator's manifest** (`-factory-manifest FILE`, the JSON a design-first API factory
writes with `factory export`, schema `factory.api-manifest/v1`): the APIs the manifest lists
without hand-written code are left out of the diff and counted as `generator_guaranteed` - the
factory's own gates (deterministic generation, an up-to-date check, acceptance built from the
design's examples) keep them in step, and re-parsing generated code breaks whenever the
generator changes its output. Only the escape layer (compute logic, custom handlers,
`datasource.sql`) is compared. The manifest also maps every business rule of an API with
hand-written code to that code: the prose rules are where a design goes stale, and they are
judged as claims, not diffed (`docsp factory-rules`, below). On a real factory repository
(65 operations) the manifest took the report from 75 items (almost all generator-output
parsing gaps) to 11 informational ones.

The doc set's **authority** decides the wording: `design` reports "code deviates from design",
`code` reports "design needs update" and recommends generating the design from the code when
a generator exists (`-generator`, for example a GraphQL schema export), `ask` stays neutral.

Each side is read at a revision with `git ls-tree` / `git show` (checkouts are never touched)
or, without `-design-ref` / `-code-ref`, from the working tree, where an uncommitted change is
the newest touch. The output is contract/v1 (`-out FILE`, mode 0600; `-format text|json|none`).

| Flag | Meaning |
|---|---|
| `-instance-config FILE` | take everything from a group doc set's `contract:` block (below); flags given override it |
| `-group NAME`, `-doc-set NAME` | which group / doc set (default: the only doc set with a contract) |
| `-checkout repo=DIR` | a clone of a group repository; it need not be at the revision |
| `-design DIR`, `-design-ref REV`, `-design-path GLOB`, `-design-adapter NAME`, `-design-repo NAME` | the design side (path flags repeat) |
| `-code DIR`, `-code-ref REV`, `-code-path GLOB`, `-code-adapter NAME`, `-code-repo NAME` | the code side |
| `-authority design\|code\|ask`, `-generator TEXT` | as above |
| `-no-history`, `-workers N` | skip the history classification; concurrent git calls |
| `-factory-manifest FILE` | an API factory's manifest: compare only the APIs with hand-written code |

In a repository group the spec and the code may live in different repositories; the doc set
declares the adapters and the revisions:

```yaml
groups:
  - name: shop
    repos:
      - {name: spec, role: api-spec, path: /srv/spec}
      - {name: api, role: code, path: /srv/api}
    doc_sets:
      - name: graphql
        repo: spec
        docs: ["api/graphql/**"]
        targets: [{repo: api, ref: origin/develop}]
        contract:
          design_adapter: openapi-graphql
          code_adapter: kotlin-graphql
          design_ref: origin/main
          code_paths: ["services/**"]
          authority: code
          generator: "export the GraphQL schema from the server"
```

`go run ./bench contract-lab -lab bench/contractlab/lab.json -work DIR` replays the lab of design/§13 (drifts injected into
pinned public clones, expected detections in `bench/contractlab/lab.json`).

### `docsp factory-rules -factory-manifest FILE -ledger FILE [flags] [repo]`

Puts the business rules of an API factory's escape layer into the ledger. For every API the
manifest lists with hand-written code (compute logic, a custom handler), each rule line of its
design or spec is judged against the repository like any documentation line, with the
hand-written files given to the judge as the place to start (a hint, not evidence). The
resulting claim pins those files (`evidence.pinned`, their blobs at the judged commit): when
one of them changes, `docsp stale` re-opens the rule and the per-commit `recheck` judges it
again; the pins are kept. Tests are not pinned (they are not the implementation), nor is the
rule's own file (SQL inside a spec: an edit there re-opens the line anyway). Rules of APIs the
generator guarantees are left out.

Rules already in the ledger with the same pinned files are not judged again, so the command
can run on every commit and only pays for new or edited rules and a changed set of
hand-written files. The manifest must be exported from the commit being judged: a rule line
that is missing or does not contain the rule's condition is an error, not a guess. `-dry-run`
lists what would be judged without a model; `-out FILE` writes the plan and result as JSON.

### `docsp factory-pr -base REV ...` and `docsp factory-coverage ...`: the factory proves the proposals

Both work on a checkout of an API factory's project with its manifest, a judge
(`-instance-config`) and the factory itself (`-factory`, default `go run ./factory/cmd/factory`),
whose `factory try` applies file changes to a temporary copy of the revision, regenerates,
builds and runs its tests and acceptance. The model only proposes; the factory's run decides.

**`docsp factory-pr`** reviews a change from the design's side. The rules with hand-written code
it touches - a change to their code or to their own design - are judged at the head (the
agentic judge, told where the code is). For a rule the code now contradicts, the drafter
proposes the smallest design edit (the rule's wording and any example encoding the old
behaviour); it is marked verified only when the factory accepts the design with it at the
head. The comment says what was found and leaves the decision - stale design or wrong code -
to a person. Shown: per problem one sentence on what differs and the one yes/no question that
decides it, answered by ticking a box; folded under it is what deciding may take: one sentence on what differs, the
one yes/no question that decides it, the design and the code side by side (permalinks at the
head), a counterexample - the design's examples the factory actually ran and saw fail
(🔬 executed), or else the drafter's worked input (📖 read) - the change that caused it and
the draft as a patch for `git apply`. Each problem ends in checkboxes (design is stale / code
is wrong, and for 📖 only: not a problem); a 🔬 problem cannot be ticked away as a
false positive. Ticks are read back on the next run (`-feedback-only` reads them without a
review) and kept when the verdict is unchanged: they measure the check's false-positive rate,
next to a neutral weekly sample, and are not used to rate people. `-comment-out FILE` writes
the comment, `-post-pr N -github-repo owner/name` posts it with `gh` (one comment, updated in
place only when the findings change - a rewrite while someone has the page open makes their
tick fail), `-from-report FILE` re-renders a saved report without a review, `-no-try` skips
the factory.

**`docsp factory-coverage`** measures the **rule executable coverage**: for each rule with
hand-written code the model proposes a mutant (the smallest code edit that breaks that rule
alone) and the factory runs the project with it. The rule is covered when a case written in the
design (an example or a logic example) fails; a hand-written test or a factory-derived case
catching it does not count - they guard the code, not the design. For an uncovered rule the
model drafts a new example (`-propose`, on by default), kept only when the factory shows it
passes on the current code and fails on the mutant. A survivor counts as `uncovered` only when its
breaking is proven observable - a verified drafted example, or a factory-derived case (auto,
differential), catches it; otherwise it is `undetermined` (possibly an equivalent mutant, or one
only reachable by inputs the API never passes) and stays out of the coverage. When the drafter
says a mutant cannot be observed through the API, a different mutant is asked for (up to three).
An API whose cases fail without any change is `baseline_failing`, not measured. One mutant per
rule is a sample, not a proof: another run may pick a different mutant (reusing outcomes keeps
the number stable while nothing changes). On api-factory (24 rules with hand-written code) a
full run took 9 minutes and $1.00: 11 of 18 measurable rules covered (61%), 7 verified example
proposals, 6 undetermined. Outcomes are reused while the rule, its design and its code
are unchanged (`-previous`, default the `-out` file), so a rerun only pays for what changed;
`-api ID` (repeatable) limits a run to some APIs, `-workers N` runs rules in parallel.

### `docsp extract [--config FILE] [repo]`, `docsp validate FILE`, `docsp version`

`extract` prints what the extractors found (contract extract/v1) - useful when tuning a
configuration or writing an extractor. `validate` checks a findings document against the
contract.

## Configuration: `.docsp.yaml`

All fields are optional; unknown fields are errors. Schema: `contracts/config-v1.schema.json`.

```yaml
version: 1
docs: ["*.md"]                    # which files are documentation
exclude: ["testdata/", "CHANGELOG*.md"]
public_config: ["chart/values.yaml", "*.example.yaml"]   # files whose keys are user-facing
public_depth: 1                   # deepest public key level (0 = top level)
min_confidence: medium
owner: docs-team                  # finding owner
related_repos: [other-repo]       # "other-repo scripts/x.sh" is that repo's path, not ours
ignore:                           # every rule needs a reason
  - kind: env_var
    text: "DEBUG_*"
    reason: developer-only switches
impact:
  trailer: Docs-Not-Needed
  rules:
    - name: cli
      code: ["cmd/**"]
      docs: ["README.md", "docs/cli.md"]
reverse:
  kinds: [cli_flag, env_var, metric]   # which public symbols must be documented
audit:                            # docsp audit (layer ④c)
  stale_commits: 2                # later commits to a claim's code that make it stale
extractors:
  disable: [terraform]
  external:
    - name: rust
      command: ["/usr/local/bin/docsp-extract-rust"]
```

A single line can be exempted in a document with `<!-- docsp:ignore <reason> -->` on the line
before it, a region with `<!-- docsp:ignore-start <reason> -->` … `<!-- docsp:ignore-end -->`.

A repository can never choose a model: `audit.ai` is rejected here. Models belong to the
instance configuration.

## Models (instance configuration)

Which model answers which task is decided only by the **instance configuration**
(`contracts/instance-v1.schema.json`, YAML, kept outside every repository). Each task is
configured, budgeted and evaluated on its own: **judge** ("does this evidence / change
contradict this section?") and **draft** (propose the edit; only after a verified yes; a human
reviews it). Swapping a model is a configuration change.

```yaml
version: 1
ai:
  judge:
    provider: claude-code          # none | ollama | claude-code | anthropic | exec
    model: claude-opus-5-5
    effort: medium                 # low | medium | high | xhigh | max
    timeout_seconds: 180
    max_concurrency: 2
    budget: {max_requests: 300}    # hard per-run stop; also max_usd, max_tokens
  draft:
    provider: anthropic
    model: claude-opus-5-5
    anthropic:
      program: /opt/docsp/bin/docsp-judge-anthropic
      api_key_file: /home/me/.config/docsp/anthropic.key   # must be mode 0600
      # api_key_env: ANTHROPIC_API_KEY                     # or an environment variable
      fallbacks: default
    budget: {max_usd: 5}
classification:                    # what may leave the machine
  default: private
  repos: {my-public-repo: public}
  paths: [{repo: my-public-repo, glob: "internal-notes/**", class: private}]
eval:                              # switching thresholds per task
  judge: {min_precision: 0.8, min_recall: 0.5, max_unknown_rate: 0.3, max_quote_rejection_rate: 0.1}
  draft: {min_supported_rate: 0.9, min_similarity: 0.2}
```

| Provider | Where it runs | Notes |
|---|---|---|
| `none` | - | deterministic checks only (default) |
| `ollama` | local server | dialer refuses non-local addresses; temperature 0, fixed seed |
| `claude-code` | the Claude Code CLI (`claude -p`) with the operator's own login (locally) or the token variable named in `claude_code.pass_env` (CI) | argv only, no shell; no tools, no MCP, no settings, no session on disk, empty working directory; JSON output validated against the reply schema; docsp never reads the credential |
| `anthropic` | `docsp-judge-anthropic` (own module, official Go SDK) | key only from a 0600 file or a named env var, never logged; HTTP only to `https://api.anthropic.com`; structured output; explicit effort; per-run USD budget; snippet and agentic mode |
| `exec` | any program speaking judge/v1 as JSON Lines on stdin/stdout | treated as cloud unless `exec.local: true`; declares its `modes` |

Agentic mode is supported by `claude-code` (its only tools are docsp's read-only tools, served
over MCP by a `docsp judge-tools-mcp` child process; no built-in tools; docsp's system prompt
is appended to the CLI's own agent prompt), `ollama` (native tool calls, then the answer with
the reply schema as `format`; give it a context of 32k tokens or more with `ollama.num_ctx`)
`anthropic` (the adapter runs the tool-use loop over the Messages API; every tool call is sent
back to docsp as a judge-tools/v1 `tool_call` line and executed by the core, worktree-confined and
classification-aware; the answer is structured output) and `exec` (`modes: [agentic]`).
`docsp-judge-replay -answers FILE [-probe]`
(`make build-replay`) is an offline exec provider that answers from canned answers (JSON
Lines `{"key": "drift:<repo>" | "line:<repo>:<path>:<line>", "answer": {...}}`; `-probe` makes
one tool call per request) - for tests and for re-scoring a frozen run at no cost.

`docsp-judge-anthropic` is started by docsp with argv built from the instance configuration:
`--model ID`, `--effort LEVEL` (snippet requests), `--agentic-effort LEVEL` (only when
`ai.judge.agentic.effort` is set; agentic requests otherwise use the model's default), `--max-retries N` (SDK retries on 408/409/429/5xx, honouring
`retry-after`), `--fallbacks default|none` (server-side refusal fallback; the answering model is
recorded), and exactly one of `--api-key-file FILE` (refused unless mode 0600) or
`--api-key-env NAME`.

Every provider sits behind the same guard: data classification (a cloud provider never receives
`private` content - the request is refused and reported), the per-task budget (requests,
tokens, USD; the worst case of the next request is checked before it is sent), contract
validation of every response, and verbatim-quote verification. Current strong models no longer
accept sampling parameters (temperature), so reproducibility comes from fixed prompts, schema-bound
answers and quote verification instead.

### Agentic judge

```yaml
ai:
  judge:
    provider: claude-code
    model: claude-sonnet-5-5
    agentic:
      max_steps: 16              # tool steps per line (default)
      drift_max_steps: 45        # tool steps of the drift scan (default)
      max_request_usd: 0.6       # cap per line request; reaching it ends that request only
      drift_max_request_usd: 2.5
      group: line                # or doc
      # effort: high             # unset = the model's default (as evaluated); ai.judge.effort is snippet-only
```

1. **Drift scan**, once per repository and HEAD: the model reads the history (`git_log`,
   `git_show`, `git_changes`) and lists the changes that make older documentation wrong. The
   harness keeps an entry only if its commits are ancestors of HEAD, its quotes are verbatim
   at path:line±2, and a path it calls deleted/renamed/moved is really gone. Validated entries
   (D1, D2, ...) are shared with every judgment; the list is cached by HEAD SHA, prompt and
   model (validation is re-run on every load).
2. **Judgment**: the model splits the line into facts, each with a status (`contradicted`,
   `incomplete_list`, `outdated_wording`, `dangling_reference`, `unverifiable_in_repo`,
   `confirmed`) and evidence. Other documentation is not evidence; the same name on another
   host, under another name, moved, or in a later state is a mismatch.
3. **Evidence** is re-checked by the harness, never trusted: `quote` (verbatim at path:line±2,
   not the line under test), `absence` (the exact `search` re-run over every tracked file in
   scope, private ones included; accepted only without a match, and only if the scope selects a
   file or is itself the missing file),
   `drift` (a validated entry), `path_missing` (the path exists neither from the root, nor next
   to the document, nor - a bare name - anywhere).
4. **Verdict**: the first problem status held by a fact with validated evidence, in the order
   contradicted > dangling_reference > incomplete_list > outdated_wording; otherwise
   `confirmed` (every fact confirmed and validated), `unverifiable_in_repo` (the rest is
   outside the repository) or `unknown`.

Answers are structured output (`--json-schema` for claude-code, `format` for Ollama); a
tolerant parser is the fallback, and parse failures are counted on their own.

Contract `judge/v1` (`contracts/judge-v1-*.schema.json`) carries the task type, the claim,
evidence snippets with ids, the exact prompt and its hash, the required quote, the verdict
(yes/no/unknown), confidence, the answering model, token and cost accounting - and, for the
coming **agentic** mode, an evidence list (`path`, `line`, `quote`, each verified at path:line±2)
and a tool transcript; 1.2 adds the agentic task types (`repo_drift`, `claim_facts`,
`doc_facts`) whose structured `answer` the core validates itself. In agentic mode the provider
explores the worktree through the read-only tools of `judge-tools/v1` (`list_dir`,
`read_file`, `grep`, `search`, `glob`, `git_log`, `git_show`, `git_changes`, `changes_since`,
`find_references`, `check_quote`, `outline`, `config_value`; results reach the model as plain
text with real line numbers, unescaped), which the core executes: confined to the worktree
(absolute paths, `..`, any `.git` segment and symlink escapes are refused), size- and
step-capped, private paths refused for cloud providers.

## Extractors (plugins)

| Name | Side | Finds |
|---|---|---|
| `markdown` | docs | inline code, fenced shell blocks (commands, flags, `make` targets, env assignments, paths, `cd`-aware), links and anchors, paths in prose |
| `comments` | docs | the same in source-code comments (Go, shell, Python, YAML, Terraform, JS, Dockerfile, Makefile) |
| `go` | code | programs (`cmd/<name>`), cobra commands, `flag`/`pflag` flags, sub-command switches, `os.Getenv` and env helpers, Prometheus and OpenTelemetry metric names, hand-written exposition |
| `python` | code | argparse and click flags/commands, `os.environ`/`os.getenv`, `prometheus_client` metrics |
| `shell` | code | scripts as programs, `getopts` and `case` flags, environment inputs (`${VAR:-default}`, read-but-never-assigned) |
| `yaml` | code | GitHub Actions workflow and action inputs, Kubernetes ConfigMap keys and container env, kustomize literals, Helm values, Prometheus recording rules, keys of public config files |
| `make`, `terraform`, `javascript` | code | Makefile targets; `variable`/`output` names; `process.env` and k6 `__ENV` |

An extractor is a Go type implementing `plugin.Extractor` (pure function from one file to
symbols and references), or an **external program** declared in `.docsp.yaml`: it receives the
repository root as its last argument and prints an extract/v1 document
(`contracts/extract-v1.schema.json`) on stdout. External extractors are run without a shell,
with a minimal environment and a timeout; their output is schema-validated.

## GitHub Action

```yaml
- uses: actions/checkout@v7
  with: {fetch-depth: 0, persist-credentials: false}
- uses: <owner>/docs-platform@<sha>
  with:
    mode: both            # check, impact or both
    fail-on: medium
```

| Input | Default | Meaning |
|---|---|---|
| `mode` | `both` | `check`, `impact` or `both` (impact runs on pull requests only) |
| `path` | `.` | repository path |
| `config` | | configuration file |
| `min-confidence` | `medium` | as `--min-confidence` |
| `fail-on` | `medium` | as `--fail-on` |
| `findings-file` | `docsp-findings.json` | where the findings document is written |

The PR description reaches the tool only through an environment variable and a file, never
through the shell script, so a crafted PR body cannot inject commands.

### Event-driven consistency: `MIERAI/docs-platform/ci`

```yaml
on: {push: {branches: [main]}, pull_request: {}, schedule: [{cron: "23 2 * * 1"}]}
permissions: {contents: read, pull-requests: write, issues: write, actions: read}
jobs:
  docs:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<sha>
        with: {fetch-depth: 0, persist-credentials: false, ref: "${{ github.event.pull_request.head.sha || github.sha }}"}
      - uses: MIERAI/docs-platform/ci@<sha>
        with:
          task: ${{ github.event_name == 'pull_request' && 'pr' || 'main' }}   # or factory-pr
          claude-oauth-token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
```

- `task: main` (push, schedule): keeps the claim ledger in the **Actions cache**, never in the
  repository (it is private data). The first run builds it (`audit -mode agentic`, up to
  `bootstrap-usd`); later runs do `stale` → `schedule` → `recheck`. The weekly run also keeps the
  cache from expiring.
- `task: pr`: copies the default branch's ledger, rechecks at the PR's head only the claims the
  change made stale (priority 0), and `docsp pr-comment -before <ledger> -after <rechecked copy>
  -post-pr N -github-repo owner/name` posts the problems the change
  *introduces* (not the backlog) as one comment - one sentence, "fix the doc or the code?"
  answered by ticking a box, the evidence folded; updated only when the findings change.
- `task: factory-pr`: `factory export` at the head, then `docsp factory-pr` (see above).
- **Budget**: every judge appends its spend to `DOCSP_SPEND_LOG`; the run records it as an
  artifact `docsp-usd-<YYYY-MM>-<usd>-<task>-<run>-<attempt>`, and the next run adds this month's
  up. Past `monthly-usd` (default 20) no paid model runs until next month; one run is capped by
  `run-usd` (main 3, PR 1). No token (a fork's pull request) or a private repository: no AI.
- Inputs: `model` (claude-sonnet-5-5), `claude-code-version`, `monthly-usd`, `run-usd`,
  `bootstrap-usd` (8), `factory` (factory-pr), `github-token`.

## Findings, ledger, SLOs

`docsp check` emits **findings/v1**, the same contract as the sibling product
security-platform, so one ledger, scorecard and dashboard serve both. Finding ids are
`sha1(layer|asset|check)[:12]` with line numbers deliberately left out of the asset, so editing
unrelated lines does not reopen findings; `coverage` entries tell the ledger what was inspected,
so a finding is only marked fixed when its document was actually checked again. docs-platform
needs two additive changes to that contract (a `docs` layer and the `docs_` metric namespace);
see [docs/contracts.md](docs/contracts.md).

Metrics carried in every document: `docs_references{kind,status}`,
`docs_public_symbols{kind,documented}`, `docs_reference_resolution_ratio` and
`docs_public_symbol_coverage_ratio`; `docsp impact` emits `docs_impact_changes` for the
"doc-impacting PRs with docs" SLI.

## Benchmark

[REPRODUCE.md](REPRODUCE.md) reproduces every headline number from this repository alone:
`make repro-offline` (no model) and a paid tier per provider. `bench/data` holds the
benchmark data about the three public repositories (ground truth, DEV/TEST eval sets, the
prototype's frozen answers and drift lists, the mutation set, expected numbers), pinned by
full SHA in `bench/data/manifest.json`. More `bench` commands:

```sh
go run ./bench fetch -work <scratch>              # clone the public repos at the pinned SHAs
go run ./bench mutate -work <scratch> -out m.json # regenerate the mutation set (deterministic)
go run ./bench verify-offline [-expected FILE] -eval-test F -eval-dev F -ledger-replay F -replay F -evalset F -mutations F
```

`bench/` has two benchmarks against human-confirmed ground truth (JSON Lines: `repo, file, line,
claim, actual, type, fix, evidence_paths`). Each public repository is cloned read-only into the
scratch directory.

**History replay (primary metric, model-independent):**

```sh
go run ./bench replay -gt <dir> -work <scratch> -pin name=rev ... [-config-dir <instance configs>] -md replay.md -json replay.json [-eval-out evalset.jsonl]
```

For each item: `git blame` finds the commit that last wrote the stale line; the commits after it
that touched the item's evidence paths are the *causing* commits. Each is replayed (its tree
materialized from `git archive`, its own diff) through the same derivation as `docsp impact`,
and the item scores when the section holding the stale line was listed as `reread` at the first
causing commit, or at any (`--max-commits N` causing commits per item, default 25; `--parallel N`
commits at a time). Items with no identifiable causing commit are reported separately;
the cost side is how many sections each change asks to re-read. `-eval-out` freezes the model
eval set from the same history.

**Ledger backtest (evidence-based invalidation without hindsight):**

```sh
go run ./bench ledger-backtest -ledger <ledger judged before the causing commits> -gt <dir> -set <eval set> \
  -repo name=<checkout with history> ... [-impact replay.json] [-transcripts '<eval results glob>' -transcript-sets '<sets glob>'] [-json out.json]
```

It replays history forward from each claim's verified revision and reports, for the ledger
alone, with each footprint rule (weak, siblings, members) and with all of them, how many
ground-truth positives were caught at their first / any causing commit and the per-commit
stale cost; `-impact` adds the change-time impact selection and ledger OR impact on the same
items; `-transcripts` with `-transcript-sets` adds footprints to a ledger recorded without them.

**Snapshot check / audit:**

```sh
go run ./bench -gt <dir of *.jsonl> -work <scratch dir> [-pin name=rev] [-config-dir <instance configs>] -md report.md [-json report.json] [-denylist FILE] [-audit [-instance-config FILE] [-max-questions N]]
```

It runs the check in-process and reports per finding type, per ground-truth type, per estimated
detector layer and per confidence threshold. Ground truth is never exhaustive, so precision is a
lower bound; unmatched findings are listed for triage. With `-audit` it also scores the semantic
audit per sub-layer and per rule.

## Development

```sh
make test        # unit, table, golden and contract tests
make race
make lint        # golangci-lint v2.15.0 via `go run` (strict: funlen 50, gocognit, gosec, wrapcheck, ...)
make repro-offline # REPRODUCE.md offline tier; fails when a number differs
make fuzz        # every parser of external input (FUZZTIME=30s each, GO=go)
make vuln        # govulncheck v1.9.0 via `go run`
make golden      # rewrite golden files after an intended change, then review the diff
make build-replay # bin/docsp-judge-replay, the offline canned-answer provider
make check-self  # docs-platform checks its own documentation
```

Requirements: Go (see `go.mod`), git. The core has one third-party dependency
(`go.yaml.in/yaml/v3`); the Anthropic provider is a separate module (`providers/anthropic`,
built by `make providers`) so its SDK never enters the core.

## Security

- Read-only against inspected repositories; no shell, no execution of repository content.
- Secrets and private instance files are read only from mode-0600 files or the environment and
  never logged.
- Findings may describe private systems: output files are written with mode 0600.
- AI is never allowed to change state; the default provider is `none`; judgements and drafts
  are proposals.
- Data classification is enforced in code: a cloud provider (claude-code, anthropic, exec unless
  declared local) only ever receives content classified `public` in the instance configuration;
  everything else is refused and reported. The default class is `private`.
- Credentials: the Anthropic key only from a 0600 file or a named environment variable, never
  logged or written; claude-code relies on the CLI's own login and docsp never reads it; the
  Anthropic client only talks to `https://api.anthropic.com`.

## License

Apache-2.0, see [LICENSE](LICENSE).
