<!-- docsp:pr -->
<!-- docsp:sig 6aed6ba34a845ded -->
**docs-platform**:この PR のコード変更で **3 箇所のドキュメント**がコードと合わなくなりました。
**docs-platform**:**1 箇所のドキュメント**について、根拠となるコード同士が食い違っています(例:2 箇所で異なるデフォルト値)。どちらか一方とは比較していません。

> ⚠️ **未完了:問題がないという意味ではありません。** このチェックは最後まで実行されませんでした。以下がすべてではありません:
> - 実行の AI 予算が尽き、影響を受けたドキュメント行の一部が未判定です
> - AI 判定が一部の行で失敗しました(エラー・タイムアウト・拒否)

#### [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3) · コードと矛盾 · 📖

retention is 14d in cfg.yaml

**ドキュメントを直すべきですか、それともコードの誤りですか?**

- [x] ドキュメントが古い:ドキュメントを直します <!-- docsp:fb README.md:3 design_stale contradicted read -->
- [ ] コードの誤り:コードを直します <!-- docsp:fb README.md:3 code_wrong contradicted read -->
- [ ] どちらでもない:不一致はない(誤検知) <!-- docsp:fb README.md:3 not_a_problem contradicted read -->

<details><summary>根拠</summary>

ドキュメント [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3)

```markdown
Retention is 7 days.
```

コード [`cfg.yaml:2`](https://github.com/o/demo/blob/abc123/cfg.yaml#L2)

```yaml
retention: 14d
```

- retention is 14d in cfg.yaml — contradicted

</details>

#### [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9) · 列挙が不完全 · 📖

**ドキュメントを直すべきですか、それともコードの誤りですか?**

- [ ] ドキュメントが古い:ドキュメントを直します <!-- docsp:fb README.md:9 design_stale incomplete_list read -->
- [ ] コードの誤り:コードを直します <!-- docsp:fb README.md:9 code_wrong incomplete_list read -->
- [ ] どちらでもない:不一致はない(誤検知) <!-- docsp:fb README.md:9 not_a_problem incomplete_list read -->

<details><summary>根拠</summary>

ドキュメント [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9)

```markdown
Jobs: build, test.
```


</details>

#### [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4) · 参照先が存在しない · 📖

**ドキュメントを直すべきですか、それともコードの誤りですか?**

- [ ] ドキュメントが古い:ドキュメントを直します <!-- docsp:fb docs/run.md:4 design_stale dangling_reference read -->
- [ ] コードの誤り:コードを直します <!-- docsp:fb docs/run.md:4 code_wrong dangling_reference read -->
- [ ] どちらでもない:不一致はない(誤検知) <!-- docsp:fb docs/run.md:4 not_a_problem dangling_reference read -->

<details><summary>根拠</summary>

ドキュメント [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4)

```markdown
See `old.md`.
```


</details>

…ほか **1 件**はここに載せていません。全件は[ジョブサマリー](https://github.com/o/demo/actions/runs/1)にあります。

<sub>📖 = AI がコードを読んだ判断で、誤検知の可能性があります。チェックは誤検知率の集計にだけ使います。プラットフォームはファイルを一切変更しません。</sub>
