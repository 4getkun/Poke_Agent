package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pokegents/dashboard/server/store"
)

func TestMigrateNonClaudeChatToITermUsesRealStateHandoff(t *testing.T) {
	// This is intentionally a temp-HOME/temp-dataDir integration test. It
	// exercises real file-backed running state, identity state, transcript
	// discovery, snapshot persistence, handoff markdown writing, and the HTTP
	// migrate handler. Only the external iTerm launch is replaced by a recorder,
	// so the test cannot touch the developer's actual dashboard/iTerm session.
	home := t.TempDir()
	t.Setenv("HOME", home)

	dataDir := filepath.Join(home, ".pokegents")
	claudeProjectDir := filepath.Join(home, ".claude", "projects")
	for _, dir := range []string{
		filepath.Join(dataDir, "running"),
		filepath.Join(dataDir, "agents"),
		claudeProjectDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	pgid := "pg-codex-handoff"
	codexSessionID := "codex-session-handoff"
	cwd := filepath.Join(home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptPath := filepath.Join(dataDir, "codex-homes", "custom-codex-model", "sessions", "2026", "05", "06", "codex-session-handoff.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	sessionMeta, err := json.Marshal(map[string]any{
		"type":      "session_meta",
		"timestamp": "2026-05-06T10:00:00Z",
		"payload": map[string]string{
			"id":  "codex-session-handoff",
			"cwd": cwd,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeJSONLLines(t, transcriptPath,
		string(sessionMeta),
		`{"type":"response_item","timestamp":"2026-05-06T10:00:01Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"please carry this codex request into iterm claude"}]}}`,
		`{"type":"response_item","timestamp":"2026-05-06T10:00:02Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"codex answer that must be in handoff context"}]}}`,
	)

	fileStore := store.NewFileStore(dataDir)
	if err := fileStore.Agents.Save(store.AgentIdentity{
		PokegentID:   pgid,
		DisplayName:  "Codex Agent",
		Profile:      "reviewer",
		Interface:    "chat",
		AgentBackend: "custom-codex-model",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(dataDir, "running", "reviewer-"+pgid+".json")
	writeTestJSON(t, runningPath, store.RunningSession{
		Profile:        "reviewer",
		PokegentID:     pgid,
		SessionID:      codexSessionID,
		DisplayName:    "Codex Agent",
		Interface:      "chat",
		AgentBackend:   "custom-codex-model",
		CWD:            cwd,
		TranscriptPath: transcriptPath,
		PID:            os.Getpid(), // real pid so StateManager treats it as alive.
	})

	backendStore := store.NewBackendStore(dataDir)
	state := NewStateManagerWithStore(fileStore, dataDir, claudeProjectDir)
	state.SetBackendStore(backendStore)
	if err := state.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	terminal := &recordingTerminal{}
	eventBus := NewEventBus()
	s := &Server{
		state:        state,
		eventBus:     eventBus,
		fileStore:    fileStore,
		dataDir:      dataDir,
		backendStore: backendStore,
		terminal:     terminal,
		chatMgr:      NewChatManager(dataDir, nil, eventBus, NewUsageLogger(dataDir)),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+pgid+"/migrate", strings.NewReader(`{"to":"iterm2"}`))
	req.SetPathValue("id", pgid)
	rec := httptest.NewRecorder()
	s.handleMigrateInterface(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("migrate status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if terminal.resumePokegentCalls != 0 || terminal.resumeSessionCalls != 0 {
		t.Fatalf("native resume must not be used for Codex→iTerm: ResumePokegent=%d ResumeSession=%d", terminal.resumePokegentCalls, terminal.resumeSessionCalls)
	}
	if len(terminal.launches) != 1 {
		t.Fatalf("LaunchProfile calls = %d, want 1", len(terminal.launches))
	}
	launch := terminal.launches[0]
	if launch.PokegentID != pgid {
		t.Fatalf("launch pokegent_id = %q, want %q", launch.PokegentID, pgid)
	}
	if launch.Profile != "reviewer" {
		t.Fatalf("launch profile = %q, want reviewer", launch.Profile)
	}
	if launch.HandoffContextPath == "" {
		t.Fatalf("LaunchProfile did not receive HandoffContextPath")
	}
	if !strings.HasPrefix(launch.HandoffContextPath, dataDir+string(filepath.Separator)) {
		t.Fatalf("handoff path %q is not under temp dataDir %q", launch.HandoffContextPath, dataDir)
	}

	handoff, err := os.ReadFile(launch.HandoffContextPath)
	if err != nil {
		t.Fatalf("read handoff context: %v", err)
	}
	assertContainsAll(t, string(handoff),
		"## Migrated Conversation Context",
		"Target provider: claude.",
		"Previous provider: codex.",
		"please carry this codex request into iterm claude",
		"codex answer that must be in handoff context",
	)

	snapshotPath := filepath.Join(dataDir, "snapshots", pgid+".json")
	var snapshot ConversationSnapshot
	rawSnapshot, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("snapshot was not written at %s: %v", snapshotPath, err)
	}
	if err := json.Unmarshal(rawSnapshot, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snapshot.PokegentID != pgid || snapshot.SourceProvider != "codex" || snapshot.SourceSessionID != codexSessionID {
		t.Fatalf("bad snapshot identity: %+v", snapshot)
	}
	if snapshot.SourceTranscriptPath != transcriptPath || snapshot.CWD != cwd {
		t.Fatalf("bad snapshot source fields: %+v", snapshot)
	}

	rewritten := readTestJSON[store.RunningSession](t, runningPath)
	if rewritten.Interface != "iterm2" {
		t.Fatalf("running interface = %q, want iterm2", rewritten.Interface)
	}
	if rewritten.SessionID != "" {
		t.Fatalf("running session_id = %q, want empty fresh-Claude placeholder", rewritten.SessionID)
	}
	if rewritten.TranscriptPath != "" {
		t.Fatalf("running transcript_path = %q, want empty fresh-Claude placeholder", rewritten.TranscriptPath)
	}
	if rewritten.SourceTranscriptPath != transcriptPath {
		t.Fatalf("source_transcript_path = %q, want %q", rewritten.SourceTranscriptPath, transcriptPath)
	}
	if rewritten.AgentBackend != "" {
		t.Fatalf("agent_backend = %q, want empty for iTerm Claude", rewritten.AgentBackend)
	}

	identity, err := fileStore.Agents.Get(pgid)
	if err != nil {
		t.Fatalf("identity missing: %v", err)
	}
	if identity.Interface != "iterm2" || identity.AgentBackend != "" {
		t.Fatalf("identity not switched to iTerm Claude: %+v", identity)
	}
}

type recordingTerminal struct {
	launches            []LaunchOptions
	resumePokegentCalls int
	resumeSessionCalls  int
}

func (t *recordingTerminal) FocusSession(_, _ string) error    { return nil }
func (t *recordingTerminal) WriteText(_, _, _ string) error    { return nil }
func (t *recordingTerminal) SetTabName(_, _, _ string) error   { return nil }
func (t *recordingTerminal) CloseSession(_, _ string) error    { return nil }
func (t *recordingTerminal) CloneSession(_, _ string) error    { return nil }
func (t *recordingTerminal) IsAvailable() bool                 { return true }
func (t *recordingTerminal) IsSessionFocused(_, _ string) bool { return false }
func (t *recordingTerminal) LaunchProfile(opts LaunchOptions) error {
	t.launches = append(t.launches, opts)
	return nil
}
func (t *recordingTerminal) ResumeSession(_, _, _ string) error { t.resumeSessionCalls++; return nil }
func (t *recordingTerminal) ResumePokegent(_, _, _, _ string) error {
	t.resumePokegentCalls++
	return nil
}
