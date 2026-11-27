<!-- docsp:factory-pr -->
<!-- docsp:sig 6224bcce956f4e55 -->
**docs-platform**: this PR makes **3 design rules** disagree with the code.

> ⚠️ **Incomplete: this does not mean everything is fine.** This check did not finish; what is listed below is not everything:
> - the AI judge failed on some lines (errors, timeouts or refusals)

#### [`design.yaml:3`](https://github.com/o/af/blob/h/design.yaml#L3) SLA 300 s · 🔬

the design says 300 s, the code 600 s

**Should the design change, or the code?**

- [ ] Yes: the design is out of date (take the folded draft) <!-- docsp:fb design.yaml:3 design_stale contradicted executed -->
- [ ] No: the code is wrong <!-- docsp:fb design.yaml:3 code_wrong contradicted executed -->

<details><summary>Evidence and draft</summary>

Design [`design.yaml:3`](https://github.com/o/af/blob/h/design.yaml#L3)

```yaml
  - SLA 300 s
```

Counterexamples (the design's examples the factory ran on this PR):

- `ex1`:`response.sla` the design expects **1**, the code gives **no value (0 / empty)**

Draft (✅ verified by the factory; save it as `design.patch` on the PR branch, then `git apply design.patch`):

```diff
--- a/design.yaml
+++ b/design.yaml
@@ -1 +1 @@
-300
+600
```

</details>

#### [`design.yaml:5`](https://github.com/o/af/blob/h/design.yaml#L5) health · 📖

**Should the design change, or the code?**

- [ ] Yes: the design is out of date (take the folded draft) <!-- docsp:fb design.yaml:5 design_stale outdated_wording read -->
- [x] No: the code is wrong <!-- docsp:fb design.yaml:5 code_wrong outdated_wording read -->
- [ ] Neither: nothing is inconsistent here (false positive) <!-- docsp:fb design.yaml:5 not_a_problem outdated_wording read -->

<details><summary>Evidence and draft</summary>

Design [`design.yaml:5`](https://github.com/o/af/blob/h/design.yaml#L5)

```yaml
  - health
```

The design's examples that fail for this API (run, but the draft does not change this rule, so they do not prove this rule is the problem):

- `ex1`:`response.sla` the design expects **1**, the code gives **no value (0 / empty)**

</details>

…and **1 more** not listed here: the full list is in [the job summary](https://github.com/o/af/actions/runs/7).

<details><summary>2 more rules were checked</summary>

- [`design.yaml:12`](https://github.com/o/af/blob/h/design.yaml#L12) name — ✅ still holds
- [`design.yaml:14`](https://github.com/o/af/blob/h/design.yaml#L14) limit — ❔ not judged

</details>

<sub>🔬 = the factory ran the design's examples on this PR and confirmed the mismatch; 📖 = the AI's reading of the code, it can be wrong. Ticks are only used to measure this check's false-positive rate. The platform never changes a file.</sub>
