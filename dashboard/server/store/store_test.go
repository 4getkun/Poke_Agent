package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRunningStore(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "running")
	os.MkdirAll(runDir, 0755)

	s := &FileRunningStore{dir: runDir}

	// Create
	rs := RunningSession{
		Profile:     "test",
		SessionID:   "abc-123",
		PokegentID:  "def-456",
		DisplayName: "Test Agent",
		PID:         1234,
		TTY:         "/dev/ttys001",
	}
	if err := s.Create(rs); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get
	got, err := s.Get("abc-123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DisplayName != "Test Agent" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Test Agent")
	}

	// GetByPokegentID
	got, err = s.GetByPokegentID("def-456")
	if err != nil {
		t.Fatalf("GetByPokegentID: %v", err)
	}
	if got.SessionID != "abc-123" {
		t.Errorf("SessionID = %q, want %q", got.SessionID, "abc-123")
	}

	// Update
	if err := s.Update("abc-123", func(rs *RunningSession) {
		rs.DisplayName = "Renamed Agent"
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.Get("abc-123")
	if got.DisplayName != "Renamed Agent" {
		t.Errorf("after Update, DisplayName = %q, want %q", got.DisplayName, "Renamed Agent")
	}

	// List
	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List len = %d, want 1", len(list))
	}

	// Delete
	if err := s.Delete("abc-123"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ = s.List()
	if len(list) != 0 {
		t.Errorf("after Delete, List len = %d, want 0", len(list))
	}
}

func TestFileStatusStore(t *testing.T) {
	dir := t.TempDir()
	statusDir := filepath.Join(dir, "status")
	os.MkdirAll(statusDir, 0755)

	s := &FileStatusStore{dir: statusDir}

	sf := StatusFile{SessionID: "test-1", State: "busy", Detail: "processing"}
	if err := s.Upsert(sf); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Get("test-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != "busy" {
		t.Errorf("State = %q, want %q", got.State, "busy")
	}

	// Update
	sf.State = "idle"
	s.Upsert(sf)
	got, _ = s.Get("test-1")
	if got.State != "idle" {
		t.Errorf("after Upsert, State = %q, want %q", got.State, "idle")
	}

	// Delete
	s.Delete("test-1")
	_, err = s.Get("test-1")
	if err == nil {
		t.Error("expected error after Delete")
	}
}

func TestFileMessageStore(t *testing.T) {
	dir := t.TempDir()
	msgDir := filepath.Join(dir, "messages")
	os.MkdirAll(msgDir, 0755)

	s := &FileMessageStore{dir: msgDir, dataDir: dir}

	// Send
	msg, err := s.Send("agent-a", "Agent A", "agent-b", "Agent B", "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello")
	}

	// GetUndelivered
	undelivered, _ := s.GetUndelivered("agent-b")
	if len(undelivered) != 1 {
		t.Fatalf("GetUndelivered len = %d, want 1", len(undelivered))
	}

	// MarkDelivered
	s.MarkDelivered([]string{msg.ID})
	undelivered, _ = s.GetUndelivered("agent-b")
	if len(undelivered) != 0 {
		t.Errorf("after MarkDelivered, GetUndelivered len = %d, want 0", len(undelivered))
	}

	// Consume (deletes files)
	s.Send("agent-a", "Agent A", "agent-b", "Agent B", "second msg")
	consumed, _ := s.Consume("agent-b")
	if len(consumed) < 1 {
		t.Error("Consume should return messages")
	}

	// Budget
	s.ResetBudget("agent-a")
	budget, _ := s.GetBudget("agent-a")
	if budget != 0 {
		t.Errorf("budget = %d, want 0", budget)
	}
}

func TestFileConfigStore(t *testing.T) {
	dir := t.TempDir()
	s := &FileConfigStore{path: filepath.Join(dir, "config.json")}

	// Missing file → defaults
	cfg, err := s.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cfg.Port != 7834 {
		t.Errorf("Port = %d, want 7834", cfg.Port)
	}
	if cfg.DefaultProfile != "personal" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "personal")
	}

	// With file
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"port": 9999}`), 0644)
	cfg, _ = s.Get()
	if cfg.Port != 9999 {
		t.Errorf("Port = %d, want 9999", cfg.Port)
	}
	// Unset fields keep defaults
	if cfg.DefaultProfile != "personal" {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, "personal")
	}
}

func TestCodexTranscriptParserCustomToolCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.jsonl")
	patch := "*** Begin Patch\n*** Update File: web/src/App.tsx\n@@\n-old\n+new\n*** End Patch\n"
	line, err := json.Marshal(map[string]any{
		"type":      "response_item",
		"timestamp": "2026-05-05T12:00:00Z",
		"payload": map[string]any{
			"type":    "custom_tool_call",
			"status":  "completed",
			"call_id": "call_1",
			"name":    "apply_patch",
			"input":   patch,
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	page := (&CodexTranscriptParser{}).Parse(path, 10, "")
	if len(page.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(page.Entries))
	}
	entry := page.Entries[0]
	if entry.Type != "assistant" || len(entry.Blocks) != 1 {
		t.Fatalf("entry = %#v, want one assistant tool_use block", entry)
	}
	block := entry.Blocks[0]
	if block.Type != "tool_use" || block.Name != "apply_patch" || block.ID != "call_1" {
		t.Fatalf("block = %#v, want apply_patch tool_use", block)
	}
	if !strings.Contains(block.Input, "*** Update File: web/src/App.tsx") || !strings.Contains(block.Input, "+new") {
		t.Fatalf("block input did not preserve patch preview data: %q", block.Input)
	}
}

func TestCodexTranscriptParserCompactedEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.jsonl")

	userLine, err := json.Marshal(map[string]any{
		"type":      "response_item",
		"timestamp": "2026-05-05T12:00:00Z",
		"payload": map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "/compact"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal user: %v", err)
	}
	compactLine, err := json.Marshal(map[string]any{
		"type":      "compacted",
		"timestamp": "2026-05-05T12:01:00Z",
		"payload": map[string]any{
			"message":             "Summary:\n- preserved key decision\n- next step",
			"replacement_history": []any{map[string]any{"large": strings.Repeat("x", 1024)}},
		},
	})
	if err != nil {
		t.Fatalf("Marshal compacted: %v", err)
	}
	if err := os.WriteFile(path, []byte(string(userLine)+"\n"+string(compactLine)+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	page := (&CodexTranscriptParser{}).Parse(path, 10, "")
	if len(page.Entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(page.Entries))
	}
	entry := page.Entries[1]
	if entry.Type != "user" {
		t.Fatalf("compacted entry type = %q, want user", entry.Type)
	}
	if !strings.HasPrefix(entry.Content, "Context compacted.\n\nSummary:") ||
		!strings.Contains(entry.Content, "preserved key decision") {
		t.Fatalf("unexpected compacted content: %q", entry.Content)
	}
	if strings.Contains(entry.Content, "replacement_history") || strings.Contains(entry.Content, strings.Repeat("x", 32)) {
		t.Fatalf("compacted entry leaked raw replacement_history: %q", entry.Content)
	}
}
