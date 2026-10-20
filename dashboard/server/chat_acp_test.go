package server

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildACPPromptBlocks_NoImages — the common case: plain text becomes a
// single text content block.
func TestBuildACPPromptBlocks_NoImages(t *testing.T) {
	blocks, err := buildACPPromptBlocks("hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d (%v)", len(blocks), blocks)
	}
	got, _ := blocks[0].(map[string]any)
	if got["type"] != "text" || got["text"] != "hello world" {
		t.Errorf("unexpected text block: %+v", got)
	}
}

// TestBuildACPPromptBlocks_OnlyImage — a bare token with no surrounding
// prose should produce just an image block (no empty text padding).
func TestBuildACPPromptBlocks_OnlyImage(t *testing.T) {
	tmp := writeTempPNG(t, []byte{0x89, 0x50, 0x4E, 0x47}) // PNG magic; bytes irrelevant
	blocks, err := buildACPPromptBlocks("[Image: " + tmp + "]")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d (%v)", len(blocks), blocks)
	}
	got, _ := blocks[0].(map[string]any)
	if got["type"] != "image" {
		t.Errorf("want image type, got %v", got["type"])
	}
	if got["mimeType"] != "image/png" {
		t.Errorf("want image/png mime, got %v", got["mimeType"])
	}
	wantData := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4E, 0x47})
	if got["data"] != wantData {
		t.Errorf("base64 mismatch: got %v", got["data"])
	}
}

// TestBuildACPPromptBlocks_TextThenImage — surrounding text gets its own
// blocks, with whitespace trimmed, and the image rides between them.
func TestBuildACPPromptBlocks_TextThenImage(t *testing.T) {
	tmp := writeTempPNG(t, []byte("png-data"))
	blocks, err := buildACPPromptBlocks("look at [Image: " + tmp + "] and tell me")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d (%v)", len(blocks), blocks)
	}
	first, _ := blocks[0].(map[string]any)
	mid, _ := blocks[1].(map[string]any)
	last, _ := blocks[2].(map[string]any)
	if first["type"] != "text" || first["text"] != "look at" {
		t.Errorf("first: want text 'look at', got %+v", first)
	}
	if mid["type"] != "image" {
		t.Errorf("mid: want image, got %+v", mid)
	}
	if last["type"] != "text" || last["text"] != "and tell me" {
		t.Errorf("last: want text 'and tell me', got %+v", last)
	}
}

// TestBuildACPPromptBlocks_MissingFile — when an image token references a
// non-existent path, fall back to including the literal token as text so
// the agent at least sees the path; never error out.
func TestBuildACPPromptBlocks_MissingFile(t *testing.T) {
	blocks, err := buildACPPromptBlocks("hi [Image: /tmp/does-not-exist-12345.png] there")
	if err != nil {
		t.Fatal(err)
	}
	// Three blocks: "hi", literal-text-fallback, "there"
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	mid, _ := blocks[1].(map[string]any)
	if mid["type"] != "text" {
		t.Errorf("missing-file fallback should be text, got %v", mid["type"])
	}
}

// TestMimeForImagePath — extension → mime mapping. Defends image paste
// against silent breakage when adding new image formats.
func TestMimeForImagePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/tmp/x.png", "image/png"},
		{"/tmp/x.PNG", "image/png"},
		{"/tmp/x.jpg", "image/jpeg"},
		{"/tmp/x.jpeg", "image/jpeg"},
		{"/tmp/x.gif", "image/gif"},
		{"/tmp/x.webp", "image/webp"},
		{"/tmp/x.bin", "application/octet-stream"},
		{"/tmp/x", "application/octet-stream"},
	}
	for _, c := range cases {
		if got := mimeForImagePath(c.in); got != c.want {
			t.Errorf("mimeForImagePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExtractCwdFromJSONL — first non-null cwd entry wins; pre-cwd
// metadata entries (custom-title, agent-name) are skipped.
func TestExtractCwdFromJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	lines := []string{
		`{"type":"custom-title","cwd":null}`,
		`{"type":"agent-name","cwd":null}`,
		`{"type":"system","cwd":"/home/user/projects"}`,
		`{"type":"user","cwd":"/home/user/projects"}`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := extractCwdFromJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/user/projects" {
		t.Errorf("got %q, want /home/user/projects", got)
	}
}

// TestExtractCwdFromJSONL_AllNull — when every entry has a null cwd we
// return an error so callers can refuse to launch with a bad cwd.
func TestExtractCwdFromJSONL_AllNull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jsonl")
	body := `{"type":"custom-title","cwd":null}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := extractCwdFromJSONL(path)
	if err == nil {
		t.Errorf("expected error for all-null cwds")
	}
}

// TestChatVerbLabel — known kinds map to the same compact labels the chat
// transcript parser uses. Regression here would break card/chat consistency.
func TestChatVerbLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"execute", "Bash"},
		{"read", "Read"},
		{"edit", "Update"},
		{"search", "Search"},
		{"fetch", "Fetch"},
		{"think", "Agent"},
		{"other", "Tool"},
		{"", ""},
		{"weirdkind", "Weirdkind"},
	}
	for _, c := range cases {
		if got := chatVerbLabel(c.in); got != c.want {
			t.Errorf("chatVerbLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestChatToolArgs — prefers known argv fields over locations; falls back
// to first location path; returns "" when nothing useful is present.
func TestChatToolArgs(t *testing.T) {
	loc := []struct {
		Path string `json:"path"`
	}{{Path: "/tmp/foo"}}
	cases := []struct {
		name      string
		raw       string
		locations []struct {
			Path string `json:"path"`
		}
		want string
	}{
		{"command wins", `{"command":"ls -la"}`, loc, "ls -la"},
		{"codex cmd", `{"cmd":"git status --short"}`, loc, "git status --short"},
		{"file_path next", `{"file_path":"/etc/hosts"}`, loc, "/etc/hosts"},
		{"falls back to location", `{}`, loc, "/tmp/foo"},
		{"empty everywhere", `{}`, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := chatToolArgs(json.RawMessage(c.raw), c.locations)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCompactSessionUpdateFrame_OmitsCodexExecOutput(t *testing.T) {
	line := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"call_1","title":"exec_command","status":"completed","rawInput":{"cmd":"git status --short","workdir":"/tmp/project"},"content":[{"type":"terminal","terminalOutput":"very large output that should not reach the browser"}],"rawOutput":"very large raw output"}}}`)

	got := compactSessionUpdateFrame(line)
	if got == nil {
		t.Fatal("compactSessionUpdateFrame returned nil")
	}

	var frame struct {
		Params struct {
			Update map[string]any `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal(got, &frame); err != nil {
		t.Fatal(err)
	}
	update := frame.Params.Update
	if update["title"] != "Git" {
		t.Fatalf("title = %v, want Git", update["title"])
	}
	if update["rawOutput"] != "[exec output omitted by dashboard]" {
		t.Fatalf("rawOutput was not omitted: %v", update["rawOutput"])
	}
	content, _ := update["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	first, _ := content[0].(map[string]any)
	if first["terminalOutput"] != "[exec output omitted by dashboard]" {
		t.Fatalf("terminalOutput was not omitted: %v", first["terminalOutput"])
	}
}

func TestPatchRunningFileChatPreservesVerifiedTranscriptAsLastGood(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, ".pokegents")
	runningDir := filepath.Join(dataDir, "running")
	if err := os.MkdirAll(runningDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "rollout-2026-05-06T17-01-59-019dffbd-c486-7910-a997-1248f16b1f59.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"session_meta"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runningPath := filepath.Join(runningDir, "engineer@general-pg-1.json")
	initial := map[string]any{
		"profile":         "engineer@general",
		"pokegent_id":     "pg-1",
		"session_id":      "019dffbd-c486-7910-a997-1248f16b1f59",
		"transcript_path": transcript,
		"interface":       "chat",
		"agent_backend":   "custom-codex-model",
	}
	b, _ := json.Marshal(initial)
	if err := os.WriteFile(runningPath, b, 0o644); err != nil {
		t.Fatal(err)
	}

	patchRunningFileChat(dataDir, "pg-1", "engineer@general", 1234, "019e0092-fba6-72f3-969a-29b3b7117292", "/tmp/project", "", "custom-codex-model")

	var got map[string]any
	raw, err := os.ReadFile(runningPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["session_id"] != "019e0092-fba6-72f3-969a-29b3b7117292" {
		t.Fatalf("session_id = %v", got["session_id"])
	}
	if got["transcript_path"] != transcript {
		t.Fatalf("transcript_path = %v, want preserved %s", got["transcript_path"], transcript)
	}
	if got["last_good_session_id"] != "019dffbd-c486-7910-a997-1248f16b1f59" {
		t.Fatalf("last_good_session_id = %v", got["last_good_session_id"])
	}
	if got["last_good_transcript_path"] != transcript {
		t.Fatalf("last_good_transcript_path = %v", got["last_good_transcript_path"])
	}
}

func TestSessionIDFromTranscriptPath(t *testing.T) {
	path := "/home/user/.pokegents/codex-homes/custom-codex-model/sessions/2026/05/06/rollout-2026-05-06T17-01-59-019dffbd-c486-7910-a997-1248f16b1f59.jsonl"
	want := "019dffbd-c486-7910-a997-1248f16b1f59"
	if got := sessionIDFromTranscriptPath(path); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestCompleteBrowserPrompt_IgnoresUntrackedResponses(t *testing.T) {
	sess := &ChatSession{
		smState:          "busy",
		browserPromptIDs: make(map[int64]string),
		wsClients:        make(map[*wsClient]struct{}),
	}

	if sess.completeBrowserPrompt(99, nil) {
		t.Fatal("untracked response should not complete a turn")
	}
	if sess.smState != "busy" {
		t.Fatalf("smState = %q, want busy", sess.smState)
	}

	sess.trackBrowserPrompt(42, "hello")
	if !sess.completeBrowserPrompt(42, nil) {
		t.Fatal("tracked prompt response should complete a turn")
	}
	if sess.smState != "done" {
		t.Fatalf("smState = %q, want done", sess.smState)
	}
}

func TestCompleteBrowserPrompt_ErrorMarksError(t *testing.T) {
	dir := t.TempDir()
	sess := &ChatSession{
		PokegentID:         "agent-1",
		ACPID:              "session-1",
		dataDir:            dir,
		smState:            "busy",
		browserPromptIDs:   make(map[int64]string),
		wsClients:          make(map[*wsClient]struct{}),
		currentDetail:      "thinking…",
		lastSummaryStaging: "partial text",
	}
	sess.stderrTail = []string{`ERROR codex_acp::thread: stream disconnected before completion: response.failed event received`}
	sess.trackBrowserPrompt(7, "hello")
	if !sess.completeBrowserPrompt(7, &chatJSONRPCError{Code: -32603, Message: "Internal error"}) {
		t.Fatal("tracked prompt response should complete a turn")
	}
	if sess.smState != "error" {
		t.Fatalf("smState = %q, want error", sess.smState)
	}
	if !strings.Contains(sess.currentDetail, "model stream failed") {
		t.Fatalf("currentDetail = %q, want model stream failure hint", sess.currentDetail)
	}
	if sess.lastSummary != "partial text" {
		t.Fatalf("lastSummary = %q, want partial text preserved", sess.lastSummary)
	}
}

func TestFormatBrowserPromptError_ContentFilter(t *testing.T) {
	got := formatBrowserPromptError(
		&chatJSONRPCError{Code: -32603, Message: "Internal error"},
		`ERROR codex_acp::thread: stream disconnected before completion: Incomplete response returned, reason: content_filter`,
	)
	if !strings.Contains(got, "blocked by model content filter") {
		t.Fatalf("got %q, want content filter hint", got)
	}
}

func TestFormatACPStderrSystemMessage_ParseArgs(t *testing.T) {
	got := formatACPStderrSystemMessage(`ERROR codex_core::tools::router: error=failed to parse function arguments: EOF while parsing a string at line 1 column 1690`)
	if !strings.Contains(got, "malformed tool-call JSON") {
		t.Fatalf("got %q, want malformed JSON hint", got)
	}
}

// TestEffortToThinkingConfig pins the pokegents-effort → SDK-thinking-config
// mapping. Frontend writes "low/medium/high/max" into role/project configs,
// and the chat backend translates here. A regression here silently breaks
// model effort for chat agents (the wrapper just runs on SDK defaults).
func TestEffortToThinkingConfig(t *testing.T) {
	cases := []struct {
		in         string
		wantType   string
		wantBudget int // 0 if not applicable
	}{
		{"low", "enabled", 4000},
		{"medium", "enabled", 10000},
		{"high", "enabled", 32000},
		{"max", "adaptive", 0},
		{"", "", 0},
		{"unknown", "", 0},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := effortToThinkingConfig(c.in)
			if c.wantType == "" {
				if got != nil {
					t.Errorf("effortToThinkingConfig(%q) = %+v, want nil", c.in, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("effortToThinkingConfig(%q) = nil, want type=%q", c.in, c.wantType)
			}
			if got["type"] != c.wantType {
				t.Errorf("type: got %v, want %q", got["type"], c.wantType)
			}
			if c.wantBudget > 0 {
				if budget, _ := got["budgetTokens"].(int); budget != c.wantBudget {
					t.Errorf("budgetTokens: got %v, want %d", got["budgetTokens"], c.wantBudget)
				}
			}
		})
	}
}

// writeTempPNG creates a temp file with the given bytes and returns its
// path. Cleanup is automatic via t.TempDir.
func writeTempPNG(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.png")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifyCommandMatchesChatPreviewLabels(t *testing.T) {
	cases := []struct{ cmd, want string }{
		{"sed -n '1,20p' dashboard/server/chat_acp.go", "Read"},
		{"awk '{print $1}' file.txt", "Read"},
		{"rg \"foo\" dashboard", "Search"},
		{"python3 scripts/check.py", "Run"},
		{"npm test", "Run"},
		{"git status", "Git"},
		{"ls -la", "List"},
		{"echo hi", "Bash"},
	}
	for _, c := range cases {
		if got := classifyCommand(c.cmd); got != c.want {
			t.Errorf("classifyCommand(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}
