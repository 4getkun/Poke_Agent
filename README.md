# Pokegents for Windows

An experimental, local Windows task board for supervising LLM agents: a visual
activity map, named agent cards, task history, and an opt-in local MCP bridge.

This is a Windows derivative/fork of [tRidha/pokegents](https://github.com/tRidha/pokegents),
based on upstream commit `4a86179c917cfc308e755af34059c0d7810c941f`.
Board/MCP implementation also draws on Agent Board; see [third-party notices](THIRD_PARTY_NOTICES.md).
The original MIT copyright is preserved. This repository is maintained by
[4getkun](https://github.com/4getkun); it is not the upstream macOS release.

## Current status

This is an **unsigned development preview**, not a production-ready agent runner.
The production agent execution gate remains disabled while the remaining acceptance
checks are completed. Do not use a preview to supervise unattended production work.
MCP is disabled by default and requires an explicitly enabled local connection.
The app uses its own generic map/avatar presentation in distributable builds;
Pokémon sprites and the reference map are not included in this repository or its releases.

Download previews from [Releases](https://github.com/4getkun/Poke_Agent/releases).
Unsigned previews can trigger Windows warnings. No trusted code signature is claimed.

## Build on Windows x64

Use PowerShell 7, Go 1.27.0, Node.js 24, and an x64 MinGW GCC toolchain on PATH.
The desktop UI needs the Microsoft Edge WebView2 Runtime installed separately.

```powershell
$env:CGO_ENABLED = '1'
$env:CGO_CFLAGS = '-DSQLITE_ENABLE_FTS5'
$env:CGO_LDFLAGS = '-lm'
./scripts/build.ps1 -Version 0.1.2
./scripts/dependencies.ps1
```

The build checks TypeScript, UI assets, translations, and SQLite/FTS in the exact
executable. It writes outputs and logs under ignored `artifacts/`. Building a Setup
also requires Inno Setup: `./scripts/package.ps1 -Version 0.1.2 -IsccPath <absolute-ISCC.exe>`.
The compiler and dependency notices are build inputs; never copy a user profile
or credentials into the source tree or a release.

Tests after building the UI:

```powershell
Set-Location dashboard
go test -tags sqlite_fts5 -ldflags='-s -w' ./...
```

See [MCP setup](docs/mcp-agent-setup.md), [privacy](PRIVACY.md), and
[the dependency inventory](THIRD_PARTY_NOTICES.md). Provider integration and
physical Windows UI acceptance require additional local checks; unit tests do
not establish those results.

## Code signing policy

SignPath Foundation sponsorship is being prepared; **it has not been approved and
no SignPath-signed release exists yet**. If approved, the intended attribution is:
"Free code signing provided by [SignPath.io](https://signpath.io), certificate by
[SignPath Foundation](https://signpath.org)."

Committer, reviewer, and signing approver: [4getkun](https://github.com/4getkun).
Release signing requires the maintainer's explicit manual approval. The initial
SignPath configuration targets the portable application executable; it does not
claim that the Inno installer's embedded uninstaller is signed.
See [signing setup and application details](docs/signpath-setup.md).

## License

Project code is MIT, with upstream notices retained. Third-party source and fonts
retain their own licenses, including Apache-2.0, BSD, ISC and SIL OFL 1.1.
See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

