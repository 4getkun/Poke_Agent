package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileEphemeralStore manages ephemeral agent files in ~/.pokegents/ephemeral/.
type FileEphemeralStore struct {
	mu  sync.Mutex
	dir string
}

func (s *FileEphemeralStore) path(agentID string) string {
	return filepath.Join(s.dir, agentID+".json")
}

func (s *FileEphemeralStore) Get(agentID string) (*EphemeralAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(agentID))
	if err != nil {
		return nil, err
	}
	var ea EphemeralAgent
	if err := json.Unmarshal(data, &ea); err != nil {
		return nil, err
	}
	return &ea, nil
}

func (s *FileEphemeralStore) List() ([]EphemeralAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result []EphemeralAgent
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var ea EphemeralAgent
		if err := json.Unmarshal(data, &ea); err != nil {
			continue
		}
		result = append(result, ea)
	}
	return result, nil
}

func (s *FileEphemeralStore) Create(ea EphemeralAgent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	os.MkdirAll(s.dir, 0o755)
	data, err := json.MarshalIndent(ea, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path(ea.AgentID), data, 0o644)
}

func (s *FileEphemeralStore) Complete(agentID, lastMessage, transcriptPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(agentID))
	if err != nil {
		return fmt.Errorf("ephemeral agent %s not found: %w", agentID, err)
	}
	var ea EphemeralAgent
	if err := json.Unmarshal(data, &ea); err != nil {
		return err
	}
	ea.State = "completed"
	ea.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	ea.LastMessage = lastMessage
	ea.TranscriptPath = transcriptPath
	if ea.CreatedAt != "" {
		if created, err := time.Parse(time.RFC3339, ea.CreatedAt); err == nil {
			ea.DurationSec = int(time.Since(created).Seconds())
		}
	}
	updated, err := json.MarshalIndent(ea, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.path(agentID), updated, 0o644)
}

func (s *FileEphemeralStore) Delete(agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(s.path(agentID))
}

func (s *FileEphemeralStore) Cleanup(maxAge time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ea EphemeralAgent
		if err := json.Unmarshal(data, &ea); err != nil {
			continue
		}
		if ea.State != "completed" {
			continue
		}
		if ea.CompletedAt == "" {
			continue
		}
		completed, err := time.Parse(time.RFC3339, ea.CompletedAt)
		if err != nil {
			continue
		}
		if completed.Before(cutoff) {
			os.Remove(path)
			removed++
		}
	}
	return removed, nil
}
