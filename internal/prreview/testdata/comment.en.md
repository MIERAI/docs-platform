<!-- docsp:pr -->
<!-- docsp:sig 6f7d953a28f5c106 -->
**docs-platform**: this PR changed the code so that **3 documentation lines** no longer match it.
**docs-platform**: for **1 documentation lines** the code disagrees with itself (e.g. two places set different defaults); they were not judged against either side.

> ⚠️ **Incomplete: this does not mean everything is fine.** This check did not finish; what is listed below is not everything:
> - the run's AI budget ran out before every affected documentation line was judged
> - the AI judge failed on some lines (errors, timeouts or refusals)

#### [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3) · contradicts the code · 📖

retention is 14d in cfg.yaml

**Should the document change, or is the code wrong?**

- [x] The document is out of date: I will fix it <!-- docsp:fb README.md:3 design_stale contradicted read -->
- [ ] The code is wrong: I will fix it <!-- docsp:fb README.md:3 code_wrong contradicted read -->
- [ ] Neither: nothing is inconsistent here (false positive) <!-- docsp:fb README.md:3 not_a_problem contradicted read -->

<details><summary>Evidence</summary>

Document [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3)

```markdown
Retention is 7 days.
```

Code [`cfg.yaml:2`](https://github.com/o/demo/blob/abc123/cfg.yaml#L2)

```yaml
retention: 14d
```

- retention is 14d in cfg.yaml — contradicted

</details>

#### [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9) · incomplete list · 📖

**Should the document change, or is the code wrong?**

- [ ] The document is out of date: I will fix it <!-- docsp:fb README.md:9 design_stale incomplete_list read -->
- [ ] The code is wrong: I will fix it <!-- docsp:fb README.md:9 code_wrong incomplete_list read -->
- [ ] Neither: nothing is inconsistent here (false positive) <!-- docsp:fb README.md:9 not_a_problem incomplete_list read -->

<details><summary>Evidence</summary>

Document [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9)

```markdown
Jobs: build, test.
```


</details>

#### [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4) · refers to something that does not exist · 📖

**Should the document change, or is the code wrong?**

- [ ] The document is out of date: I will fix it <!-- docsp:fb docs/run.md:4 design_stale dangling_reference read -->
- [ ] The code is wrong: I will fix it <!-- docsp:fb docs/run.md:4 code_wrong dangling_reference read -->
- [ ] Neither: nothing is inconsistent here (false positive) <!-- docsp:fb docs/run.md:4 not_a_problem dangling_reference read -->

<details><summary>Evidence</summary>

Document [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4)

```markdown
See `old.md`.
```


</details>

…and **1 more** not listed here: the full list is in [the job summary](https://github.com/o/demo/actions/runs/1).

<sub>📖 = the AI's reading of the code; it can be wrong. Ticks are only used to measure this check's false-positive rate. The platform never changes a file.</sub>
