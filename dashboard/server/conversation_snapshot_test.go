package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationSnapshotWriteRead(t *testing.T) {
	dataDir := t.TempDir()
	s := &Server{dataDir: dataDir}
	want := ConversationSnapshot{
		SchemaVersion:        conversationSnapshotSchemaVersion,
		PokegentID:           "pg-123",
		SourceProvider:       "claude",
		SourceBackendKey:     "claude",
		SourceSessionID:      "session-123",
		SourceTranscriptPath: "/tmp/source.jsonl",
		CWD:                  "/tmp/work",
		CapturedAt:           "2026-05-06T10:00:00Z",
		Summary:              "latest summary",
		RecentTurns: []NormalizedTurn{{
			ID:   "u1",
			Role: "user",
			Text: "hello",
		}},
	}

	if err := s.writeConversationSnapshot(want); err != nil {
		t.Fatalf("writeConversationSnapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "snapshots", "pg-123.json")); err != nil {
		t.Fatalf("snapshot file not written: %v", err)
	}
	got, err := s.readConversationSnapshot("pg-123")
	if err != nil {
		t.Fatalf("readConversationSnapshot: %v", err)
	}
	if got.PokegentID != want.PokegentID ||
		got.SourceProvider != want.SourceProvider ||
		got.SourceSessionID != want.SourceSessionID ||
		got.RecentTurns[0].Text != want.RecentTurns[0].Text {
		t.Fatalf("snapshot roundtrip mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestBuildConversationSnapshotFromClaudeTranscript(t *testing.T) {
	dataDir := t.TempDir()
	claudeProjectDir := filepath.Join(dataDir, ".claude", "projects")
	projectDir := filepath.Join(claudeProjectDir, "tmp-work")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(projectDir, "claude-session.jsonl")
	writeJSONLLines(t, transcriptPath,
		`{"type":"system","cwd":"/tmp/work"}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-05-06T10:00:00Z","message":{"role":"user","content":"ship the snapshot migration"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-05-06T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"I will implement the snapshot migration."},{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"go test ./server"}}]}}`,
	)

	s := &Server{
		dataDir: dataDir,
		state:   NewStateManager(dataDir, claudeProjectDir),
	}
	got, err := s.buildConversationSnapshotFromTranscript("pg-claude", "claude", "claude-session", transcriptPath, "")
	if err != nil {
		t.Fatalf("buildConversationSnapshotFromTranscript: %v", err)
	}

	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
	if got.SourceProvider != "claude" {
		t.Fatalf("source_provider = %q, want claude", got.SourceProvider)
	}
	if got.CWD != "/tmp/work" {
		t.Fatalf("cwd = %q, want /tmp/work", got.CWD)
	}
	if got.Summary != "I will implement the snapshot migration." {
		t.Fatalf("summary = %q", got.Summary)
	}
	if len(got.RecentTurns) != 2 {
		t.Fatalf("recent_turns len = %d, want 2: %+v", len(got.RecentTurns), got.RecentTurns)
	}
	if got.RecentTurns[0].Role != "user" || got.RecentTurns[0].Text != "ship the snapshot migration" {
		t.Fatalf("bad user turn: %+v", got.RecentTurns[0])
	}
	assistant := got.RecentTurns[1]
	if assistant.Role != "assistant" || !strings.Contains(assistant.Text, "snapshot migration") {
		t.Fatalf("bad assistant turn: %+v", assistant)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "shell" || assistant.ToolCalls[0].ProviderName != "Bash" {
		t.Fatalf("bad normalized tool calls: %+v", assistant.ToolCalls)
	}
}

func TestBuildConversationSnapshotFromCodexTranscript(t *testing.T) {
	dataDir := t.TempDir()
	codexDir := filepath.Join(dataDir, ".pokegents", "codex-homes", "custom-codex-model", "sessions", "2026", "05", "06")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(codexDir, "codex-session.jsonl")
	writeJSONLLines(t, transcriptPath,
		`{"type":"session_meta","timestamp":"2026-05-06T10:00:00Z","payload":{"id":"codex-session","cwd":"/tmp/codex-work"}}`,
		`{"type":"response_item","timestamp":"2026-05-06T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"review the migration patch"}]}}`,
		`{"type":"response_item","timestamp":"2026-05-06T10:00:02Z","payload":{"type":"function_call","call_id":"call-1","name":"exec_command","arguments":"{\"cmd\":\"go test ./server\"}"}}`,
		`{"type":"response_item","timestamp":"2026-05-06T10:00:03Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"review complete with notes"}]}}`,
	)

	s := &Server{
		dataDir: dataDir,
		state:   NewStateManager(dataDir, filepath.Join(dataDir, ".claude", "projects")),
	}
	got, err := s.buildConversationSnapshotFromTranscript("pg-codex", "custom-codex-model", "codex-session", transcriptPath, "")
	if err != nil {
		t.Fatalf("buildConversationSnapshotFromTranscript: %v", err)
	}

	if got.SourceProvider != "codex" {
		t.Fatalf("source_provider = %q, want codex", got.SourceProvider)
	}
	if got.SourceBackendKey != "custom-codex-model" || got.SourceSessionID != "codex-session" {
		t.Fatalf("source identity mismatch: %+v", got)
	}
	if got.CWD != "/tmp/codex-work" {
		t.Fatalf("cwd = %q, want /tmp/codex-work", got.CWD)
	}
	if got.Summary != "review complete with notes" {
		t.Fatalf("summary = %q", got.Summary)
	}
	var sawShell bool
	for _, turn := range got.RecentTurns {
		for _, tc := range turn.ToolCalls {
			if tc.Name == "shell" && tc.ProviderName == "exec_command" {
				sawShell = true
			}
		}
	}
	if !sawShell {
		t.Fatalf("expected normalized shell tool call in %+v", got.RecentTurns)
	}
}

func TestProviderFromBackendTypePrefersConcreteBackendType(t *testing.T) {
	if got := providerFromBackendType("custom-key", "codex"); got != "codex" {
		t.Fatalf("providerFromBackendType(custom-key, codex) = %q, want codex", got)
	}
	if got := providerFromBackendType("custom-key", "claude-acp"); got != "claude" {
		t.Fatalf("providerFromBackendType(custom-key, claude-acp) = %q, want claude", got)
	}
}

func TestRenderSnapshotContextForCodexAlsoInjectsInitialPrompt(t *testing.T) {
	snapshot := ConversationSnapshot{
		PokegentID:           "pg-1",
		SourceProvider:       "claude",
		SourceTranscriptPath: "/tmp/claude.jsonl",
		CWD:                  "/tmp/work",
		Summary:              "handoff summary",
		RecentTurns: []NormalizedTurn{{
			Role: "user",
			Text: "continue this task",
		}},
	}

	got := renderSnapshotContext(snapshot, TransitionPurposeMigration, "codex")
	if got.CWD != "/tmp/work" {
		t.Fatalf("cwd = %q, want /tmp/work", got.CWD)
	}
	assertContainsAll(t, got.SystemPromptAppend,
		"## Migrated Conversation Context",
		"Target provider: codex.",
		"Previous provider: claude.",
		"Previous native transcript: /tmp/claude.jsonl",
		"handoff summary",
		"User: continue this task",
	)
	if got.InitialPromptContext != got.SystemPromptAppend {
		t.Fatalf("codex handoff should inject rendered snapshot into first prompt")
	}
}

func TestRenderSnapshotContextTruncatesOldestTurnsFirst(t *testing.T) {
	snapshot := ConversationSnapshot{
		PokegentID:     "pg-big",
		SourceProvider: "claude",
		CWD:            "/tmp/work",
	}
	for i := 0; i < 80; i++ {
		snapshot.RecentTurns = append(snapshot.RecentTurns, NormalizedTurn{
			Role: "assistant",
			Text: strings.Repeat("x", 500) + " marker-" + string(rune('A'+(i%26))),
		})
	}
	snapshot.RecentTurns[len(snapshot.RecentTurns)-1].Text = "LATEST-TURN-MUST-SURVIVE " + strings.Repeat("z", 500)
	snapshot.RecentTurns[0].Text = "OLDEST-TURN-SHOULD-DROP " + strings.Repeat("o", 500)

	got := renderSnapshotContext(snapshot, TransitionPurposeMigration, "codex").SystemPromptAppend
	if len(got) > 18200 {
		t.Fatalf("rendered snapshot too large: %d", len(got))
	}
	if !strings.Contains(got, "portable context truncated") {
		t.Fatalf("expected portable context truncation marker")
	}
	if !strings.Contains(got, "LATEST-TURN-MUST-SURVIVE") {
		t.Fatalf("latest turn was lost during truncation")
	}
	if strings.Contains(got, "OLDEST-TURN-SHOULD-DROP") {
		t.Fatalf("oldest turn should be dropped before newest turn")
	}
}
