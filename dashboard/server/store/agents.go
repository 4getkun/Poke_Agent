package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FileAgentIdentityStore manages persistent agent identity files.
// Files live at ~/.pokegents/agents/{pokegent_id}.json and are never deleted automatically.
type FileAgentIdentityStore struct {
	mu  sync.Mutex
	dir string
}

func (s *FileAgentIdentityStore) path(pokegentID string) string {
	return filepath.Join(s.dir, pokegentID+".json")
}

func (s *FileAgentIdentityStore) Get(pokegentID string) (*AgentIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(pokegentID))
	if err != nil {
		return nil, err
	}
	var id AgentIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

func (s *FileAgentIdentityStore) List() ([]AgentIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []AgentIdentity
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var id AgentIdentity
		if err := json.Unmarshal(data, &id); err != nil {
			continue
		}
		result = append(result, id)
	}
	return result, nil
}

func (s *FileAgentIdentityStore) Save(identity AgentIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	os.MkdirAll(s.dir, 0o755)
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path(identity.PokegentID), data, 0o644)
}

func (s *FileAgentIdentityStore) Update(pokegentID string, fn func(*AgentIdentity)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(pokegentID)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var id AgentIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return err
	}
	fn(&id)
	updated, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, updated, 0o644)
}
