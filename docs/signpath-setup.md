# SignPath Foundation preparation

Status: public-source preparation, not an approved subscription or a signed release.
Source: https://github.com/4getkun/Poke_Agent
Download page: https://github.com/4getkun/Poke_Agent/releases
Privacy: https://github.com/4getkun/Poke_Agent/blob/main/PRIVACY.md

Repository secret scanning and push protection are enabled. The
`signpath-release` environment accepts only the `main` branch; no API token or
invented service account configuration is stored yet. Maintainer MFA is a service
prerequisite to verify during account setup, not an assertion that it was checked.

## Signing boundary

The initial artifact configuration `portable-v1` signs only `Pokegents.exe` from
the `signpath-input` GitHub Actions artifact. Product name is `Pokegents`; file
and product version must match the requested numeric `version` plus `.0`.
Only GitHub-hosted runners may produce inputs for Foundation OSS signing.
The manually dispatched signing operation runs only from `main`, requires the
`signpath-release` environment and the maintainer's manual SignPath approval.
PRs cannot invoke signing or receive its secret. Signed outputs remain workflow
artifacts until independently reviewed; the workflow does not publish a release.

Setup previews are unsigned. Signing only the outer Inno Setup file does not sign
its embedded uninstaller or temporary copies. Their complete signing integration,
trusted install/update/rollback tests and certificate pin handling remain separate
work after service approval. The current direct-EXE updater is not bypassed.

## Service configuration after acceptance

1. The maintainer must complete the Foundation application, agree to the Code of
   Conduct/privacy processing, and use MFA on GitHub and SignPath. No password,
   MFA seed, API token or private key belongs in this repository.
2. Ask SignPath to review this Windows derivative of tRidha/pokegents and its
   upstream relationship. Do not claim upstream reputation as this project's
   own adoption. Bundled font assets use SIL OFL; dependency notices disclose
   individual licenses. Foundation decides sponsorship eligibility.
3. Link the public repository to GitHub.com as a trusted build system; configure
   origin verification and restrict signing to reviewed `main` builds. Install
   any requested GitHub App for **this repository only**, after reviewing access.
4. Import `distribution/signpath/portable-v1.xml`. Configure a release signing
   policy requiring manual approval by `4getkun`; do not invent a certificate or
   use a test certificate as production trust.
5. Set repository variables `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`,
   `SIGNPATH_SIGNING_POLICY_SLUG`, and `SIGNPATH_ARTIFACT_CONFIGURATION_SLUG`
   (`portable-v1`). Create the `signpath-release` environment, restrict it to
   `main`, and add the issued `SIGNPATH_API_TOKEN` as an environment secret.
6. Dispatch `Windows release preparation` from `main`, selecting `sign=true` and
   a numeric version. Approve the exact request in SignPath. Verify Windows trust,
   timestamp, product/version and artifact provenance before distributing it.

## Application text

Project name: Pokegents for Windows (Poke_Agent)

Tagline: A local Windows task board for supervising LLM agents with a visual
activity map and an opt-in MCP bridge.

Description: Pokegents for Windows is an experimental open-source Windows
derivative of tRidha/pokegents. It provides named agent cards, task and history
management, and an optional local MCP interface. It uses a local SQLite database
and Windows WebView2. Provider binaries and account credentials are not bundled.
Production agent execution remains disabled pending acceptance checks.

Reputation: This Windows derivative is newly public. It does not yet have
independently verified adoption or a history of signed releases. Its public
source, workflow runs, unsigned preview releases and third-party notices are
available for inspection. The upstream project's adoption is not represented
as this project's own. Please assess eligibility without assuming established
reputation. Acceptance is not guaranteed.

Build system: GitHub Actions, GitHub-hosted Windows runners.
Maintainer: Individual contributor, GitHub account `4getkun`.

The application form also requires the owner's first name, last name, email,
Code of Conduct agreement, personal-data processing consent and possibly a
CAPTCHA. These are left to the owner; no identity or contact details are guessed.
Optional marketing consent should remain unchecked unless the owner selects it.

## Official references

- https://signpath.org/terms
- https://signpath.org/apply
- https://docs.signpath.io/trusted-build-systems/github
- https://docs.signpath.io/artifact-configuration/syntax
