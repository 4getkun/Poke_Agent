# Windows ビルド toolchain

## 現在の選択と採用撤回

後続の機能試験で internal build は `sqlite3_open_v2` 中に access violation を起こした。
以下の旧決定は撤回し、そのhashを配布用として扱わない。
Go 1.27.0 の外部リンク `-s -w` ではSQLite/FTS初期化、埋め込みUI、認証APIと
ブラウザー描画まで進行した（`artifacts/logs/ui-fixture-external-run.log`）。
現在の開発ビルドは `-s -w -H=windowsgui` を使う。G1/G4完了の宣言ではない。

`scripts/build.ps1` は同じ生成exeを `--verify --data-dir <新規試験profile>` で
起動し、SQLite migration/FTSと埋め込みHTML読出しまで検証した後にhashを保存する。
ヘルプ表示だけを起動の合格条件にしない。WebView2/Codexは別結合試験で扱う。

古い外部GNU linkerとの組み合わせが原因候補だが、根本原因は未確定。
objdumpでは既定exeのSizeOfImageと最終debug section範囲の不整合を観測した。
新しいC compiler/linkerとの比較は未実施。stripだけで一般的な解決とは判断しない。
なお、圧縮無効の比較は下記旧記録に誤記があり、実際にはGo **1.27.0** で実施した。

## 旧決定（撤回済み）

Windows x64 の配布候補は Go internal linker を使う。標準 desktop build は
`dashboard/` で `go build -tags sqlite_fts5 -ldflags "-linkmode=internal -H=windowsgui" -o ../artifacts/Pokegents.exe .`
とし、`scripts/build.ps1` が依存導入、TypeScript、Windows用Vite、i18n検査、Go build、manifest生成を順に実行する。

## 比較と合格条件

親による Windows `ProcessStartInfo` 実測では、Go 1.27.0 / GCC 8.1.0 の既定リンク、Go 1.26.1 の既定リンク、および Go 1.26.1 の `-compressdwarf=false` は native `CreateProcess` で `invalid application` となった。Go 1.27.0 の `-linkmode=internal` では `Pokegents.exe --help` が exit 0 となったため internal link を採用する。比較コマンドとログの状態は以下に記録する。

比較コマンド（`dashboard/` で実行）は次のとおり。

- 既定: `go build -tags sqlite_fts5 -o ../artifacts/Pokegents.exe .`
- Go 1.26.1圧縮差: `go build -tags sqlite_fts5 -ldflags "-compressdwarf=false" -o ../artifacts/Pokegents.exe .`
- 採用: `go build -tags sqlite_fts5 -ldflags "-linkmode=internal -H=windowsgui" -o ../artifacts/Pokegents.exe .`

`build-uncompressed.log` と `build-internal.log` はこの確認時点で空ファイルのため、上記比較結果は親による実測記録である。

合格条件は、web検査と `go build` がすべて exit 0、生成した `Pokegents.exe` を .NET `ProcessStartInfo`（`UseShellExecute=false`、stdout/stderr redirect有効）で起動して `--help` が exit 0、manifestの実行ファイル SHA-256 が一致すること。`build.ps1` は各工程の出力を `artifacts/logs/` に記録し、Goは `Get-Command go.exe` を優先して既知の `C:\Program Files\Go\bin\go.exe` をfallbackに使う。

2026-10-20 の `scripts/build.ps1 -SkipInstall` 実測はTypeScript、Windows Vite、i18n、Go buildがすべてexit 0。`ProcessStartInfo` の `Pokegents.exe --help` もexit 0で、実行ファイルとmanifestのSHA-256は `cb4165adfd45faf502a8deec24574332b1abc35020024269c1dcf5ca183fbebf` で一致した。manifest記録は Go 1.27.0 / Node 24.16.0 / npm 12.0.2 / GCC 8.1.0。実行ログは `artifacts/logs/build-web-tsc.log`、`build-web-vite.log`、`build-i18n.log`、`desktop-build.log`、`desktop-help-smoke.log`。

## 未実施

WebView2の画面・終了ライフサイクル、実Codex会話、配布・installer、公開はこのビルド再現性確認に含めない。基準資料のdesktop/build比較ログは現在空ファイルであり、この文書のリンク結果は親の実測記録に基づく。
