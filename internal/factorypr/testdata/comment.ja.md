<!-- docsp:factory-pr -->
<!-- docsp:sig 9f88b8c05330beb5 -->
**docs-platform**:この PR で **3 件の設計書ルール**がコードと一致しなくなりました。

> ⚠️ **未完了:問題がないという意味ではありません。** このチェックは最後まで実行されませんでした。以下がすべてではありません:
> - AI 判定が一部の行で失敗しました(エラー・タイムアウト・拒否)

#### [`design.yaml:3`](https://github.com/o/af/blob/h/design.yaml#L3) SLA 300 s · 🔬

the design says 300 s, the code 600 s

**設計書を直すべきですか、それともコードですか?**

- [ ] はい:設計書が古い(折りたたみ内の修正案を採用) <!-- docsp:fb design.yaml:3 design_stale contradicted executed -->
- [ ] いいえ:コードの誤り <!-- docsp:fb design.yaml:3 code_wrong contradicted executed -->

<details><summary>根拠と修正案</summary>

設計書 [`design.yaml:3`](https://github.com/o/af/blob/h/design.yaml#L3)

```yaml
  - SLA 300 s
```

反例(ファクトリーがこの PR で実際に実行した設計書の例):

- `ex1`:`response.sla` 設計書の期待値は **1**、コードの実際は **値なし(0 / 空)**

修正案(✅ ファクトリーで検証済み。PR ブランチで `design.patch` として保存し `git apply design.patch`):

```diff
--- a/design.yaml
+++ b/design.yaml
@@ -1 +1 @@
-300
+600
```

</details>

#### [`design.yaml:5`](https://github.com/o/af/blob/h/design.yaml#L5) health · 📖

**設計書を直すべきですか、それともコードですか?**

- [ ] はい:設計書が古い(折りたたみ内の修正案を採用) <!-- docsp:fb design.yaml:5 design_stale outdated_wording read -->
- [x] いいえ:コードの誤り <!-- docsp:fb design.yaml:5 code_wrong outdated_wording read -->
- [ ] どちらでもない:不一致はない(誤検知) <!-- docsp:fb design.yaml:5 not_a_problem outdated_wording read -->

<details><summary>根拠と修正案</summary>

設計書 [`design.yaml:5`](https://github.com/o/af/blob/h/design.yaml#L5)

```yaml
  - health
```

この API で失敗する設計書の例(実行済み。ただし修正案はこのルールを変えていないため、このルールの問題だとは証明できません):

- `ex1`:`response.sla` 設計書の期待値は **1**、コードの実際は **値なし(0 / 空)**

</details>

…ほか **1 件**はここに載せていません。全件は[ジョブサマリー](https://github.com/o/af/actions/runs/7)にあります。

<details><summary>ほかに 2 件のルールも確認済み</summary>

- [`design.yaml:12`](https://github.com/o/af/blob/h/design.yaml#L12) name — ✅ 引き続き成立
- [`design.yaml:14`](https://github.com/o/af/blob/h/design.yaml#L14) limit — ❔ 未判定

</details>

<sub>🔬 = ファクトリーがこの PR で設計書の例を実際に実行し不一致を確認;📖 = AI がコードを読んだ判断で、誤検知の可能性があります。チェックは誤検知率の集計にだけ使います。プラットフォームはファイルを一切変更しません。</sub>
