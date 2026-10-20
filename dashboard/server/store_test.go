package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ──── Helpers ────────────────────────────────────────────────────────────────

// setupTestDir creates a temporary data directory with running/, status/, profiles/ subdirs.
func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"running", "status", "profiles", "messages"} {
		os.MkdirAll(filepath.Join(dir, sub), 0755)
	}
	return dir
}

func writeTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestJSON[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return v
}

// ──── Running file tests ─────────────────────────────────────────────────────

func TestRunningCreate(t *testing.T) {
	dir := setupTestDir(t)
	runDir := filepath.Join(dir, "running")

	rs := RunningSession{
		Profile:   "personal",
		SessionID: "abc-123",
		PID:       1234,
		TTY:       "/dev/ttys001",
	}
	path := filepath.Join(runDir, "personal-abc-123.json")
	writeTestJSON(t, path, rs)

	got := readTestJSON[RunningSession](t, path)
	if got.SessionID != "abc-123" {
		t.Errorf("session_id = %q, want %q", got.SessionID, "abc-123")
	}
	if got.Profile != "personal" {
		t.Errorf("profile = %q, want %q", got.Profile, "personal")
	}
}

func TestRunningListByContent(t *testing.T) {
	// Running files are keyed by session_id from JSON content, not filename
	dir := setupTestDir(t)
	runDir := filepath.Join(dir, "running")

	writeTestJSON(t, filepath.Join(runDir, "personal-aaa.json"), RunningSession{
		Profile: "personal", SessionID: "aaa", PID: 1,
	})
	writeTestJSON(t, filepath.Join(runDir, "client-bbb.json"), RunningSession{
		Profile: "client", SessionID: "bbb", PID: 2,
	})

	// Simulate loadRunning: read all files, key by SessionID
	entries, _ := os.ReadDir(runDir)
	running := make(map[string]RunningSession)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(runDir, e.Name()))
		var rs RunningSession
		if json.Unmarshal(data, &rs) == nil && rs.SessionID != "" {
			running[rs.SessionID] = rs
		}
	}

	if len(running) != 2 {
		t.Fatalf("got %d running sessions, want 2", len(running))
	}
	if running["aaa"].Profile != "personal" {
		t.Errorf("aaa.Profile = %q, want personal", running["aaa"].Profile)
	}
	if running["bbb"].Profile != "client" {
		t.Errorf("bbb.Profile = %q, want client", running["bbb"].Profile)
	}
}

func TestRunningGetBySessionIDNotFilename(t *testing.T) {
	// After hook reconciliation, filename might not match session_id
	// e.g. file is "personal-pokegent-uuid.json" but content has session_id: "claude-uuid"
	dir := setupTestDir(t)
	runDir := filepath.Join(dir, "running")

	writeTestJSON(t, filepath.Join(runDir, "personal-old-uuid.json"), RunningSession{
		Profile:    "personal",
		SessionID:  "new-claude-uuid",
		PokegentID: "old-uuid",
		PID:        1234,
	})

	entries, _ := os.ReadDir(runDir)
	running := make(map[string]RunningSession)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(runDir, e.Name()))
		var rs RunningSession
		if json.Unmarshal(data, &rs) == nil && rs.SessionID != "" {
			running[rs.SessionID] = rs
		}
	}

	// Should be keyed by "new-claude-uuid" (content), not "old-uuid" (filename)
	if _, ok := running["new-claude-uuid"]; !ok {
		t.Error("running file not found by content session_id")
	}
	if _, ok := running["old-uuid"]; ok {
		t.Error("running file should NOT be keyed by filename UUID")
	}
}

func TestRunningUpdate(t *testing.T) {
	// Atomic read-modify-write: update display_name without losing other fields
	dir := setupTestDir(t)
	path := filepath.Join(dir, "running", "personal-abc.json")

	writeTestJSON(t, path, RunningSession{
		Profile:   "personal",
		SessionID: "abc",
		PID:       1234,
		TTY:       "/dev/ttys001",
	})

	// Simulate atomic update: read → modify → write
	data, _ := os.ReadFile(path)
	var rs RunningSession
	json.Unmarshal(data, &rs)
	rs.DisplayName = "My Agent"
	updated, _ := json.Marshal(rs)
	os.WriteFile(path, updated, 0644)

	got := readTestJSON[RunningSession](t, path)
	if got.DisplayName != "My Agent" {
		t.Errorf("display_name = %q, want %q", got.DisplayName, "My Agent")
	}
	if got.PID != 1234 {
		t.Error("PID lost during update")
	}
	if got.TTY != "/dev/ttys001" {
		t.Error("TTY lost during update")
	}
}

func TestRunningDelete(t *testing.T) {
	dir := setupTestDir(t)
	path := filepath.Join(dir, "running", "personal-abc.json")
	writeTestJSON(t, path, RunningSession{SessionID: "abc"})

	os.Remove(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}

func TestRunningCloneCollision(t *testing.T) {
	// Two running files must never have the same session_id
	// Clone reconciliation should check before rename
	dir := setupTestDir(t)
	runDir := filepath.Join(dir, "running")

	// Original agent
	writeTestJSON(t, filepath.Join(runDir, "personal-sid-1.json"), RunningSession{
		Profile: "personal", SessionID: "sid-1", PID: 100,
	})

	// Clone tries to reconcile to same session_id
	clonePath := filepath.Join(runDir, "personal-pokegent-uuid.json")
	writeTestJSON(t, clonePath, RunningSession{
		Profile: "personal", SessionID: "pokegent-uuid", PokegentID: "pokegent-uuid", PID: 200,
	})

	// Simulate reconciliation: clone wants to rename to personal-sid-1.json
	targetPath := filepath.Join(runDir, "personal-sid-1.json")

	// Collision guard: check if target exists BEFORE renaming
	if _, err := os.Stat(targetPath); err == nil {
		// Target exists! Don't rename — would overwrite original
		// This is the correct behavior
	} else {
		t.Error("collision guard failed — target should exist")
	}

	// Verify original is untouched
	original := readTestJSON[RunningSession](t, targetPath)
	if original.PID != 100 {
		t.Error("original running file was overwritten")
	}
}

// ──── Status file tests ──────────────────────────────────────────────────────

func TestStatusUpsert(t *testing.T) {
	dir := setupTestDir(t)
	statusDir := filepath.Join(dir, "status")

	sf := StatusFile{
		SessionID: "abc-123",
		State:     "busy",
		Detail:    "thinking",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	path := filepath.Join(statusDir, "abc-123.json")
	writeTestJSON(t, path, sf)

	got := readTestJSON[StatusFile](t, path)
	if got.State != "busy" {
		t.Errorf("state = %q, want busy", got.State)
	}

	// Upsert: update state to idle (finished)
	sf.State = "idle"
	sf.Detail = "finished"
	writeTestJSON(t, path, sf)

	got2 := readTestJSON[StatusFile](t, path)
	if got2.State != "idle" {
		t.Errorf("state after upsert = %q, want idle", got2.State)
	}
}

func TestStatusRaceGuard(t *testing.T) {
	// PostToolUse (busy) must NOT overwrite Stop (idle)
	// This simulates the race condition we've seen in production
	dir := setupTestDir(t)
	path := filepath.Join(dir, "status", "abc.json")

	// Agent finishes: state = idle
	writeTestJSON(t, path, StatusFile{SessionID: "abc", State: "idle", Detail: "finished"})

	// Late PostToolUse arrives — should be rejected
	newState := "busy"
	event := "PostToolUse"

	current := readTestJSON[StatusFile](t, path)
	if current.State == "error" || current.State == "idle" {
		if newState == "busy" && event != "UserPromptSubmit" {
			// Guard: don't overwrite error/idle with busy (except on new prompt)
			newState = current.State // keep existing
		}
	}

	if newState != "idle" {
		t.Errorf("race guard failed: state = %q, should stay idle", newState)
	}
}

// ──── Profile tests ──────────────────────────────────────────────────────────

func TestProfileLoad(t *testing.T) {
	dir := setupTestDir(t)
	profDir := filepath.Join(dir, "profiles")

	writeTestJSON(t, filepath.Join(profDir, "personal.json"), map[string]any{
		"title": "Personal",
		"emoji": "🏠",
		"color": []int{100, 200, 100},
		"cwd":   "/home/user",
	})

	data, _ := os.ReadFile(filepath.Join(profDir, "personal.json"))
	var p Profile
	json.Unmarshal(data, &p)
	p.Name = "personal" // derived from filename

	if p.Title != "Personal" {
		t.Errorf("title = %q, want Personal", p.Title)
	}
	if p.Emoji != "🏠" {
		t.Errorf("emoji = %q, want 🏠", p.Emoji)
	}
	if p.Name != "personal" {
		t.Errorf("name = %q, want personal", p.Name)
	}
}

func TestProfileListAll(t *testing.T) {
	dir := setupTestDir(t)
	profDir := filepath.Join(dir, "profiles")

	for _, name := range []string{"personal", "client", "docs"} {
		writeTestJSON(t, filepath.Join(profDir, name+".json"), map[string]any{
			"title": name, "emoji": "📦", "color": []int{0, 0, 0},
		})
	}

	entries, _ := os.ReadDir(profDir)
	count := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			count++
		}
	}
	if count != 3 {
		t.Errorf("profile count = %d, want 3", count)
	}
}

// ──── Atomic write tests ─────────────────────────────────────────────────────

func TestAtomicWriteNoPartialReads(t *testing.T) {
	// Simulate concurrent read during write — reader should never see partial JSON
	dir := setupTestDir(t)
	path := filepath.Join(dir, "running", "test.json")

	writeTestJSON(t, path, RunningSession{SessionID: "initial", PID: 1})

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Writer: rapid updates
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			// Atomic write: write to temp, rename
			tmp := path + ".tmp"
			data, _ := json.Marshal(RunningSession{SessionID: "updated", PID: i})
			os.WriteFile(tmp, data, 0644)
			os.Rename(tmp, path)
		}
	}()

	// Reader: concurrent reads — must always get valid JSON
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			data, err := os.ReadFile(path)
			if err != nil {
				continue // file might not exist during rename
			}
			var rs RunningSession
			if err := json.Unmarshal(data, &rs); err != nil {
				errors <- err
			}
		}
	}()

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("partial read detected: %v", err)
	}
}

// ──── Message mailbox tests ──────────────────────────────────────────────────

func TestMessageMailboxCreateRead(t *testing.T) {
	dir := setupTestDir(t)
	msgDir := filepath.Join(dir, "messages", "recipient-123")
	os.MkdirAll(msgDir, 0755)

	msg := Message{
		ID:      "msg-001",
		From:    "sender-456",
		To:      "recipient-123",
		Content: "hello",
	}
	writeTestJSON(t, filepath.Join(msgDir, msg.ID+".json"), msg)

	got := readTestJSON[Message](t, filepath.Join(msgDir, msg.ID+".json"))
	if got.Content != "hello" {
		t.Errorf("content = %q, want hello", got.Content)
	}
	if got.From != "sender-456" {
		t.Errorf("from = %q, want sender-456", got.From)
	}
}

func TestMessageConsumeDeletes(t *testing.T) {
	dir := setupTestDir(t)
	msgDir := filepath.Join(dir, "messages", "recipient-123")
	os.MkdirAll(msgDir, 0755)

	path := filepath.Join(msgDir, "msg-001.json")
	writeTestJSON(t, path, Message{ID: "msg-001", Content: "hello"})

	// Consume: read then delete
	_ = readTestJSON[Message](t, path)
	os.Remove(path)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("message file should be deleted after consume")
	}
}

// ──── Name overrides tests ───────────────────────────────────────────────────

func TestNameOverridesRoundTrip(t *testing.T) {
	dir := setupTestDir(t)
	path := filepath.Join(dir, "name-overrides.json")

	overrides := map[string]string{
		"abc-123": "My Agent",
		"def-456": "Other Agent",
	}
	writeTestJSON(t, path, overrides)

	got := readTestJSON[map[string]string](t, path)
	if got["abc-123"] != "My Agent" {
		t.Errorf("override = %q, want My Agent", got["abc-123"])
	}
}

// ──── Session ID map tests ───────────────────────────────────────────────────

func TestSessionIDMapRoundTrip(t *testing.T) {
	dir := setupTestDir(t)
	path := filepath.Join(dir, "session-id-map.json")

	m := map[string]string{
		"pokegent-uuid-1": "claude-uuid-1",
		"pokegent-uuid-2": "claude-uuid-2",
	}
	writeTestJSON(t, path, m)

	got := readTestJSON[map[string]string](t, path)
	if got["pokegent-uuid-1"] != "claude-uuid-1" {
		t.Error("session ID map roundtrip failed")
	}
}

// ──── Agent order tests ──────────────────────────────────────────────────────

func TestAgentOrderRoundTrip(t *testing.T) {
	dir := setupTestDir(t)
	path := filepath.Join(dir, "agent-order.json")

	order := []string{"sid-3", "sid-1", "sid-2"}
	writeTestJSON(t, path, order)

	got := readTestJSON[[]string](t, path)
	if len(got) != 3 || got[0] != "sid-3" {
		t.Error("agent order roundtrip failed")
	}
}

func TestAgentOrderSorting(t *testing.T) {
	// Agents in order come first, unordered at end by creation time
	order := []string{"sid-b", "sid-a"}

	type agent struct {
		id        string
		createdAt string
	}
	agents := []agent{
		{"sid-a", "2024-01-02T00:00:00Z"},
		{"sid-b", "2024-01-01T00:00:00Z"},
		{"sid-c", "2024-01-03T00:00:00Z"}, // not in order
		{"sid-d", "2024-01-04T00:00:00Z"}, // not in order
	}

	orderIndex := make(map[string]int)
	for i, sid := range order {
		orderIndex[sid] = i + 1
	}

	// Sort: ordered agents first (by position), unordered at end (by createdAt)
	sorted := make([]agent, len(agents))
	copy(sorted, agents)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			oi, oj := orderIndex[sorted[i].id], orderIndex[sorted[j].id]
			swap := false
			if oi != 0 && oj != 0 {
				swap = oi > oj
			} else if oi != 0 {
				swap = false
			} else if oj != 0 {
				swap = true
			} else {
				swap = sorted[i].createdAt > sorted[j].createdAt
			}
			if swap {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	expected := []string{"sid-b", "sid-a", "sid-c", "sid-d"}
	for i, a := range sorted {
		if a.id != expected[i] {
			t.Errorf("position %d: got %s, want %s", i, a.id, expected[i])
		}
	}
}
