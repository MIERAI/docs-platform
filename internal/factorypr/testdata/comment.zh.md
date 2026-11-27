<!-- docsp:factory-pr -->
<!-- docsp:sig b8e91bef22b58fdc -->
**docs-platform**:这个 PR 让 **3 条设计书规则**和代码不一致了。

> ⚠️ **未完成:这不代表一切正常。** 这次检查没有跑完,下面列的不是全部:
> - AI 判定对部分文档行失败了(错误、超时或拒绝)

#### [`design.yaml:3`](https://github.com/o/af/blob/h/design.yaml#L3) SLA 300 s · 🔬

the design says 300 s, the code 600 s

**设计书该改,还是代码该改?**

- [ ] 是:设计书过时了(采用折叠里的改稿) <!-- docsp:fb design.yaml:3 design_stale contradicted executed -->
- [ ] 不是:代码改错了 <!-- docsp:fb design.yaml:3 code_wrong contradicted executed -->

<details><summary>证据和改稿</summary>

设计书 [`design.yaml:3`](https://github.com/o/af/blob/h/design.yaml#L3)

```yaml
  - SLA 300 s
```

反例(工厂在这个 PR 上实际跑的设计书例子):

- `ex1`:`response.sla` 设计书预期 **1**,代码实际是 **没有这个值(0 / 空)**

改稿(✅ 工厂验证通过;在 PR 分支上存成 `design.patch` 后 `git apply design.patch`):

```diff
--- a/design.yaml
+++ b/design.yaml
@@ -1 +1 @@
-300
+600
```

</details>

#### [`design.yaml:5`](https://github.com/o/af/blob/h/design.yaml#L5) health · 📖

**设计书该改,还是代码该改?**

- [ ] 是:设计书过时了(采用折叠里的改稿) <!-- docsp:fb design.yaml:5 design_stale outdated_wording read -->
- [x] 不是:代码改错了 <!-- docsp:fb design.yaml:5 code_wrong outdated_wording read -->
- [ ] 都不是:这里没有不一致(误报) <!-- docsp:fb design.yaml:5 not_a_problem outdated_wording read -->

<details><summary>证据和改稿</summary>

设计书 [`design.yaml:5`](https://github.com/o/af/blob/h/design.yaml#L5)

```yaml
  - health
```

这个 API 失败的设计书例子(实际跑过,但改稿没有改这条规则,不能证明是这条的问题):

- `ex1`:`response.sla` 设计书预期 **1**,代码实际是 **没有这个值(0 / 空)**

</details>

……另外还有 **1 处**没有在这里列出,完整列表见[任务摘要(job summary)](https://github.com/o/af/actions/runs/7)。

<details><summary>另外 2 条规则也核对过</summary>

- [`design.yaml:12`](https://github.com/o/af/blob/h/design.yaml#L12) name — ✅ 仍然成立
- [`design.yaml:14`](https://github.com/o/af/blob/h/design.yaml#L14) limit — ❔ 未判定

</details>

<sub>🔬 = 工厂在这个 PR 上实际跑了设计书的例子,确认不一致;📖 = AI 读代码的判断,可能是误报。勾选只用来统计这个检查的误报率。平台不会改任何文件。</sub>
