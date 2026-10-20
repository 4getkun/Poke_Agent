package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BackendModelConfig describes a model option within a provider backend.
// Per-model Env entries merge on top of the backend-level Env when launching
// an agent with this model — lets different models use different endpoints
// or API keys within the same provider (e.g. GPT-5.5 on Azure East,
// GPT-4o on Azure West).
type BackendModelConfig struct {
	Name   string            `json:"name,omitempty"`
	Model  string            `json:"model"`
	Effort string            `json:"effort,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// BackendConfig describes an ACP provider backend that can launch agents.
// Models live under the backend because "Codex" / "Claude" are providers;
// concrete model IDs (or aliases) are launch/runtime choices within them.
type BackendConfig struct {
	Name         string                        `json:"name"`
	Type         string                        `json:"type"`
	DefaultModel string                        `json:"default_model,omitempty"`
	Models       map[string]BackendModelConfig `json:"models,omitempty"`
	Default      bool                          `json:"default,omitempty"`
	Env          map[string]string             `json:"env,omitempty"`

	// Deprecated v1 fields. Read for compatibility, rewritten into
	// default_model/models by EnsureReadableDefaults/saveLocked.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

func (b BackendConfig) ResolvedModel() string {
	if strings.TrimSpace(b.Model) != "" {
		return strings.TrimSpace(b.Model)
	}
	if b.DefaultModel != "" && b.Models != nil {
		if m, ok := b.Models[b.DefaultModel]; ok {
			return strings.TrimSpace(m.Model)
		}
	}
	return ""
}

func (b BackendConfig) ResolvedEffort() string {
	if strings.TrimSpace(b.Effort) != "" {
		return strings.TrimSpace(b.Effort)
	}
	if b.DefaultModel != "" && b.Models != nil {
		if m, ok := b.Models[b.DefaultModel]; ok {
			return strings.TrimSpace(m.Effort)
		}
	}
	return ""
}

func (b BackendConfig) ResolvedModelLabel() string {
	if b.DefaultModel != "" && b.Models != nil {
		if m, ok := b.Models[b.DefaultModel]; ok {
			if strings.TrimSpace(m.Name) != "" {
				return strings.TrimSpace(m.Name)
			}
			if strings.TrimSpace(m.Model) != "" {
				return strings.TrimSpace(m.Model)
			}
		}
	}
	if model := b.ResolvedModel(); model != "" {
		return model
	}
	return strings.TrimSpace(b.DefaultModel)
}

// ResolvedEnvForModel returns the merged env for a model identified by its
// model ID string (e.g. "gpt-5.5"): backend-level env as the base, with
// per-model env overrides on top. Returns backend env unchanged if the model
// has no env overrides or isn't found.
func (b BackendConfig) ResolvedEnvForModel(modelID string) map[string]string {
	if modelID == "" || b.Models == nil {
		return b.Env
	}
	for _, m := range b.Models {
		if m.Model == modelID && len(m.Env) > 0 {
			merged := make(map[string]string, len(b.Env)+len(m.Env))
			for k, v := range b.Env {
				merged[k] = v
			}
			for k, v := range m.Env {
				merged[k] = v
			}
			return merged
		}
	}
	return b.Env
}

// backendsFile is the JSON shape of ~/.pokegents/backends.json.
type backendsFile struct {
	Version      int                      `json:"version,omitempty"`
	Instructions []string                 `json:"instructions,omitempty"`
	Backends     map[string]BackendConfig `json:"backends"`
}

// BackendStore reads/writes ~/.pokegents/backends.json.
type BackendStore struct {
	mu   sync.RWMutex
	path string
}

// NewBackendStore creates a BackendStore for the given data directory.
// If the file doesn't exist, it creates one with provider-level defaults.
func NewBackendStore(dataDir string) *BackendStore {
	s := &BackendStore{path: filepath.Join(dataDir, "backends.json")}
	s.ensureDefault()
	return s
}

func defaultBackends() map[string]BackendConfig {
	return map[string]BackendConfig{
		"claude": {
			Name:         "Claude",
			Type:         "claude-acp",
			Default:      true,
			DefaultModel: "sonnet-4-6",
			Models: map[string]BackendModelConfig{
				"sonnet-4-6": {Name: "Sonnet 4.6", Model: "claude-sonnet-4-6"},
				"opus-4-7":   {Name: "Opus 4.7", Model: "claude-opus-4-7"},
				"opus-4-6":   {Name: "Opus 4.6 (1M)", Model: "claude-opus-4-6[1m]"},
				"haiku-4-5":  {Name: "Haiku 4.5", Model: "haiku"},
			},
		},
		"codex": {
			Name: "Codex",
			Type: "codex-acp",
			Models: map[string]BackendModelConfig{
				"default": {Name: "Provider default", Model: ""},
			},
			DefaultModel: "default",
		},
	}
}

// ensureDefault creates a default backends.json if it doesn't already exist.
func (s *BackendStore) ensureDefault() {
	if _, err := os.Stat(s.path); err == nil {
		return
	}
	defaultFile := backendsFile{
		Version:      2,
		Instructions: defaultBackendInstructions(),
		Backends:     defaultBackends(),
	}
	data, _ := json.MarshalIndent(defaultFile, "", "  ")
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, data, 0o644)
}

// load reads and parses the backends file.
func (s *BackendStore) load() (map[string]BackendConfig, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	var f backendsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Backends == nil {
		f.Backends = make(map[string]BackendConfig)
	}
	return f.Backends, nil
}

// List returns all provider backends keyed by ID.
func (s *BackendStore) List() map[string]BackendConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	backends, err := s.load()
	if err != nil {
		return defaultBackends()
	}
	return normalizeBackends(backends)
}

// Save replaces the full backend map, normalizing defaults and writing the
// file atomically.
func (s *BackendStore) Save(backends map[string]BackendConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(backends)
}

func (s *BackendStore) saveLocked(backends map[string]BackendConfig) error {
	backends = normalizeBackends(backends)
	ensureBackendDefault(backends)
	f := backendsFile{Version: 2, Instructions: defaultBackendInstructions(), Backends: backends}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func defaultBackendInstructions() []string {
	return []string{
		"Backends are providers/runtimes, not individual models.",
		"Use backend ids like claude and codex. Put model choices under backends.<id>.models.",
		"type selects the launcher: claude-acp, codex, or codex-acp.",
		"default_model selects one key from models. Each model entry can set model and optional effort.",
		"env belongs to the provider backend process, for auth or provider-specific endpoints.",
	}
}

// Upsert creates or updates a backend. If cfg.Default is true, all other
// backends are demoted.
func (s *BackendStore) Upsert(id string, cfg BackendConfig) error {
	if id == "" {
		return fmt.Errorf("backend id is required")
	}
	if cfg.Name == "" {
		cfg.Name = id
	}
	if cfg.Type == "" {
		cfg.Type = id
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backends, err := s.load()
	if err != nil {
		backends = map[string]BackendConfig{}
	}
	if cfg.Default {
		for k, b := range backends {
			b.Default = false
			backends[k] = b
		}
	}
	backends[id] = cfg
	return s.saveLocked(backends)
}

// EnsureReadableDefaults upgrades an existing minimal/v1 backends.json into the
// provider→models shape. It preserves user env and user-named legacy entries by
// converting old model-as-backend records into model options under the provider.
func (s *BackendStore) EnsureReadableDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backends, err := s.load()
	if err != nil {
		backends = map[string]BackendConfig{}
	}
	return s.saveLocked(backends)
}

// Delete removes a backend. The built-in "claude" backend cannot be deleted
// because launches fall back to it when no default is present.
func (s *BackendStore) Delete(id string) error {
	if id == "" {
		return fmt.Errorf("backend id is required")
	}
	if id == "claude" {
		return fmt.Errorf("cannot delete built-in claude backend")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backends, err := s.load()
	if err != nil {
		return err
	}
	backends = normalizeBackends(backends)
	if _, ok := backends[id]; !ok {
		return fmt.Errorf("backend %q not found", id)
	}
	delete(backends, id)
	return s.saveLocked(backends)
}

// SetDefault marks exactly one backend as default.
func (s *BackendStore) SetDefault(id string) error {
	if id == "" {
		return fmt.Errorf("backend id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	backends, err := s.load()
	if err != nil {
		return err
	}
	backends = normalizeBackends(backends)
	if _, ok := backends[id]; !ok {
		return fmt.Errorf("backend %q not found", id)
	}
	for k, b := range backends {
		b.Default = k == id
		backends[k] = b
	}
	return s.saveLocked(backends)
}

// Get returns a provider backend by ID. For compatibility, legacy model-as-
// backend IDs are resolved to their parent provider with DefaultModel set to
// that legacy ID.
func (s *BackendStore) Get(id string) (*BackendConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	backends, err := s.load()
	if err != nil {
		return nil, false
	}
	backends = normalizeBackends(backends)
	if b, ok := backends[id]; ok {
		return &b, true
	}
	for _, b := range backends {
		if _, ok := b.Models[id]; ok {
			b.DefaultModel = id
			return &b, true
		}
	}
	return nil, false
}

// DefaultID returns the ID of the backend marked as default.
// Falls back to "claude" if none is marked.
func (s *BackendStore) DefaultID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	backends, err := s.load()
	if err != nil {
		return "claude"
	}
	backends = normalizeBackends(backends)
	for id, b := range backends {
		if b.Default {
			return id
		}
	}
	return "claude"
}

func (s *BackendStore) CanonicalID(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	backends, err := s.load()
	if err != nil {
		return id
	}
	backends = normalizeBackends(backends)
	if _, ok := backends[id]; ok {
		return id
	}
	for providerID, b := range backends {
		if _, ok := b.Models[id]; ok {
			return providerID
		}
	}
	return id
}

func normalizeBackends(input map[string]BackendConfig) map[string]BackendConfig {
	if len(input) == 0 {
		return defaultBackends()
	}
	out := map[string]BackendConfig{}
	for id, b := range defaultBackends() {
		out[id] = b
	}

	for id, b := range input {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if b.Name == "" {
			b.Name = id
		}
		if b.Type == "" {
			b.Type = id
		}
		if b.Models == nil {
			b.Models = map[string]BackendModelConfig{}
		}
		if b.Model != "" || b.Effort != "" {
			key := b.DefaultModel
			if key == "" {
				key = "default"
			}
			m := b.Models[key]
			if m.Name == "" {
				m.Name = "Default"
			}
			if m.Model == "" {
				m.Model = b.Model
			}
			if m.Effort == "" {
				m.Effort = b.Effort
			}
			b.Models[key] = m
			b.DefaultModel = key
			b.Model = ""
			b.Effort = ""
		}

		provider := providerForBackend(id, b)
		if provider == id {
			out[id] = mergeBackend(out[id], b)
			continue
		}

		parent := out[provider]
		if b.Env != nil && parent.Env == nil {
			parent.Env = b.Env
		}
		if b.Default {
			parent.Default = true
		}
		if parent.Models == nil {
			parent.Models = map[string]BackendModelConfig{}
		}
		model := BackendModelConfig{Name: b.Name}
		if b.DefaultModel != "" {
			if m, ok := b.Models[b.DefaultModel]; ok {
				model = m
				if model.Name == "" {
					model.Name = b.Name
				}
			}
		}
		parent.Models[id] = model
		if parent.DefaultModel == "" || parent.DefaultModel == "default" || b.Default {
			parent.DefaultModel = id
		}
		out[provider] = parent
	}
	ensureBackendDefault(out)
	return out
}

func mergeBackend(base, override BackendConfig) BackendConfig {
	if override.Name != "" {
		base.Name = override.Name
	}
	if override.Type != "" {
		base.Type = override.Type
	}
	if override.DefaultModel != "" {
		base.DefaultModel = override.DefaultModel
	}
	if override.Default {
		base.Default = true
	}
	if override.Env != nil {
		base.Env = override.Env
	}
	if base.Models == nil {
		base.Models = map[string]BackendModelConfig{}
	}
	for k, v := range override.Models {
		base.Models[k] = v
	}
	return base
}

func providerForBackend(id string, b BackendConfig) string {
	haystack := strings.ToLower(id + " " + b.Name + " " + b.Type)
	if strings.Contains(haystack, "codex") || strings.Contains(haystack, "openai") || strings.Contains(haystack, "gpt") {
		if id == "codex" {
			return id
		}
		return "codex"
	}
	if strings.Contains(haystack, "claude") || strings.Contains(haystack, "anthropic") {
		if id == "claude" {
			return id
		}
		return "claude"
	}
	return id
}

func ensureBackendDefault(backends map[string]BackendConfig) {
	if len(backends) == 0 {
		for k, v := range defaultBackends() {
			backends[k] = v
		}
		return
	}
	defaultSeen := false
	for id, b := range backends {
		if b.Name == "" {
			b.Name = id
		}
		if b.Type == "" {
			b.Type = id
		}
		if b.Default {
			if defaultSeen {
				b.Default = false
			} else {
				defaultSeen = true
			}
		}
		backends[id] = b
	}
	if !defaultSeen {
		if b, ok := backends["claude"]; ok {
			b.Default = true
			backends["claude"] = b
			return
		}
		for id, b := range backends {
			b.Default = true
			backends[id] = b
			return
		}
	}
}
