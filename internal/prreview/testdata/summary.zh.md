### docs-platform: 3 处文档不一致,1 处代码自相矛盾(判定了 6 条)

> ⚠️ **未完成:这不代表一切正常。** 这次检查没有跑完,下面列的不是全部:
> - 这次运行的 AI 预算用完了,有些被这个 PR 影响的文档行没有判定
> - AI 判定对部分文档行失败了(错误、超时或拒绝)

| 位置 | 类别 | 文档 |
|---|---|---|
| [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3) | 和代码矛盾 | Retention is 7 days. |
| [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9) | 列举不完整 | Jobs: build, test. |
| [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4) | 引用的东西不存在 | See `old.md`. |
| [`docs/run.md:7`](https://github.com/o/demo/blob/abc123/docs/run.md#L7) | 代码内部说法不一 | The default timeout is 30s. |
