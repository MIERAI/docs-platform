# docs-platform

Keep documentation true to the code, the SRE way: **detect drift → findings → ledger → SLOs →
remediation loop**.

Documentation rots silently. A flag is renamed, an environment variable is dropped, a script
moves, and the README keeps describing the old world until a newcomer trips over it.
docs-platform treats that drift like any other reliability problem: a deterministic detector
emits findings in a shared, versioned contract; a ledger tracks each finding from first seen to
fixed; SLOs ("drift fixed within N days", "share of doc-impacting PRs that touch docs") make the
state visible; AI may *propose* fixes, humans decide.

`docsp` is the command-line tool. This first release implements:

| Layer | What | Status |
|---|---|---|
| ① generated reference docs + CI drift check | docs rendered from code, CI fails when stale | design |
| ② executable docs | shell blocks run in CI | design |
| ③ reference check | every identifier a document mentions exists in code; every public symbol is documented | **implemented** |
| ④a PR impact | a change to mapped code must touch the mapped docs, or carry a reason | **implemented** |
| ④b/④c AI proposals and audits | AI proposes a docs PR after merge; periodic AI audit → findings | design (provider interface only, default `none`) |

The design (in Chinese) is [docs/design.md](docs/design.md); the data contracts are described in
[docs/contracts.md](docs/contracts.md).

## Quick start

```sh
go build -o bin/docsp ./cmd/docsp
bin/docsp check -format text /path/to/repo          # human summary
bin/docsp check -out findings.json /path/to/repo    # findings/v1 JSON (mode 0600)
bin/docsp impact -base origin/main /path/to/repo    # PR impact check
```

`docsp` never writes into the repository it inspects, never runs code from it, and only calls
the read-only git sub-commands `diff`, `log`, `rev-parse` and `ls-files`.

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

Layer ④a. `.docsp.yaml` maps code paths to the documents that describe them. When a change
touches mapped code but none of its documents, the check fails - unless a commit message or the
PR description carries an explicit reason as a trailer, `Docs-Not-Needed: <reason>` (the key is
configurable). A trailer without a reason is not a waiver. The result is an impact/v1 document.

| Flag | Meaning |
|---|---|
| `--base REF` | base revision of the change (required) |
| `--head REF` | head revision (default `HEAD`) |
| `--config FILE` | configuration file |
| `--pr-body-file FILE` | the PR description, searched for the waiver trailer |
| `--out FILE` | write the impact JSON to FILE (mode 0600) |

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
extractors:
  disable: [terraform]
  external:
    - name: rust
      command: ["/usr/local/bin/docsp-extract-rust"]
```

A single line can be exempted in a document with `<!-- docsp:ignore <reason> -->` on the line
before it, a region with `<!-- docsp:ignore-start <reason> -->` … `<!-- docsp:ignore-end -->`.

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

`bench/` measures recall and precision against human-confirmed ground truth (JSON Lines:
`repo, file, line, claim, actual, type, fix, evidence_paths`):

```sh
go run ./bench -gt <dir of *.jsonl> -work <scratch dir> [-pin name=rev] [-config-dir <instance configs>] -md report.md [-json report.json] [-denylist FILE]
```

It clones each public repository read-only into the scratch directory, runs the check
in-process, and reports per finding type, per ground-truth type, per estimated detector layer
(reference vs semantic) and per confidence threshold. Ground truth is never exhaustive, so
precision is a lower bound; unmatched findings are listed for triage.

## Development

```sh
make test        # unit, table, golden and contract tests
make race
make lint        # golangci-lint (strict: funlen 50, gocognit, gosec, wrapcheck, ...)
make fuzz        # every parser of external input (FUZZTIME=30s each, GO=go)
make vuln        # govulncheck
make golden      # rewrite golden files after an intended change, then review the diff
make check-self  # docs-platform checks its own documentation
```

Requirements: Go (see `go.mod`), git. One third-party dependency: `go.yaml.in/yaml/v3`.

## Security

- Read-only against inspected repositories; no shell, no execution of repository content.
- Secrets and private instance files are read only from mode-0600 files or the environment and
  never logged.
- Findings may describe private systems: output files are written with mode 0600.
- AI is never allowed to change state; the default AI provider is `none`.

## License

Apache-2.0, see [LICENSE](LICENSE).
