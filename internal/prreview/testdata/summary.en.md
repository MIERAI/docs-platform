### docs-platform: 3 documentation problems, 1 source conflicts (6 lines judged)

> ⚠️ **Incomplete: this does not mean everything is fine.** This check did not finish; what is listed below is not everything:
> - the run's AI budget ran out before every affected documentation line was judged
> - the AI judge failed on some lines (errors, timeouts or refusals)

| Line | Category | Document |
|---|---|---|
| [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3) | contradicts the code | Retention is 7 days. |
| [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9) | incomplete list | Jobs: build, test. |
| [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4) | refers to something that does not exist | See `old.md`. |
| [`docs/run.md:7`](https://github.com/o/demo/blob/abc123/docs/run.md#L7) | the code disagrees with itself | The default timeout is 30s. |
