package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"pokegents/dashboard/server/store"
)

// TestRealACPRoundTripMigration is a gated, token-spending E2E. It launches
// real Codex ACP and real Claude ACP subprocesses in an isolated temp HOME and
// proves the live target model can see handoff context created from the prior
// backend's native transcript.
//
// It is skipped by default because it requires network/auth and spends model
// tokens. Run explicitly:
//
//	POKEGENTS_E2E_REAL_ACP=1 OPENAI_API_KEY=... go test ./server -run TestRealACPRoundTripMigration -count=1 -timeout=8m
//
// Isolation: the test does not bind dashboard ports and does not use the real
// ~/.pokegents, ~/.codex, or ~/.claude paths. The only intentionally inherited
// values are OPENAI_API_KEY and, when set, ANTHROPIC_API_KEY. If ANTHROPIC_API_KEY
// is unset, the Claude ACP subprocess is allowed to use the same Claude Code
// subscription auth you use in the terminal. Claude turns default to a full
// Sonnet model ID because Claude Code's subscription ACP path may not accept
// every short alias equally. Override with POKEGENTS_E2E_CLAUDE_MODEL when
// needed; the test refuses Opus unless POKEGENTS_E2E_ALLOW_OPUS=1.
func TestRealACPRoundTripMigration(t *testing.T) {
	if os.Getenv("POKEGENTS_E2E_REAL_ACP") != "1" {
		t.Skip("set POKEGENTS_E2E_REAL_ACP=1 to launch real Claude/Codex ACP backends and spend tokens")
	}
	originalHome, _ := os.UserHomeDir()
	sourceCodexHome := os.Getenv("POKEGENTS_E2E_CODEX_HOME")
	if sourceCodexHome == "" {
		sourceCodexHome = os.Getenv("CODEX_HOME")
	}
	sourceClaudeConfig := os.Getenv("POKEGENTS_E2E_CLAUDE_CONFIG_DIR")
	if sourceClaudeConfig == "" && os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		sourceClaudeConfig = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("INTERNAL_ASSIST_API_KEY") == "" && sourceCodexHome == "" {
		t.Skip("Codex auth required: set OPENAI_API_KEY, INTERNAL_ASSIST_API_KEY, or CODEX_HOME/POKEGENTS_E2E_CODEX_HOME")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skipf("npx not available: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not available: %v", err)
	}

	repoRoot := testRepoRoot(t)
	acpPath := filepath.Join(repoRoot, "dashboard", "acp-fork", "dist", "index.js")
	if _, err := os.Stat(acpPath); err != nil {
		t.Fatalf("Claude ACP fork not found at %s: %v", acpPath, err)
	}

	home := t.TempDir()
	dataDir := filepath.Join(home, ".pokegents")
	claudeProjectDir := filepath.Join(home, ".claude", "projects")
	codexHome := filepath.Join(home, ".codex")
	cwd := filepath.Join(home, "work")
	for _, dir := range []string{dataDir, claudeProjectDir, codexHome, cwd} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("POKEGENTS_ROOT", repoRoot)
	t.Setenv("POKEGENTS_CLAUDE_ACP_PATH", acpPath)
	if sourceClaudeConfig != "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		// Usually unset is best for subscription auth (the CLI uses HOME and
		// keychain/default config discovery). This override exists only for
		// explicit non-standard Claude config dirs.
		t.Setenv("CLAUDE_CONFIG_DIR", sourceClaudeConfig)
	}
	if sourceCodexHome != "" {
		copyCodexAuthConfig(t, sourceCodexHome, codexHome)
	}

	writeE2EBackends(t, dataDir, originalHome)

	s, err := NewServer(Config{
		DataDir:          dataDir,
		ClaudeProjectDir: claudeProjectDir,
		SearchDBPath:     filepath.Join(dataDir, "search.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.chatMgr.CloseAll(10 * time.Second)

	claudeModel := os.Getenv("POKEGENTS_E2E_CLAUDE_MODEL")
	if claudeModel == "" {
		claudeModel = "claude-sonnet-4-6"
	}
	if strings.Contains(strings.ToLower(claudeModel), "opus") && os.Getenv("POKEGENTS_E2E_ALLOW_OPUS") != "1" {
		t.Fatalf("refusing to run live E2E with Opus model %q; set POKEGENTS_E2E_ALLOW_OPUS=1 if intentional", claudeModel)
	}

	pgid := launchE2EChatAgent(t, s, "e2e-codex", claudeModel)
	sentinel := "POKEGENTS_E2E_SENTINEL_" + strings.ReplaceAll(pgid[:8], "-", "_")
	codexPrompt := "Remember this sentinel for migration: " + sentinel + ". Reply only with it."
	sendAndWaitForSummary(t, s, pgid, codexPrompt, sentinel, 3*time.Minute)
	waitForRunningTranscriptPath(t, dataDir, pgid, 45*time.Second)

	// Codex -> Claude. The implementation should snapshot Codex's transcript,
	// launch fresh Claude ACP, and inject the handoff context.
	switchBackend(t, s, pgid, "e2e-claude")
	waitForBackend(t, s, pgid, "e2e-claude", 90*time.Second)
	claudePrompt := "From migrated context, reply only with the sentinel."
	sendAndWaitForSummary(t, s, pgid, claudePrompt, sentinel, 3*time.Minute)

	// Claude -> Codex. This proves the same normalized handoff path works in
	// the opposite direction against live backends too.
	waitForClaudeTranscriptDiscoverable(t, s, pgid, 45*time.Second)
	switchBackend(t, s, pgid, "e2e-codex")
	waitForBackend(t, s, pgid, "e2e-codex", 90*time.Second)
	codexReturnPrompt := "From migrated context, reply only with the sentinel."
	sendAndWaitForSummary(t, s, pgid, codexReturnPrompt, sentinel, 3*time.Minute)
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func writeE2EBackends(t *testing.T, dataDir, originalHome string) {
	t.Helper()
	body := map[string]any{
		"backends": map[string]any{
			"e2e-codex": map[string]any{
				"name": "E2E Codex",
				// Use the ACP adapter in tests. The raw @openai/codex CLI can
				// require a TTY and fail with "stdin is not a terminal" under
				// Go's piped subprocess test harness.
				"type": "codex-acp",
				"env": map[string]string{
					"OPENAI_API_KEY":          os.Getenv("OPENAI_API_KEY"),
					"INTERNAL_ASSIST_API_KEY": os.Getenv("INTERNAL_ASSIST_API_KEY"),
				},
			},
			"e2e-claude": map[string]any{
				"name": "E2E Claude",
				"type": "claude-acp",
				"env":  optionalClaudeEnv(originalHome),
			},
		},
	}
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "backends.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyCodexAuthConfig(t *testing.T, srcHome, dstHome string) {
	t.Helper()
	for _, name := range []string{"config.toml", "auth.json", "credentials.json"} {
		src := filepath.Join(srcHome, name)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dstHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstHome, name), data, 0o600); err != nil {
			t.Fatalf("copy Codex config %s: %v", name, err)
		}
	}
}

func optionalClaudeEnv(originalHome string) map[string]string {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return map[string]string{"ANTHROPIC_API_KEY": key}
	}
	env := map[string]string{}
	if originalHome != "" {
		// Claude Code subscription auth is often stored via mechanisms keyed off
		// the user's real HOME. Use it only for the Claude subprocess when no
		// API key is provided; pokegents state, Codex home, and transcripts stay
		// explicitly routed to temp paths via the other env vars.
		env["HOME"] = originalHome
	}
	return env
}

func launchE2EChatAgent(t *testing.T, s *Server, backend, claudeModel string) string {
	t.Helper()
	body := fmt.Sprintf(`{"profile":"reviewer","name":"E2E Roundtrip","interface":"chat","agent_backend":%q,"model":%q,"effort":"low"}`, backend, claudeModel)
	req := httptest.NewRequest(http.MethodPost, "/api/pokegents/launch", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	s.handleUnifiedLaunch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("launch %s status=%d body=%s", backend, rec.Code, rec.Body.String())
	}
	var resp LaunchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode launch response: %v", err)
	}
	if resp.PokegentID == "" {
		t.Fatalf("launch response missing pokegent_id: %s", rec.Body.String())
	}
	return resp.PokegentID
}

func switchBackend(t *testing.T, s *Server, pgid, backend string) {
	t.Helper()
	e2eCooldown(t, "before switching backend to "+backend)
	if err := s.state.LoadAll(); err != nil {
		t.Fatalf("LoadAll before switch: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+pgid+"/switch-backend", strings.NewReader(fmt.Sprintf(`{"backend":%q}`, backend)))
	req.SetPathValue("id", pgid)
	rec := httptest.NewRecorder()
	s.handleSwitchBackend(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("switch to %s status=%d body=%s", backend, rec.Code, rec.Body.String())
	}
}

func waitForBackend(t *testing.T, s *Server, pgid, backend string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sess := s.chatMgr.Get(pgid); sess != nil {
			if err := s.state.LoadAll(); err == nil {
				if rs, err := s.fileStore.Running.GetByPokegentID(pgid); err == nil &&
					rs.AgentBackend == backend && rs.SessionID != "" && sess.ACPID == rs.SessionID {
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for backend %s", backend)
}

func sendAndWaitForSummary(t *testing.T, s *Server, pgid, prompt, want string, timeout time.Duration) {
	t.Helper()
	e2eCooldown(t, "before sending prompt")
	sess := s.chatMgr.Get(pgid)
	if sess == nil {
		t.Fatalf("chat session not found for %s", pgid)
	}
	if err := sess.SendPrompt(prompt); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	deadline := time.Now().Add(timeout)
	var lastSummary string
	for time.Now().Before(deadline) {
		waitChatIdle(t, sess, 500*time.Millisecond)
		_ = s.state.LoadAll()
		if rs, err := s.fileStore.Running.GetByPokegentID(pgid); err == nil {
			path := rs.TranscriptPath
			if path == "" && rs.SessionID != "" {
				path = store.FindTranscriptPath(rs.SessionID, s.state.claudeProjectDir)
			}
			if path != "" {
				_, summary := extractLastMessages(path)
				if summary != "" {
					lastSummary = summary
				}
				if strings.Contains(summary, want) {
					return
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("timed out waiting for summary containing %q; last summary=%q", want, lastSummary)
}

func e2eCooldown(t *testing.T, reason string) {
	t.Helper()
	d := 15 * time.Second
	if raw := strings.TrimSpace(os.Getenv("POKEGENTS_E2E_COOLDOWN_SECONDS")); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("invalid POKEGENTS_E2E_COOLDOWN_SECONDS=%q", raw)
		}
		if secs < 0 {
			t.Fatalf("POKEGENTS_E2E_COOLDOWN_SECONDS must be >= 0, got %d", secs)
		}
		d = time.Duration(secs) * time.Second
	}
	if d == 0 {
		return
	}
	t.Logf("E2E cooldown %s: %s", reason, d)
	time.Sleep(d)
}

func waitChatIdle(t *testing.T, sess *ChatSession, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sess.smMu.Lock()
		state := sess.smState
		sess.smMu.Unlock()
		if state == "idle" || state == "error" {
			return state == "idle"
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func waitForRunningTranscriptPath(t *testing.T, dataDir, pgid string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rs := readRunningByPokegentForTest(t, dataDir, pgid)
		if rs.TranscriptPath != "" {
			if _, err := os.Stat(rs.TranscriptPath); err == nil {
				return rs.TranscriptPath
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("timed out waiting for transcript_path in running file for %s", pgid)
	return ""
}

func waitForClaudeTranscriptDiscoverable(t *testing.T, s *Server, pgid string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = s.state.LoadAll()
		if rs, err := s.fileStore.Running.GetByPokegentID(pgid); err == nil && rs.SessionID != "" {
			if path := store.FindTranscriptPath(rs.SessionID, s.state.claudeProjectDir); path != "" {
				return path
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("timed out waiting for Claude transcript for %s", pgid)
	return ""
}

func readRunningByPokegentForTest(t *testing.T, dataDir, pgid string) RunningSession {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dataDir, "running", "*-"+pgid+".json"))
	if len(matches) == 0 {
		t.Fatalf("running file for %s not found", pgid)
	}
	return readTestJSON[RunningSession](t, matches[0])
}
