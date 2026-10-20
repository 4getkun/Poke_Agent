package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockACPSession creates a minimal ChatSession with a fake ACP client
// for testing the WebSocket relay. The fake client records stdin writes
// and allows injecting stdout lines via broadcastWSRaw.
func mockACPSession(t *testing.T) (*ChatSession, *ChatManager) {
	t.Helper()

	bus := NewEventBus()
	mgr := NewChatManager(t.TempDir(), func() {}, bus, nil)

	sess := &ChatSession{
		PokegentID:   "test-pgid-12345678",
		ACPID:        "test-acp-session-id",
		Profile:      "test",
		smState:      "idle",
		subscribers:  make(map[chan ChatSessionEvent]struct{}),
		activeTasks:  make(map[string]json.RawMessage),
		pendingPerms: make(map[int64]*pendingPermission),
		wsClients:    make(map[*wsClient]struct{}),
	}

	mgr.mu.Lock()
	mgr.sessions["test-pgid-12345678"] = sess
	mgr.mu.Unlock()

	return sess, mgr
}

type fakeStdinWriter struct {
	mu    sync.Mutex
	lines [][]byte
}

func (f *fakeStdinWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	f.lines = append(f.lines, cp)
	return len(p), nil
}

func (f *fakeStdinWriter) Close() error { return nil }

func (f *fakeStdinWriter) getLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.lines))
	for i, l := range f.lines {
		out[i] = strings.TrimSpace(string(l))
	}
	return out
}

func TestWSConnectAndBootstrap(t *testing.T) {
	sess, mgr := mockACPSession(t)
	_ = sess

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/test-pgid-12345678/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer ws.Close()

	// Should receive ws_connected bootstrap frame
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal("read:", err)
	}

	var boot map[string]any
	if err := json.Unmarshal(msg, &boot); err != nil {
		t.Fatal("unmarshal:", err)
	}
	if boot["type"] != "ws_connected" {
		t.Errorf("want type ws_connected, got %v", boot["type"])
	}
	if boot["session_id"] != "test-acp-session-id" {
		t.Errorf("want session_id test-acp-session-id, got %v", boot["session_id"])
	}
	if boot["state"] != "idle" {
		t.Errorf("want state idle, got %v", boot["state"])
	}
}

func TestWSReceivesForwardedEvents(t *testing.T) {
	sess, mgr := mockACPSession(t)

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/test-pgid-12345678/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer ws.Close()

	// Drain the bootstrap frame
	ws.ReadMessage()

	// Simulate ACP sending a notification via broadcastWSRaw
	notif := `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"test","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}}`
	sess.broadcastWSRaw([]byte(notif))

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal("read:", err)
	}

	if string(msg) != notif {
		t.Errorf("want forwarded notification verbatim\ngot:  %s\nwant: %s", msg, notif)
	}
}

func TestWSForwardsToStdin(t *testing.T) {
	sess, mgr := mockACPSession(t)

	stdin := &fakeStdinWriter{}
	sess.client = &chatACPClient{
		stdin: stdin,
		done:  make(chan struct{}),
	}

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/test-pgid-12345678/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer ws.Close()

	// Drain bootstrap
	ws.ReadMessage()

	// Send a session/prompt request through WebSocket
	prompt := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"test","prompt":[{"type":"text","text":"hello"}]}}`
	if err := ws.WriteMessage(websocket.TextMessage, []byte(prompt)); err != nil {
		t.Fatal("write:", err)
	}

	// Give the handler goroutine time to process
	time.Sleep(100 * time.Millisecond)

	lines := stdin.getLines()
	if len(lines) == 0 {
		t.Fatal("expected prompt to be forwarded to ACP stdin")
	}
	if lines[0] != prompt {
		t.Errorf("stdin got:\n%s\nwant:\n%s", lines[0], prompt)
	}
}

func TestWSInterceptsFsRequests(t *testing.T) {
	sess, mgr := mockACPSession(t)

	stdin := &fakeStdinWriter{}
	sess.client = &chatACPClient{
		stdin: stdin,
		done:  make(chan struct{}),
	}

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/test-pgid-12345678/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal("dial:", err)
	}
	defer ws.Close()
	ws.ReadMessage() // drain bootstrap

	// Write a temp file to read back
	tmpFile := t.TempDir() + "/test.txt"
	if err := writeTestFile(tmpFile, "file contents"); err != nil {
		t.Fatal(err)
	}

	// Send fs/read_text_file through WebSocket — should be intercepted
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "fs/read_text_file",
		"params":  map[string]string{"path": tmpFile},
	})
	if err != nil {
		t.Fatal("marshal request:", err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatal("write:", err)
	}

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal("read response:", err)
	}

	var resp struct {
		ID     int64 `json:"id"`
		Result struct {
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatal("unmarshal:", err)
	}
	if resp.ID != 99 {
		t.Errorf("want id 99, got %d", resp.ID)
	}
	if resp.Result.Content != "file contents" {
		t.Errorf("want 'file contents', got %q", resp.Result.Content)
	}

	// Verify it was NOT forwarded to ACP stdin
	time.Sleep(50 * time.Millisecond)
	lines := stdin.getLines()
	for _, l := range lines {
		if strings.Contains(l, "fs/read_text_file") {
			t.Error("fs/read_text_file should not be forwarded to ACP stdin")
		}
	}
}

func TestWSMultipleClients(t *testing.T) {
	sess, mgr := mockACPSession(t)
	_ = sess

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/test-pgid-12345678/ws"

	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal("dial ws1:", err)
	}
	defer ws1.Close()
	ws1.ReadMessage() // bootstrap

	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal("dial ws2:", err)
	}
	defer ws2.Close()
	ws2.ReadMessage() // bootstrap

	// Both should receive forwarded events
	notif := `{"jsonrpc":"2.0","method":"claude/session_state","params":{"state":"busy"}}`
	sess.broadcastWSRaw([]byte(notif))

	ws1.SetReadDeadline(time.Now().Add(2 * time.Second))
	ws2.SetReadDeadline(time.Now().Add(2 * time.Second))

	_, msg1, err := ws1.ReadMessage()
	if err != nil {
		t.Fatal("ws1 read:", err)
	}
	_, msg2, err := ws2.ReadMessage()
	if err != nil {
		t.Fatal("ws2 read:", err)
	}

	if string(msg1) != notif {
		t.Errorf("ws1 got %s, want %s", msg1, notif)
	}
	if string(msg2) != notif {
		t.Errorf("ws2 got %s, want %s", msg2, notif)
	}
}

func TestWSClientCount(t *testing.T) {
	sess, mgr := mockACPSession(t)

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/test-pgid-12345678/ws"

	ws1, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	ws1.ReadMessage()
	time.Sleep(50 * time.Millisecond)

	sess.wsMu.RLock()
	count1 := len(sess.wsClients)
	sess.wsMu.RUnlock()
	if count1 != 1 {
		t.Errorf("want 1 client, got %d", count1)
	}

	ws2, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
	ws2.ReadMessage()
	time.Sleep(50 * time.Millisecond)

	sess.wsMu.RLock()
	count2 := len(sess.wsClients)
	sess.wsMu.RUnlock()
	if count2 != 2 {
		t.Errorf("want 2 clients, got %d", count2)
	}

	ws1.Close()
	time.Sleep(100 * time.Millisecond)

	sess.wsMu.RLock()
	count3 := len(sess.wsClients)
	sess.wsMu.RUnlock()
	if count3 != 1 {
		t.Errorf("want 1 client after disconnect, got %d", count3)
	}

	ws2.Close()
	time.Sleep(100 * time.Millisecond)

	sess.wsMu.RLock()
	count4 := len(sess.wsClients)
	sess.wsMu.RUnlock()
	if count4 != 0 {
		t.Errorf("want 0 clients after all disconnect, got %d", count4)
	}
}

func TestWS404ForUnknownSession(t *testing.T) {
	_, mgr := mockACPSession(t)

	srv := &Server{chatMgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chat/{id}/ws", srv.handleChatWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/chat/nonexistent/ws"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial to fail for unknown session")
	}
	if resp != nil && resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
