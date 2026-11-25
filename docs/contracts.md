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

## findings/v1

docs-platform emits the findings contract defined by security-platform (unmodified upstream copy:
`contracts/upstream/security-platform-findings-v1.2.schema.json`), at minor **1.3 (proposed)**.

### Proposed additive extension (1.3)

1. `findings[].layer` gains the value `docs`.
2. `metrics[].name` accepts the `docs_` prefix (`^(security|docs)_[a-z0-9_]+$`).
3. `findings[].attributes.docs` is documented (`$defs.docsAttributes`); `attributes` was already
   a free object, so this changes nothing for existing consumers.

`TestContract` in `internal/check` validates real output against the 1.3 schema and against the
unmodified upstream 1.2 schema, and fails if any difference other than (1) and (2) appears.

Later, optional: promote `locations[]` to a typed top-level field (useful to security code
scanners as well) and register the `doc` and `code` asset types.

### Field mapping

| Field | docs-platform value |
|---|---|
| `collector.name` / `version` | `docs-check` / docsp version |
| `collector.tools` | `extract-<name>: <version>` for every extractor, `residue: <version>` when used |
| `coverage[]` | `doc:<repo>` and `code:<repo>` ok; `doc:<repo>/<file>` gap for a file no extractor could read |
| `id` | `sha1(layer + "|" + asset + "|" + check)[:12]` (as upstream) |
| `check` | `docs.reference.unresolved.<kind>`, `docs.symbol.undocumented.<kind>`, `docs.reference.outside_repo`, `docs.residue.term` |
| `layer` | `docs` |
| `asset` | wrong/outside/residue: `doc:<repo>/<doc path>#<kind>:[<program> ]<subject>`; undocumented: `code:<repo>/<kind>:[<program> ]<subject>` - never a line number |
| `severity` | wrong/outside: high confidence → `medium`, medium → `low`, low → `info`; undocumented: high → `low`, else `info`; residue → `medium` |
| `kind` | `compliance` |
| `needs_human` | `true` unless confidence is high |
| `owner` | `.docsp.yaml` `owner` (default `unassigned`) |
| `evidence` | `path:line` list of every location |
| `attributes.docs.finding_type` | `wrong_or_obsolete`, `undocumented`, `outside_repo`, `residue` (reserved: `impact_unaccompanied`, `semantic`) |
| `attributes.docs.detector_layer` | `reference` now; reserved: `generated`, `executable`, `impact`, `ai_audit` |
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

## impact/v1

One `docsp impact` run: base, head, number of changed files, one entry per rule
(`not_triggered`, `docs_updated`, `missing_docs`, `waived` with the code and docs that changed),
the waiver reason if any, and the verdict (`pass`, `fail`, `waived`, `not_applicable`).
