package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"pokegents/dashboard/server/store"
)

var backendIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type backendResponse struct {
	ID           string                              `json:"id"`
	Name         string                              `json:"name"`
	Type         string                              `json:"type"`
	DefaultModel string                              `json:"default_model,omitempty"`
	Models       map[string]store.BackendModelConfig `json:"models,omitempty"`
	Default      bool                                `json:"default,omitempty"`
	Env          map[string]string                   `json:"env,omitempty"`
}

func backendResponseFromConfig(id string, b store.BackendConfig, includeEnv bool) backendResponse {
	resp := backendResponse{ID: id, Name: b.Name, Type: b.Type, DefaultModel: b.DefaultModel, Models: b.Models, Default: b.Default}
	if includeEnv && len(b.Env) > 0 {
		resp.Env = b.Env
	}
	return resp
}

func sortedBackendResponses(backends map[string]store.BackendConfig, includeEnv bool) []backendResponse {
	ids := make([]string, 0, len(backends))
	for id := range backends {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]backendResponse, 0, len(ids))
	for _, id := range ids {
		out = append(out, backendResponseFromConfig(id, backends[id], includeEnv))
	}
	return out
}

func (s *Server) handleUpsertBackend(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		ID           string                              `json:"id"`
		Name         string                              `json:"name"`
		Type         string                              `json:"type"`
		DefaultModel string                              `json:"default_model"`
		Models       map[string]store.BackendModelConfig `json:"models"`
		Default      bool                                `json:"default"`
		Env          map[string]string                   `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if id == "" {
		id = strings.TrimSpace(body.ID)
	}
	if !backendIDPattern.MatchString(id) {
		http.Error(w, "invalid backend id", http.StatusBadRequest)
		return
	}
	cfg := store.BackendConfig{
		Name:         strings.TrimSpace(body.Name),
		Type:         strings.TrimSpace(body.Type),
		DefaultModel: strings.TrimSpace(body.DefaultModel),
		Models:       body.Models,
		Default:      body.Default,
		Env:          body.Env,
	}
	if err := s.backendStore.Upsert(id, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if b, ok := s.backendStore.Get(id); ok {
		writeJSON(w, backendResponseFromConfig(id, *b, true))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) handleDeleteBackend(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.backendStore.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleSetDefaultBackend(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id = strings.TrimSpace(body.ID)
	}
	if err := s.backendStore.SetDefault(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "default_id": id})
}
