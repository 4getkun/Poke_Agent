# Third-party notices inventory

This file records upstream attribution, dependency licenses and the release boundary.
Generated inventories accompany each preview; this is not a promise of SignPath eligibility.

## Fixed reference snapshots

- This Windows derivative is based on [tRidha/pokegents](https://github.com/tRidha/pokegents/tree/4a86179c917cfc308e755af34059c0d7810c941f), commit `4a86179c917cfc308e755af34059c0d7810c941f`. Its MIT copyright (2026 Thariq Ridha) and license are preserved in the root `LICENSE`.
- Agent Board reference snapshot: `9c0ef0f3f7c35f0384bfe9c2890a78008e9aaef6`, MIT, Copyright (c) 2026 Agent Board Contributors. Its required license notice is reproduced below. The local reference checkout is not published.
- The ACP fork's Apache-2.0 license text is present at `dashboard/acp-fork/LICENSE`.

## Bundled fonts

- `dashboard/web/src/assets/fonts/mplus1p/MPLUS1p-Bold.woff2` is M PLUS 1p Bold (700), converted from the Google Fonts TrueType source at commit `47d54ca1ebb14fd864695f1d39dc734f39017c33` using fontTools. Source URLs, original and generated hashes, conversion details, and glyph checks are recorded in `dashboard/web/src/assets/fonts/mplus1p/source.json`.
- Copyright 2016 The M+ Project Authors. Licensed under the SIL Open Font License, Version 1.1. The complete upstream copyright and license text is preserved in `dashboard/web/src/assets/fonts/mplus1p/OFL.txt` and is accessible in the application's diagnostics panel.

- `dashboard/web/src/assets/fonts/zenkakugothicnew/ZenKakuGothicNew-Regular.woff2` and `ZenKakuGothicNew-Bold.woff2` are Zen Kaku Gothic New Regular (400) and Bold (700), converted from the upstream TrueType sources at commit `2705757e17e42954f3acbdf921ac0ae24d1270cd` using fontTools. Source URLs, hashes, conversion details, and glyph checks are recorded in `dashboard/web/src/assets/fonts/zenkakugothicnew/source.json`.
- Copyright 2022 The Zen Kaku Gothic Project Authors (https://github.com/googlefonts/zen-kakugothic). Licensed under the SIL Open Font License, Version 1.1. The complete upstream copyright and license text is preserved in `dashboard/web/src/assets/fonts/zenkakugothicnew/OFL.txt` and is accessible in the application's diagnostics panel.

- `dashboard/web/src/assets/fonts/notosansjp/NotoSansJP-Regular.woff2` and `NotoSansJP-Bold.woff2` are Noto Sans JP Regular (400) and Bold (700), instantiated from the Google Fonts variable TrueType source using fontTools. Source commit, URLs, hashes, conversion details, and glyph checks are recorded in `dashboard/web/src/assets/fonts/notosansjp/source.json`.
- Copyright 2014-2021 Adobe (http://www.adobe.com/), with Reserved Font Name 'Source'. Licensed under the SIL Open Font License, Version 1.1. The complete upstream copyright and license text is preserved in `dashboard/web/src/assets/fonts/notosansjp/OFL.txt` and is accessible in the application's diagnostics panel.

## Retained reference font

- The PKMN source assets below are retained for provenance. They are not referenced by the current Windows UI entry point or bundled into its build.
- `dashboard/web/src/assets/fonts/pkmn/pkmn_r.ttf` is PKMN Regular from `https://github.com/nue-of-k/pkmn` at commit `0719c3d73d0fcb35a7d14a06b7de73bc1784d8e0`; it was resaved with fontTools to recalculate table checksums and converted to `dashboard/web/src/assets/fonts/pkmn/pkmn_r.woff2` for browser delivery. Source and generated-file hashes are recorded in `dashboard/web/src/assets/fonts/pkmn/source.json`.
- The upstream license is MIT. The complete upstream notice is preserved in `dashboard/web/src/assets/fonts/pkmn/LICENSE` and reproduced here:

```text
MIT License

Copyright © 2009-2015 鵺 (Nue)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Distribution observations

- The local MCP adapter uses `github.com/modelcontextprotocol/go-sdk v1.8.0`. Its LICENSE describes an MIT-to-Apache-2.0 transition, with MIT retained for contributions lacking relicensing consent and CC-BY-4.0 for non-specification documentation. Preserve the complete upstream LICENSE rather than reducing this to a single SPDX label. The fixed module and transitive dependencies are recorded in `dashboard/go.mod` / `dashboard/go.sum`; their full notices accompany builds.

- The Windows Vite configuration sets `publicDir: false` in `dashboard/web/vite.windows.config.ts`; this configuration does not copy a Vite `public` directory into the distribution build. The build does not fetch sprite images through that public directory.
- `artifacts/Pokegents.exe` is accompanied by Go build-info and dependency inventories generated by `scripts/dependencies.ps1`. All listed Go modules are downloaded before collecting their licenses. Missing module licenses fail the inventory. Available npm license/notice files and Go module notices are copied under `artifacts/notices/`; inventory identifiers are relative, without local home paths.
- No standardized Go SBOM is generated here. `artifacts/go-module-inventory.json` is the direct `go list -m -json all` module inventory, and `artifacts/go-build-info.txt` records `go version -m` output.

## Windows runtime and build tools

- `github.com/wailsapp/go-webview2 v1.0.23` is MIT; its default Go loader has its own ISC notice (`webviewloader/LICENSE`, John Chadwick and Wails Project Developers). The release build uses `sqlite_fts5` and does **not** enable `native_webview2loader`, so Microsoft's proprietary WebView2Loader.dll is not embedded. The separately installed WebView2 Runtime is an OS runtime prerequisite, not a redistributed component of these previews.
- `github.com/mattn/go-sqlite3` is MIT and incorporates SQLite public-domain source; preserve its full license and source notices. Other Go dependency licenses are included in the generated notices.
- The frontend dependency inventory contains MIT, ISC, Apache-2.0, BSD-3-Clause and CC-BY-4.0 entries; `caniuse-lite` is CC-BY-4.0 development data. It is included in the development SBOM, not presented as project-authored MIT code.
- PE version resources are generated with `github.com/josephspurrier/goversioninfo v1.7.0` (MIT), a build tool, and its resource generator dependencies. Its source: https://github.com/josephspurrier/goversioninfo/tree/v1.7.0 . Build tools are not copied into the release.
- Inno Setup produces the optional installer. Its compiler is not redistributed. GitHub Actions records the hosted runner, source commit and tool versions; the workflow uses existing hosted-runner Inno Setup and x64 GCC. The generated installer and application are unsigned until a separately approved signing operation succeeds.
- Legacy ACP source is Apache-2.0 and retains its own LICENSE. Its optional third-party provider SDK is not installed or bundled by the Windows build. Provider executables and account credentials are not redistributed.

## Asset boundary

Pokémon sprites and the reference `town.png` have not been granted a redistribution
license by this project. They are excluded from the public source export and
Windows builds. Upstream's MIT code license is not treated as a license for those
images. Current Windows map/avatar visuals are generated by project code.

## Remaining review boundary

- Foundation sponsorship and its treatment of a newly public Windows derivative,
  mixed upstream contribution licensing and SIL OFL font assets require its review.
- Any future dependency, asset or native-loader build-tag change needs a fresh
  inventory. The current report does not authorize adding proprietary runtime files.

## Agent Board MIT notice

MIT License

Copyright (c) 2026 Agent Board Contributors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
