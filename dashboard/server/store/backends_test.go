package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBackendStoreMigratesLegacyModelBackendToProviderModel(t *testing.T) {
	dataDir := t.TempDir()
	legacy := map[string]any{
		"backends": map[string]any{
			"claude": map[string]any{"name": "Claude", "type": "claude-acp", "default": true},
			"custom-codex-model": map[string]any{
				"name": "Custom Codex Model",
				"type": "codex-acp",
				"env":  map[string]any{"CODEX_HOME": filepath.Join(dataDir, "codex-home")},
			},
		},
	}
	raw, _ := json.MarshalIndent(legacy, "", "  ")
	if err := os.WriteFile(filepath.Join(dataDir, "backends.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	bs := NewBackendStore(dataDir)
	if err := bs.EnsureReadableDefaults(); err != nil {
		t.Fatal(err)
	}
	backends := bs.List()
	if _, ok := backends["custom-codex-model"]; ok {
		t.Fatalf("legacy model backend should not remain top-level: %+v", backends)
	}
	codex, ok := backends["codex"]
	if !ok {
		t.Fatalf("codex provider missing: %+v", backends)
	}
	if codex.Type != "codex-acp" {
		t.Fatalf("codex type = %q, want codex-acp", codex.Type)
	}
	if codex.Env["CODEX_HOME"] == "" {
		t.Fatalf("legacy env was not preserved: %+v", codex.Env)
	}
	if _, ok := codex.Models["custom-codex-model"]; !ok {
		t.Fatalf("legacy backend was not converted into model option: %+v", codex.Models)
	}
	if codex.DefaultModel != "custom-codex-model" {
		t.Fatalf("codex default_model = %q, want custom-codex-model", codex.DefaultModel)
	}
	if got := codex.ResolvedModelLabel(); got != "Custom Codex Model" {
		t.Fatalf("ResolvedModelLabel = %q", got)
	}
	if got := bs.CanonicalID("custom-codex-model"); got != "codex" {
		t.Fatalf("CanonicalID = %q, want codex", got)
	}
	resolved, ok := bs.Get("custom-codex-model")
	if !ok || resolved.Type != "codex-acp" || resolved.DefaultModel != "custom-codex-model" {
		t.Fatalf("Get legacy model = %+v, %v", resolved, ok)
	}
}
