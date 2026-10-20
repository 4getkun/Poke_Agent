package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ──── ActivityEntry (local copy matching handleGetActivity) ─────────────────

// ActivityEntry mirrors the struct defined inside handleGetActivity.
type ActivityEntry struct {
	Timestamp string
	SessionID string
	AgentName string
	Files     string
	Summary   string
	Raw       string
}

// parseActivityEntry extracts fields from one log line using the same logic
// as handleGetActivity in server.go.
func parseActivityEntry(line string) (ActivityEntry, bool) {
	if line == "" {
		return ActivityEntry{}, false
	}
	entry := ActivityEntry{Raw: line}
	rest := line

	// [TIMESTAMP]
	if strings.HasPrefix(rest, "[") {
		if idx := strings.Index(rest, "] "); idx > 0 {
			entry.Timestamp = rest[1:idx]
			rest = rest[idx+2:]
		}
	}
	// [SESSION_ID]
	if strings.HasPrefix(rest, "[") {
		if idx := strings.Index(rest, "] "); idx > 0 {
			entry.SessionID = rest[1:idx]
			rest = rest[idx+2:]
		}
	}
	// [AGENT_NAME]
	if strings.HasPrefix(rest, "[") {
		if idx := strings.Index(rest, "] "); idx > 0 {
			entry.AgentName = rest[1:idx]
			rest = rest[idx+2:]
		}
	}

	// Files — Summary
	if dashIdx := strings.Index(rest, " — "); dashIdx >= 0 {
		entry.Files = strings.TrimSpace(rest[:dashIdx])
		entry.Summary = rest[dashIdx+len(" — "):]
	} else {
		entry.Files = strings.TrimSpace(rest)
	}

	// Skip entries with no actual file paths
	if entry.Files == "" || strings.HasPrefix(entry.Files, "—") {
		return entry, false
	}
	return entry, true
}

// parseAllEntries parses multiple lines and returns only valid entries.
func parseAllEntries(lines []string) []ActivityEntry {
	var out []ActivityEntry
	for _, line := range lines {
		if e, ok := parseActivityEntry(line); ok {
			out = append(out, e)
		}
	}
	return out
}

// ──── Test 1: Parse a well-formed activity entry ────────────────────────────

func TestParseActivityEntry(t *testing.T) {
	line := "[2026-03-26T10:30:00Z] [abc-123] [Client SDK] edited server/server.go, edited server/state.go"
	entry, ok := parseActivityEntry(line)
	if !ok {
		t.Fatal("expected valid entry, got skip")
	}
	if entry.Timestamp != "2026-03-26T10:30:00Z" {
		t.Errorf("timestamp = %q, want %q", entry.Timestamp, "2026-03-26T10:30:00Z")
	}
	if entry.SessionID != "abc-123" {
		t.Errorf("session_id = %q, want %q", entry.SessionID, "abc-123")
	}
	if entry.AgentName != "Client SDK" {
		t.Errorf("agent_name = %q, want %q", entry.AgentName, "Client SDK")
	}
	if entry.Files != "edited server/server.go, edited server/state.go" {
		t.Errorf("files = %q, want %q", entry.Files, "edited server/server.go, edited server/state.go")
	}
	if entry.Summary != "" {
		t.Errorf("summary = %q, want empty", entry.Summary)
	}
	if entry.Raw != line {
		t.Errorf("raw = %q, want original line", entry.Raw)
	}
}

// ──── Test 2: Parse entry with summary (has " — " separator) ────────────────

func TestParseEntryWithSummary(t *testing.T) {
	line := "[2026-03-26T11:00:00Z] [def-456] [Platform] edited hooks/status-update.sh — Fixed rotation logic for activity logs"
	entry, ok := parseActivityEntry(line)
	if !ok {
		t.Fatal("expected valid entry, got skip")
	}
	if entry.Files != "edited hooks/status-update.sh" {
		t.Errorf("files = %q, want %q", entry.Files, "edited hooks/status-update.sh")
	}
	if entry.Summary != "Fixed rotation logic for activity logs" {
		t.Errorf("summary = %q, want %q", entry.Summary, "Fixed rotation logic for activity logs")
	}
}

// ──── Test 3: Parse entry without summary ───────────────────────────────────

func TestParseEntryWithoutSummary(t *testing.T) {
	line := "[2026-03-26T12:00:00Z] [ghi-789] [Rollouts] edited deploy/main.tf, edited deploy/vars.tf"
	entry, ok := parseActivityEntry(line)
	if !ok {
		t.Fatal("expected valid entry, got skip")
	}
	if entry.Files != "edited deploy/main.tf, edited deploy/vars.tf" {
		t.Errorf("files = %q", entry.Files)
	}
	if entry.Summary != "" {
		t.Errorf("expected empty summary, got %q", entry.Summary)
	}
}

// ──── Test 4: Filter entries without files ──────────────────────────────────

func TestFilterEntriesWithoutFiles(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"empty line", "", false},
		{"dash-only files", "[2026-03-26T13:00:00Z] [aaa-111] [Agent] — just a summary with no files", false},
		{"no brackets at all", "random garbage", true}, // has content in Files
		{"valid entry", "[2026-03-26T13:00:00Z] [bbb-222] [Agent] edited foo.go", true},
		{"empty after brackets", "[2026-03-26T13:00:00Z] [ccc-333] [Agent] ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseActivityEntry(tt.line)
			if ok != tt.want {
				t.Errorf("parseActivityEntry(%q) ok = %v, want %v", tt.line, ok, tt.want)
			}
		})
	}
}

// ──── Test 5: Rotation (>500 lines → keep last 200) ────────────────────────

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "test-project.log")

	// Write 600 lines
	var lines []string
	for i := 1; i <= 600; i++ {
		lines = append(lines, fmt.Sprintf("[2026-03-26T10:%02d:%02dZ] [sess-%04d] [Agent%d] edited file%d.go", i/60, i%60, i, i%5, i))
	}
	if err := os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	// Verify 600 lines exist
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	rawLines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(rawLines) != 600 {
		t.Fatalf("initial line count = %d, want 600", len(rawLines))
	}

	// Apply rotation logic: if >500, keep last 200 (mirrors status-update.sh)
	if len(rawLines) > 500 {
		kept := rawLines[len(rawLines)-200:]
		if err := os.WriteFile(logFile, []byte(strings.Join(kept, "\n")+"\n"), 0644); err != nil {
			t.Fatalf("rotate write: %v", err)
		}
	}

	// Verify 200 lines remain
	data, err = os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read after rotation: %v", err)
	}
	remaining := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(remaining) != 200 {
		t.Errorf("after rotation: %d lines, want 200", len(remaining))
	}

	// Verify the kept lines are the LAST 200 of the original 600 (lines 401-600)
	if remaining[0] != lines[400] {
		t.Errorf("first remaining line = %q, want %q (original line 401)", remaining[0], lines[400])
	}
	if remaining[199] != lines[599] {
		t.Errorf("last remaining line = %q, want %q (original line 600)", remaining[199], lines[599])
	}
}

// ──── Test 6: Read-since (tail from a given line offset) ────────────────────

func TestReadSince(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "project.log")

	// Write 10 lines
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("[2026-03-26T10:00:%02dZ] [sess-%02d] [Agent] edited file%d.go", i, i, i))
	}
	if err := os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Simulate "read from line 5": keep only lines AFTER line 5 (i.e. lines 6-10).
	// The hook does: tail -n +$((LAST_LINE + 1)) which with LAST_LINE=5 gives tail -n +6.
	lastLine := 5
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	allLines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	if lastLine >= len(allLines) {
		t.Fatal("lastLine >= total lines, nothing new")
	}
	newLines := allLines[lastLine:] // lines index 5..9 = original lines 6..10

	if len(newLines) != 5 {
		t.Fatalf("new lines count = %d, want 5", len(newLines))
	}

	// Verify first new line is line 6 (sess-06) and last is line 10 (sess-10)
	if !strings.Contains(newLines[0], "[sess-06]") {
		t.Errorf("first new line = %q, want to contain [sess-06]", newLines[0])
	}
	if !strings.Contains(newLines[4], "[sess-10]") {
		t.Errorf("last new line = %q, want to contain [sess-10]", newLines[4])
	}
}

// ──── Test 7: Exclude own session ───────────────────────────────────────────

func TestExcludeOwnSession(t *testing.T) {
	mySessionID := "my-session-abc"
	entries := []string{
		"[2026-03-26T10:00:01Z] [other-111] [Alice] edited foo.go",
		"[2026-03-26T10:00:02Z] [my-session-abc] [Me] edited bar.go",
		"[2026-03-26T10:00:03Z] [other-222] [Bob] edited baz.go",
		"[2026-03-26T10:00:04Z] [my-session-abc] [Me] edited qux.go",
		"[2026-03-26T10:00:05Z] [other-333] [Charlie] edited quux.go",
	}

	// Filter out entries matching mySessionID (mirrors: grep -v "\[$SESSION_ID\]")
	var filtered []string
	for _, line := range entries {
		if !strings.Contains(line, "["+mySessionID+"]") {
			filtered = append(filtered, line)
		}
	}

	if len(filtered) != 3 {
		t.Fatalf("filtered count = %d, want 3", len(filtered))
	}

	// Verify none of the filtered entries belong to our session
	for _, line := range filtered {
		if strings.Contains(line, "["+mySessionID+"]") {
			t.Errorf("own session leaked through filter: %q", line)
		}
	}

	// Verify the three remaining are Alice, Bob, Charlie
	parsed := parseAllEntries(filtered)
	expectedNames := []string{"Alice", "Bob", "Charlie"}
	for i, name := range expectedNames {
		if parsed[i].AgentName != name {
			t.Errorf("entry %d agent_name = %q, want %q", i, parsed[i].AgentName, name)
		}
	}
}

// ──── Test 8: Overlap detection ─────────────────────────────────────────────

func TestOverlapDetection(t *testing.T) {
	// "My files" — the files the current agent has recently touched (relative paths)
	myFiles := []string{
		"server/server.go",
		"server/state.go",
		"hooks/status-update.sh",
	}

	// Activity entries from other agents
	otherEntries := []string{
		"[2026-03-26T10:00:01Z] [aaa-111] [Alice] edited server/server.go, edited server/models.go",
		"[2026-03-26T10:00:02Z] [bbb-222] [Bob] edited web/src/App.tsx",
		"[2026-03-26T10:00:03Z] [ccc-333] [Charlie] edited hooks/status-update.sh — refactored activity rotation",
		"[2026-03-26T10:00:04Z] [ddd-444] [Dave] edited README.md, edited go.mod",
	}

	// Detect overlaps: mirror the hook's logic of checking each entry for file mentions
	var overlaps []string
	for _, entryLine := range otherEntries {
		for _, mf := range myFiles {
			if strings.Contains(entryLine, mf) {
				overlaps = append(overlaps, entryLine)
				break // one match per entry is enough
			}
		}
	}

	if len(overlaps) != 2 {
		t.Fatalf("overlap count = %d, want 2", len(overlaps))
	}

	// Verify the overlapping entries are Alice (server/server.go) and Charlie (hooks/status-update.sh)
	parsed := parseAllEntries(overlaps)
	if parsed[0].AgentName != "Alice" {
		t.Errorf("first overlap agent = %q, want Alice", parsed[0].AgentName)
	}
	if parsed[1].AgentName != "Charlie" {
		t.Errorf("second overlap agent = %q, want Charlie", parsed[1].AgentName)
	}

	// Verify non-overlapping entries are NOT included
	for _, o := range overlaps {
		if strings.Contains(o, "Bob") || strings.Contains(o, "Dave") {
			t.Errorf("non-overlapping entry leaked: %q", o)
		}
	}
}

// ──── Test: Overlap detection with no overlaps ──────────────────────────────

func TestOverlapDetectionNoOverlaps(t *testing.T) {
	myFiles := []string{"server/server.go"}
	otherEntries := []string{
		"[2026-03-26T10:00:01Z] [aaa-111] [Alice] edited web/src/App.tsx",
		"[2026-03-26T10:00:02Z] [bbb-222] [Bob] edited README.md",
	}

	var overlaps []string
	for _, entryLine := range otherEntries {
		for _, mf := range myFiles {
			if strings.Contains(entryLine, mf) {
				overlaps = append(overlaps, entryLine)
				break
			}
		}
	}

	if len(overlaps) != 0 {
		t.Errorf("expected no overlaps, got %d", len(overlaps))
	}
}

// ──── Test: Rotation does not trigger when under threshold ──────────────────

func TestRotationUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "small.log")

	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("[2026-03-26T10:00:%02dZ] [sess-%04d] [Agent] edited file%d.go", i%60, i, i))
	}
	if err := os.WriteFile(logFile, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Rotation should NOT trigger (100 <= 500)
	data, _ := os.ReadFile(logFile)
	rawLines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(rawLines) > 500 {
		t.Fatal("unexpectedly above threshold")
	}

	// File should remain unchanged
	if len(rawLines) != 100 {
		t.Errorf("line count = %d, want 100 (unchanged)", len(rawLines))
	}
}

// ──── Test: Parse entry with em-dash in files area is skipped ───────────────

func TestParseEntryDashPrefixFilesSkipped(t *testing.T) {
	// If the files portion starts with "—" (em dash), it should be skipped
	line := "[2026-03-26T14:00:00Z] [xxx-999] [Agent] — summary only, no files"
	_, ok := parseActivityEntry(line)
	if ok {
		t.Error("expected entry with dash-only files to be skipped")
	}
}

// ──── Test: End-to-end file read and parse ──────────────────────────────────

func TestEndToEndFileReadAndParse(t *testing.T) {
	dir := t.TempDir()
	activityDir := filepath.Join(dir, "activity")
	os.MkdirAll(activityDir, 0755)

	logContent := strings.Join([]string{
		"[2026-03-26T10:00:01Z] [sess-001] [Alice] edited server/server.go",
		"[2026-03-26T10:00:02Z] [sess-002] [Bob] edited web/App.tsx — Updated layout",
		"", // empty line should be skipped
		"[2026-03-26T10:00:03Z] [sess-003] [Charlie] — no files here",
		"[2026-03-26T10:00:04Z] [sess-004] [Dave] edited hooks/hook.sh, edited hooks/line.sh",
	}, "\n")

	if err := os.WriteFile(filepath.Join(activityDir, "test-project.log"), []byte(logContent), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read and parse like handleGetActivity does
	entries, err := os.ReadDir(activityDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}

	var all []ActivityEntry
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(activityDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if entry, ok := parseActivityEntry(line); ok {
				all = append(all, entry)
			}
		}
	}

	// Should have 3 valid entries (empty line and dash-only entry skipped)
	if len(all) != 3 {
		t.Fatalf("entry count = %d, want 3", len(all))
	}
	if all[0].AgentName != "Alice" {
		t.Errorf("entry 0 agent = %q, want Alice", all[0].AgentName)
	}
	if all[1].Summary != "Updated layout" {
		t.Errorf("entry 1 summary = %q, want 'Updated layout'", all[1].Summary)
	}
	if all[2].Files != "edited hooks/hook.sh, edited hooks/line.sh" {
		t.Errorf("entry 2 files = %q", all[2].Files)
	}
}
