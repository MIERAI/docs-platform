### docs-platform: ドキュメントの不一致 3 件、コード内の食い違い 1 件(判定 6 行)

> ⚠️ **未完了:問題がないという意味ではありません。** このチェックは最後まで実行されませんでした。以下がすべてではありません:
> - 実行の AI 予算が尽き、影響を受けたドキュメント行の一部が未判定です
> - AI 判定が一部の行で失敗しました(エラー・タイムアウト・拒否)

| 位置 | 分類 | ドキュメント |
|---|---|---|
| [`README.md:3`](https://github.com/o/demo/blob/abc123/README.md#L3) | コードと矛盾 | Retention is 7 days. |
| [`README.md:9`](https://github.com/o/demo/blob/abc123/README.md#L9) | 列挙が不完全 | Jobs: build, test. |
| [`docs/run.md:4`](https://github.com/o/demo/blob/abc123/docs/run.md#L4) | 参照先が存在しない | See `old.md`. |
| [`docs/run.md:7`](https://github.com/o/demo/blob/abc123/docs/run.md#L7) | コード内で食い違い | The default timeout is 30s. |
