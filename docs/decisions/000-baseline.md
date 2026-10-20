# WP-00: 固定ベースラインと移植境界

2026-10-20。仕様書 1.0 に従い、`reference/pokegents` を
`4a86179c917cfc308e755af34059c0d7810c941f`、`reference/Agent_Board` を
`9c0ef0f3f7c35f0384bfe9c2890a78008e9aaef6` に固定した。
参照cloneは未改変のまま保持し、実装は `dashboard/`、証拠は `artifacts/logs/` に分離する。
探索用 `C:\Github\pokegents-windows` は使用しない。

## 確認結果

- Windows x64、Go 1.27.0、Node 24.16.0、GCC 8.1.0。
- 元ソース `go test ./... -count=1` は `shellQuote` と `syscall.Kill` の未定義で失敗。
  core/services/store のテストは成功。ログ: `artifacts/logs/baseline-go.log`。
- `npm.cmd ci` と `npm.cmd run build` は成功。依存監査とchunk警告は別途評価する。
- 実際に存在するネイティブ Codex は 0.163.0。`login status` は ChatGPT ログイン済み。
  これは実会話・sandbox・Job共存試験の合格を意味しない。
- CLI自身の `app-server generate-json-schema --out ...` で版別schemaを保存した。

## 変更対象と検証

- 既存serverのWindowsコンパイル阻害はOSアダプターへ狭く分離し、既存テストを再実行する。
- desktop起動では従来の任意外部起動/ACP/JSON書き込みrouteを登録しない。
  既存コードを参考・再利用しても、その危険な既定値を公開しない。
- 既存React/Vite、Markdown、入力部品、Go core型と適合する処理を再利用する。
  Windows用の契約・正規化イベントはACP wire型と分ける。
- プロセス生成、API認証、DB移行と最終レビューは親エージェントが担当する。
- 新規コードは状態遷移・競合・承認・停止・回収の異常系から確認する。
- G1/G2が未通過の機能は能力情報で無効化し、実機証拠なしに対応と表示しない。

## 配布境界

上流MIT noticeを保持する。ACP forkとPokémon画像の権利をコードのMITと混同しない。
Windows用ビルドには未許可画像・外部フォントを含めない。公開・push・releaseは本依頼に含めない。
G0の所在・ライセンス対応表は `../verification/source-inventory.md` を併用する。
