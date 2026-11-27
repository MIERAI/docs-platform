<!-- docsp:pr -->
<!-- docsp:sig 4dc7215fffa33542 -->
**docs-platform**:这个 PR 改了代码,让 **3 处文档**和代码不一致了。
**docs-platform**:有 **1 处文档**依据的代码自己说法不一(比如两处代码设了不同的默认值),没有拿文档去和其中一方比。

> ⚠️ **未完成:这不代表一切正常。** 这次检查没有跑完,下面列的不是全部:
> - 这次运行的 AI 预算用完了,有些被这个 PR 影响的文档行没有判定
> - AI 判定对部分文档行失败了(错误、超时或拒绝)

#### [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3) · 和代码矛盾 · 📖

retention is 14d in cfg.yaml

**文档该改,还是代码改错了?**

- [x] 文档过时了:我会改文档 <!-- docsp:fb README.md:3 design_stale contradicted read -->
- [ ] 代码改错了:我会修代码 <!-- docsp:fb README.md:3 code_wrong contradicted read -->
- [ ] 都不是:这里没有不一致(误报) <!-- docsp:fb README.md:3 not_a_problem contradicted read -->

<details><summary>证据</summary>

文档 [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3)

```markdown
Retention is 7 days.
```

代码 [`cfg.yaml:2`](https://github.com/o/demo/blob/abc123/cfg.yaml#L2)

```yaml
retention: 14d
```

- retention is 14d in cfg.yaml — contradicted

</details>

#### [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9) · 列举不完整 · 📖

**文档该改,还是代码改错了?**

- [ ] 文档过时了:我会改文档 <!-- docsp:fb README.md:9 design_stale incomplete_list read -->
- [ ] 代码改错了:我会修代码 <!-- docsp:fb README.md:9 code_wrong incomplete_list read -->
- [ ] 都不是:这里没有不一致(误报) <!-- docsp:fb README.md:9 not_a_problem incomplete_list read -->

<details><summary>证据</summary>

文档 [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9)

```markdown
Jobs: build, test.
```


</details>

#### [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4) · 引用的东西不存在 · 📖

**文档该改,还是代码改错了?**

- [ ] 文档过时了:我会改文档 <!-- docsp:fb docs/run.md:4 design_stale dangling_reference read -->
- [ ] 代码改错了:我会修代码 <!-- docsp:fb docs/run.md:4 code_wrong dangling_reference read -->
- [ ] 都不是:这里没有不一致(误报) <!-- docsp:fb docs/run.md:4 not_a_problem dangling_reference read -->

<details><summary>证据</summary>

文档 [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4)

```markdown
See `old.md`.
```


</details>

……另外还有 **1 处**没有在这里列出,完整列表见[任务摘要(job summary)](https://github.com/o/demo/actions/runs/1)。

<sub>📖 = AI 读代码的判断,可能是误报。勾选只用来统计这个检查的误报率。平台不会改任何文件。</sub>
