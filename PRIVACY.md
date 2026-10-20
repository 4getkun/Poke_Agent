# Privacy

Pokegents for Windows stores its task database, imported task history, diagnostic
information and WebView2 profile locally in the selected application profile
(normally `%LOCALAPPDATA%\Pokegents`). These files are not part of releases.

The application does not include analytics or automatically upload task content.
History import reads sessions explicitly selected by the user. The optional MCP
bridge uses stdio and a token-protected numeric-loopback gateway, is disabled by
default, and may expose the enabled tool scope to the LLM client the user selects.
The connection file contains credentials: do not publish or paste its contents.

When a user requests an agent operation through an external provider/client,
that provider's authentication, network access and privacy terms apply. This
project does not bundle a Codex account, API credentials or the provider binary.
Codex provider policy: https://openai.com/policies/privacy-policy/

The app requires the separately installed Microsoft Edge WebView2 Runtime.
Its runtime servicing and diagnostic behavior are governed by Microsoft:
https://privacy.microsoft.com/privacystatement

The optional direct-EXE update CLI contacts only the HTTPS manifest source the
operator explicitly supplies. It does not automatically check or install updates.
The current unsigned preview cannot pass the trusted signed-update gate.

Source, GitHub workflow logs, and release artifacts are public. Do not include
private prompts, project files, home paths, provider histories, credentials, or
unredacted logs in issues, pull requests, or build artifacts.
