# Backlog: continuous consistency (v0.5)

Design: [design.md §12](design.md) (in Chinese). Goal: keep docs consistent with code over
time, not once - remove the docs' "phantom inventory" (claims about things that no longer
exist or no longer hold) with retail inventory control: record accuracy, cycle counting, ABC
classification. Order = implementation order. Every item: tests, docs, no model calls in CI.

## B1 Claim and evidence ledger, deterministic invalidation

- [x] B1.1 Contract `contracts/ledger-v1.schema.json`: claims (stable id independent of the
      line number, doc blob, category, verdict, `last_verified_at`, `last_verified_ref`,
      model, prompt hash, facts) and validated evidence addressed as `repo@ref:path:line`:
      quote (blob), drift (entry, commits, blobs, removed path), absence (pattern, glob,
      regex, covered file-set hash and count), path_missing (target).
- [x] B1.2 Ledger store: one JSON file per instance, mode 0600, outside inspected
      repositories; merge (newer verification of the same claim wins); private-data aware
      (paths of private classification are kept but flagged; nothing leaves the machine).
- [x] B1.3 Writers: agentic `audit` and `eval-model` record each judged line (`-ledger FILE`).
- [x] B1.4 `docsp stale -ledger FILE [-ref R] REPO`: deterministic invalidation against a new
      revision (doc line gone or moved, evidence blob changed or gone, absence scope file set
      changed, path_missing target appeared, drift removed path reappeared); claims without
      evidence → `recheck`. Output contract stale/v1 + text summary.
- [x] B1.5 Offline validation (`bench ledger-replay`): ledger from the frozen TEST answers;
      per-commit stale counts over real history; causing-commit recall of GT positives vs the
      impact selection (52% first / 67% any on the TEST slice).

## B2 Freshness, cycle counting, SLIs and error budget

- [ ] B2.1 Freshness policy in the instance config: `max_age_days`, churn threshold (commits,
      changed lines in the evidence scope), ABC classes (churn / importance) with per-class
      max age.
- [ ] B2.2 Scheduler `docsp schedule`: priority stale > due > periodic recheck, ABC and age
      within a priority; minimum count per run so every claim is verified within the period;
      budget cap (requests / USD); reports the shortfall instead of skipping silently.
- [ ] B2.3 SLIs: accuracy (weekly random sample, strong-model re-audit with harness
      validation; sampling command only, paid runs by the operator), freshness (share
      verified within max age), phantom rate (deterministic dangling references).
- [ ] B2.4 SLO and error budget: initial accuracy ≥ 80%, budget burn → full re-audit of the
      doc set + shorter max age; report SLIs, budget remaining, cost.

## B3 Repo groups and declared refs

- [ ] B3.1 Instance repo groups with roles (code, docs, api-spec, db-migration, infra).
- [ ] B3.2 Evidence across repositories (`repo@ref:path`), invalidation across repositories.
- [ ] B3.3 One drift scan per code `repo@ref`, shared by the group.
- [ ] B3.4 Doc sets declare the ref(s) they document (design docs → develop, runbooks → prod).
- [ ] B3.5 Generated docs judged at the generator input; submodules / external sources →
      unverifiable.

## B4 No forced updates

- [ ] B4.1 No "code changed, docs untouched" gate in the continuous mode; report validated
      contradictions only.
- [ ] B4.2 Debounce: judge against declared refs, batch reports.
- [ ] B4.3 Severity: contradicted / dangling immediate; outdated_wording / incomplete_list
      batched.
- [ ] B4.4 Suppressions with reason and expiry (expired = inactive).
- [ ] B4.5 Draft constraints: minimal diff changing only the contradicted facts; prefer delete
      or link over new volatile detail; measure acceptance rate, revert rate, diff size;
      recommend "generate or link instead" for docs that keep going stale.

## Experiment track

- [ ] E1 Whole-document audit (line selection); integration point = the claim ledger.
