package server

import (
	"context"
	"os/exec"
	"strings"
)

type ProviderID string

const (
	ProviderClaude ProviderID = "claude"
	ProviderCodex  ProviderID = "codex"
)

// ProviderDefinition describes setup/discovery behavior for an agent provider.
// Launch details still live in the runtime/backend adapters; this registry is
// the setup/status abstraction so adding providers does not keep expanding
// setup.go with one-off fields.
type ProviderDefinition struct {
	ID            ProviderID
	DisplayName   string
	BackendTypes  []string
	CLICommands   []string
	ConfigTargets []string
	DetectCLI     func(context.Context) checkStatus
	DetectAuth    func(context.Context) checkStatus
}

func providerDefinitions(paths PathService) []ProviderDefinition {
	return []ProviderDefinition{
		{
			ID:            ProviderClaude,
			DisplayName:   "Claude",
			BackendTypes:  []string{"claude", "claude-acp"},
			CLICommands:   []string{"claude"},
			ConfigTargets: []string{paths.ClaudeSettingsPath()},
			DetectCLI: func(_ context.Context) checkStatus {
				if path, err := exec.LookPath("claude"); err == nil {
					return checkStatus{State: "ok", Path: path}
				}
				return checkStatus{State: "missing", Message: "claude CLI not found on PATH"}
			},
			DetectAuth: detectClaudeAuth,
		},
		{
			ID:           ProviderCodex,
			DisplayName:  "Codex",
			BackendTypes: []string{"codex", "codex-acp"},
			CLICommands:  []string{"codex"},
			DetectCLI: func(_ context.Context) checkStatus {
				return detectCodexBackend()
			},
			DetectAuth: func(_ context.Context) checkStatus {
				return checkStatus{State: "unknown", Message: "Codex auth is managed by the Codex CLI"}
			},
		},
	}
}

func providerByID(paths PathService, id string) (ProviderDefinition, bool) {
	for _, p := range providerDefinitions(paths) {
		if string(p.ID) == id {
			return p, true
		}
	}
	return ProviderDefinition{}, false
}

func providerFromBackendKey(backendKey string) string {
	b := strings.ToLower(strings.TrimSpace(backendKey))
	switch {
	case b == "", b == "claude", b == "claude-acp":
		return string(ProviderClaude)
	case strings.Contains(b, "codex"), strings.Contains(b, "gpt"):
		return string(ProviderCodex)
	default:
		return b
	}
}

func providerFromBackendType(backendKey string, backendType string) string {
	if p := providerFromBackendKey(backendType); p == string(ProviderClaude) || p == string(ProviderCodex) {
		return p
	}
	return providerFromBackendKey(backendKey)
}

func providerFromTranscriptPath(path string) string {
	if path == "" {
		return ""
	}
	if isNonClaudeTranscriptPath(path) {
		return string(ProviderCodex)
	}
	return string(ProviderClaude)
}
