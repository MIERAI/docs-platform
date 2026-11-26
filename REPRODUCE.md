# Reproducing docs-platform from zero

This file is for someone who has only this repository. It builds `docsp`, runs it, and
reproduces the headline numbers of [docs/design.md](docs/design.md) §3.8, §9 and §12.7.
How the design got here, with every number's source, is in
[docs/experiments.md](docs/experiments.md).

There are two tiers:

- **Offline** (no model, no API key, no cost): unit tests, the replay of frozen model answers
  that reproduces the agentic TEST recall 0.80, the ledger replay, the history replay and
  the mutation set. One command, `make repro-offline`, runs all of it and fails when a
  number differs from [bench/data/expected-offline.json](bench/data/expected-offline.json).
- **Paid** (a model answers): the same TEST set judged live by Claude Code, the Anthropic
  API or a local Ollama model. Results vary between runs; run at least twice.

## 1. Prerequisites

| Tool | Version | Check | Needed for |
|---|---|---|---|
| Go | 1.27.0 or newer (the `go` line of `go.mod`; tested with 1.27.4) | `go version` | everything |
| git | tested with 2.50.1; any 2.3x+ should do (`merge-base --is-ancestor`, `ls-tree`, `fetch <sha>`) | `git --version` | everything |
| network | `proxy.golang.org` (Go modules) and `github.com` (three public repositories, about 20 MB) | `git ls-remote https://github.com/MIERAI/gitops-manifests.git HEAD` | offline tier |
| ripgrep | not needed: `docsp`'s search tool is built in. Only one archived prototype configuration used `rg`. | - | - |
| Python | not needed: the mutation-set generator is ported to Go (`go run ./bench mutate`). | - | - |
| Claude Code CLI | any current version (the paid runs used 2.1.33x) | `claude --version` | paid tier, provider `claude-code` |
| Anthropic API key | an API key in an environment variable | `test -n "$ANTHROPIC_API_KEY" && echo set` (prints only `set`) | paid tier, provider `anthropic` |
| Ollama | a local server with a tool-calling model | `curl -s http://127.0.0.1:11434/api/tags` | paid tier, provider `ollama` |

macOS and Linux are tested; the scripts are bash.

## 2. Build

```sh
make build            # bin/docsp
make build-replay     # bin/docsp-judge-replay (offline provider answering from frozen answers)
make providers        # bin/docsp-judge-anthropic (Anthropic API provider, its own Go module)
bin/docsp version
```

`bin/docsp check -format text .` runs the reference check on any repository (this one
included: `make check-self`).

## 3. Offline tier

```sh
make repro-offline                       # about 1.5-3 minutes once Go modules are downloaded
REPRO_WORK=/some/scratch make repro-offline   # choose the scratch directory
```

`REPRO_WORK` (default `$TMPDIR/docsp-repro`, never inside the repository) receives the clones
and every output; it is reused between runs. `SKIP_TESTS=1` skips step 1. The script is
[scripts/repro-offline.sh](scripts/repro-offline.sh); each step can be run by hand:

| Step | Command (inside the script) | What it shows |
|---|---|---|
| 1 | `go test ./...` and `(cd providers/anthropic && go test ./...)` | unit, golden, contract, fake-provider tests |
| 2 | `go build ./cmd/docsp ./cmd/docsp-judge-replay` | the binaries |
| 3 | `go run ./bench fetch -work $REPRO_WORK` | full clones of the three public repositories at the pinned SHAs of [bench/data/manifest.json](bench/data/manifest.json); also verifies every data file's sha256 |
| 4 | `bin/docsp eval-model -set bench/data/eval/agentic-test.jsonl -mode agentic -instance-config <replay> -worktree ... -ledger ...` | the agentic TEST set judged by `docsp-judge-replay` answering with the prototype's frozen answers; the current harness re-validates every piece of evidence and derives each verdict |
| 5 | the same on `agentic-dev.jsonl` | the DEV set |
| 6 | `go run ./bench ledger-replay -ledger <from step 4> ...` | evidence-based invalidation over every first-parent commit |
| 7 | `go run ./bench replay -manifest bench/data/manifest.json -eval-out ...` | change-time impact (history replay) and the frozen snippet eval set |
| 8 | `go run ./bench mutate ...`, then `go run ./bench verify-offline ...` | regenerates the mutation set; compares 46 numbers with the expected file |

The last lines must be:

```
ok       agentic_test.recall                          want 0.80                     got 0.80
...
all 46 expected numbers reproduced
```

The headline lines of the run, and what they mean:

| Output line | Expected | Meaning |
|---|---|---|
| step 4: `precision 0.86, recall 0.80, unknown 0.10, quote rejection 0.00 (tp 24 fp 4 fn 6 tn 26)` | exact | the prototype's frozen TEST answers, re-scored by the product: 24 of 30 positives flagged, 4 of 30 controls flagged (unadjudicated; see caveats) |
| step 4: `drift gitops-manifests @9218a247e391: scanned, 18 entries, 18 validated` and `infrastructure-live ... 15 entries, 15 validated` | exact | every frozen drift entry passes the harness's validation |
| step 4: `prompt templates 5f6ad97cd5674d02` | exact | the prompt text the paid v0.4.2 runs used; a change means the paid numbers below no longer apply |
| step 5: `precision 0.95, recall 0.60 ... (tp 18 fp 1 fn 12 tn 28)` | exact | the same for the DEV set (the prototype's DEV answers) |
| step 6: `gitops-manifests \| 44 \| 329 \| 0.66 \| 0 \| 2 \| 11 \| 83% \| 5.0%` and `infrastructure-live \| 16 \| 50 \| 0.80 \| 0 \| 3 \| 5 \| 64% \| 12.0%` | exact | the ledger's cost: claims staled per commit |
| step 6: `16/21 (76%) \| 19/21 (90%)` and `3/4 (75%) \| 4/4 (100%)` | exact | together 19/25 (76%) of the scorable positives are invalidated at the first causing commit, 23/25 (92%) at some causing commit |
| verify: `impact_on_ledger_items.first_causing 9/25`, `any_causing 13/25` | exact | the change-time impact selection on the same 25 items: 36% / 52% |
| step 7: `dev (api-factory) \| 74 \| 60 \| 14 \| 21/60 (35%) \| 32/60 (53%)` and `test (gitops + infra, held out) \| 73 \| 61 \| 12 \| 32/61 (52%) \| 41/61 (67%)` | exact | history replay (§9.2): the stale section is listed for re-reading at the first / any causing commit |
| verify: `snippet_evalset.sha256 46d0dd2f...` | exact | the snippet-mode eval set (352 items) is regenerated byte for byte from history |
| verify: `mutations.count 47`, `affected_lines 74`, `unique_affected_lines 65` | exact | the mutation set, byte-identical to the prototype's |

Everything in the offline tier is deterministic: a mismatch is a regression (or an intended
change, which then updates `bench/data/expected-offline.json`, the manifest hashes and the
CHANGELOG together).

### Lint and the other checks

```sh
make lint     # golangci-lint v2.15.0, built from source with `go run` (no binary download)
make race     # go test -race, core and providers/anthropic
make vuln     # govulncheck v1.9.0, also via `go run`
make check-self
```

`make lint` needs network for the first module download only. Afterwards it also runs
without network from the module cache:
`GOPROXY=file://$(go env GOMODCACHE)/cache/download GOSUMDB=off make lint` (tested). Plain
`GOPROXY=off` does not work with `go run pkg@version`. `GOLANGCI_LINT=/path/to/golangci-lint
make lint` uses an installed binary instead.

## 4. Benchmark data (bench/data)

Everything in `bench/data` is about three public repositories, pinned by full SHA in the
manifest (each SHA was checked to exist on GitHub with `git ls-remote` and a fetch by SHA on
2026-11-26):

| Repository | Revision | Split |
|---|---|---|
| [MIERAI/api-factory](https://github.com/MIERAI/api-factory) | `d7555c93fc9cb39c0efe7a8829e79d33d0e5c3f8` | dev |
| [MIERAI/gitops-manifests](https://github.com/MIERAI/gitops-manifests) | `9218a247e3910995ebe5f801579f30655d1d079e` | test (held out) |
| [MIERAI/infrastructure-live](https://github.com/MIERAI/infrastructure-live) | `25db81482feb85f76b824fcd8cd8fd4469948dfd` | test (held out) |

| File | Content |
|---|---|
| `groundtruth/*.jsonl` | 147 documentation defects (wrong 30 / obsolete 62 / missing 55): `repo, file, line, claim, actual, type, fix, evidence_paths` |
| `eval/agentic-test.jsonl` | TEST: 30 ground-truth positives + 30 controls verified consistent at the revision (sha256 `b0cbc271...`, the set of every paid TEST run) |
| `eval/agentic-dev.jsonl` | DEV: 30 positives + 29 controls of api-factory |
| `answers/test-frozen.jsonl`, `answers/dev-frozen.jsonl` | the prototype's frozen model answers (Sonnet 5.5), keyed `drift:<repo>` / `line:<repo>:<path>:<line>`, for `docsp-judge-replay` |
| `drift/*.json` | the prototype's drift lists (raw and validated entries, cost, turns) |
| `mutations.json` | the mutation set (`bench mutate` regenerates it) |
| `expected-offline.json` | the numbers `make repro-offline` must reproduce |
| `manifest.json` | pins and sha256 of every file |

The snippet eval set (352 items with code snippets) is not stored: step 7 regenerates it
from history and checks its sha256. It quotes encrypted (SOPS) files of the public
repositories, which stay out of this repository.

**Privacy.** `go test ./bench` (part of `make test`) fails when a data file contains a local
absolute path, an e-mail address, an API key or token pattern, a private key or SOPS
ciphertext; with `DOCSP_DENYLIST=<private term list, mode 0600>` it also checks those terms
(the same file `docsp check -denylist` reads). Host names, Tailscale addresses and `~/...`
paths that appear in the data are quoted from the public repositories themselves (each was
checked to occur in their public history).

## 5. Paid tier

The paid tier judges the TEST set live. It costs money (or subscription quota) and its
result varies from run to run: **run it at least twice** and compare the range, not one run.

Common setup:

```sh
make build providers
REPRO_WORK=${REPRO_WORK:-${TMPDIR:-/tmp}/docsp-repro}
go run ./bench fetch -work "$REPRO_WORK"          # same clones as the offline tier
WT="-worktree gitops-manifests=$REPRO_WORK/gitops-manifests -worktree infrastructure-live=$REPRO_WORK/infrastructure-live"
```

Always estimate first (no model is called):

```sh
bin/docsp eval-model -set bench/data/eval/agentic-test.jsonl -instance-config paid.yaml \
  -mode agentic -dry-run -no-drift-cache $WT
```

Then the run (each run writes an eval/v1 JSON into `-out-dir`; `-no-drift-cache` makes every
run scan the history itself, as the measured runs did):

```sh
mkdir -p out
bin/docsp eval-model -set bench/data/eval/agentic-test.jsonl -instance-config paid.yaml \
  -mode agentic -no-drift-cache -out-dir out $WT
```

The line to read: `precision P, recall R, unknown U ... (tp .. fp .. fn .. tn ..)`.

### 5.1 Claude Code CLI (`provider: claude-code`)

Uses the operator's own Claude Code login (subscription quota, no API bill; docsp never
reads the credential). In CI, name the token variable in `claude_code.pass_env`.

```yaml
# paid.yaml
version: 1
ai:
  judge:
    provider: claude-code
    model: claude-sonnet-5-5
    timeout_seconds: 180
    max_concurrency: 2
    budget: {max_requests: 300, max_usd: 6}
    claude_code: {program: /absolute/path/to/claude}   # `command -v claude`
classification:
  default: private
  repos: {gitops-manifests: public, infrastructure-live: public, api-factory: public}
```

| | Expected | Source |
|---|---|---|
| dry-run estimate | `cost $2.5113` (60 lines + 2 drift scans) | `-dry-run`, this repository |
| recall on TEST | **0.73-0.77** (22/30 and 23/30 in the two v0.4.2 runs) | `eval-judge-claude-code-claude-sonnet-5-5-20261126T000956Z/001345Z` |
| precision | 0.82-0.85 (controls flagged 4-5/30, unadjudicated) | same |
| unknown rate | 0.10-0.12 | same |
| nominal cost | $2.74-3.33 per run (list-price equivalent; billed to the subscription) | same |
| drift validated | 23/23 and 18/18 | same |

A single run outside 0.67-0.83 (two items away from the observed range) is worth
investigating before it is believed; that threshold is a judgement, not a measured bound.

### 5.2 Anthropic API (`provider: anthropic`)

For users with an API key and no Claude Code subscription. `docsp-judge-anthropic` (module
`providers/anthropic`, official Go SDK) runs the agentic tool-use loop itself: every tool
call goes back to `docsp`, which executes it read-only inside the worktree and refuses
private paths; the answer is structured output bound to the reply schema.

**The key.** Put it in an environment variable and name the variable in the configuration
(`api_key_env`). docsp passes the variable through by name only; the provider reads it once
and never logs, prints or stores it. (A mode-0600 file named by `api_key_file` is the
alternative.) Requests go only to `https://api.anthropic.com`.

```yaml
# paid.yaml
version: 1
ai:
  judge:
    provider: anthropic
    model: claude-sonnet-5-5
    timeout_seconds: 300
    max_concurrency: 2
    budget: {max_requests: 300, max_usd: 6}     # hard stop for the whole run
    anthropic:
      program: /absolute/path/to/repo/bin/docsp-judge-anthropic
      api_key_env: ANTHROPIC_API_KEY
      fallbacks: default                        # server-side refusal fallback (recorded)
    agentic:
      max_request_usd: 0.6                      # cap per line; ends that line only
      drift_max_request_usd: 2.5
      # effort: high                            # unset = the model's default (as evaluated)
classification:
  default: private
  repos: {gitops-manifests: public, infrastructure-live: public, api-factory: public}
```

| | Expected | Source |
|---|---|---|
| dry-run estimate | `cost $2.3501` (Sonnet 5.5), `$4.7002` (Opus 5.5) | `-dry-run`, this repository |
| cost | not measured yet; budget $2.5-3.5 per run on Sonnet (the Claude Code runs of the same prompts cost $2.74-3.48) | estimate |
| recall | **not measured yet.** The prompts are the ones evaluated with Claude Code, but the model does not get Claude Code's own agent prompt, so the first two runs set this provider's baseline. Compare with 0.73-0.77. | - |

The provider is unit-tested against a fake HTTP server only (`cd providers/anthropic && go
test ./...`); no live API call was made while building it.

### 5.3 Ollama (`provider: ollama`)

Local, free, slow. The dialer refuses non-local addresses.

```yaml
# paid.yaml
version: 1
ai:
  judge:
    provider: ollama
    model: gemma4:12b
    timeout_seconds: 900
    max_concurrency: 1
    ollama: {endpoint: "http://127.0.0.1:11434", num_ctx: 32768}
classification:
  default: private
```

| | Expected | Source |
|---|---|---|
| cost | $0; tokens about 2.9M in / 0.16M out for TEST (dry run) | `-dry-run` |
| recall | **not measured with the product.** The prototype, judging with a local model but with Sonnet's drift lists ("hybrid"), scored gemma4:12b 15/30 (0/30 controls flagged, 20 unknown, about 85 s per line) and qwen3.6:35b 22/30 (12/30 controls flagged). The product runs the drift scan with the same local model, so expect lower. | `runs/test/v2L_*_hybrid` (archive) |

## 6. What the numbers mean, and their caveats

- **n is small.** TEST is 30 positives and 30 controls; one item is 3.3 points of recall.
  0.73 vs 0.77 is one item. DEV and TEST are separate repositories; designs were tuned on
  DEV, TEST was run once per design.
- **Ground truth is one strong-AI audit.** The 147 defects were produced by a strong model
  and spot-checked (§9.1 of the design: of 15 disagreements, 1 ground-truth item was wrong
  and 3 debatable). Recall and precision therefore measure *agreement with that audit*,
  not accuracy, and strong models are favoured.
- **Controls are assumed consistent.** A flagged control counts as a false positive. In the
  prototype's TEST run, 4 controls were flagged and adjudication found 1 real false
  positive (design §3.8); the 0.86 precision is the unadjudicated figure.
- **The replay is not a model run.** The 0.80 is the prototype's answers re-scored by the
  product. It proves the port is faithful (60/60 verdicts equal the prototype's), not that
  a live run scores 0.80: the live product runs score 0.73-0.77.
- **The ledger replay is optimistic.** The ledger is built from the TEST-time answers, whose
  evidence cites the very files that later revealed the line as stale, and causing commits
  are defined from the ground truth's evidence paths; the two overlap. A ledger built when
  the line was written would not always cite those files. 76% / 92% vs the impact
  selection's 36% / 52% is an upper bound of the advantage (design §12.7).
- **History replay** counts a stale section as caught only when listed for re-reading at a
  causing commit; items without a causing commit (26 of 147) are reported, not scored.
- **The snippet eval set** labels positives the same way (agreement with the audit).

## 7. What a stranger still cannot reproduce

- **Live model numbers exactly.** Model outputs vary; the paid tier gives ranges from two
  runs, not a deterministic value.
- **The Anthropic API provider's agentic recall and cost:** implemented and fake-tested,
  never run against the live API.
- **Ollama with the product:** never run; only prototype numbers exist.
- **The prototype experiments** (gemma configurations, snippet vs agentic on Sonnet, the
  whole-document audit): their code and raw runs live in the owner's private archive, not
  here. Their numbers are documented in docs/experiments.md with their sources.
- **The ground-truth audit itself** (how the 147 items were found) and the control
  adjudication are not reproducible; their results are data here.
- **Pins on GitHub.** The pinned SHAs exist today; if a repository were rewritten or
  removed, `bench fetch` fails with "pinned revision ... not found on GitHub".
