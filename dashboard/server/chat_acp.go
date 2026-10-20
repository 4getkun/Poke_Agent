package server

// Phase 3 — Chat-backed pokegent supervisor (thin relay).
//
// Spawns one `@zed-industries/claude-agent-acp` subprocess per chat-mode
// pokegent and bridges it to the dashboard:
//
//   - Speaks JSON-RPC over NDJSON on the subprocess's stdio (Phase 0 spike #2
//     verified the wire format).
//   - Sends `_meta.systemPrompt.append` for Claude ACP and passes
//     `model_instructions_file` for Codex ACP so role/project prompts apply.
//   - Sniffs session/update notifications for AgentCard status fields.
//   - Relays raw ACP frames to WebSocket clients (browser owns full state).
//   - Handles fs/* requests server-side.
//   - Mailboxes are keyed by pokegent_id.
//
// Identity / running / status files are written by the unified launch
// endpoint (launch.go) BEFORE this supervisor spawns the subprocess
// (Principle 6). The supervisor just patches `claude_pid` once available
// and updates state on session/update.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pokegents/dashboard/server/store"
)

// Tunables. Most are documented inline at point-of-use; collected here so
// they're easy to find when diagnosing slow turns or oversized buffers.
const (
	// chatStderrTailLines bounds the rolling buffer of subprocess stderr
	// kept for crash diagnostics.
	chatStderrTailLines = 32
	// chatPromptTimeout is the upper bound on a single ACP `session/prompt`
	// round-trip. Long Claude turns + tool calls can exceed default HTTP
	// request timeouts; we detach into a goroutine bounded by this. Override
	// via env var POKEGENTS_CHAT_PROMPT_TIMEOUT (Go duration syntax).
	chatPromptTimeout = 30 * time.Minute
)

// ── JSON-RPC envelopes ─────────────────────────────────────

type chatJSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type chatRawFrame struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      *int64            `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Params  json.RawMessage   `json:"params,omitempty"`
	Result  json.RawMessage   `json:"result,omitempty"`
	Error   *chatJSONRPCError `json:"error,omitempty"`
}

type chatRPCResponse struct {
	Result json.RawMessage
	Error  *chatJSONRPCError
}

// -- ACP transport -----------------------------------------------------------

type chatACPClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex
	nextID  atomic.Int64
	pending sync.Map // int64 → chan chatRPCResponse

	onNotif             func(method string, params json.RawMessage)
	onReq               func(method string, params json.RawMessage) (any, *chatJSONRPCError)
	onRawLine           func(line []byte)
	onUnmatchedResponse func(id int64, errResp *chatJSONRPCError) // called when a response arrives for an ID not in c.pending

	closed atomic.Bool
	done   chan struct{}
}

func (c *chatACPClient) sendRequest(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, fmt.Errorf("acp client closed")
	}
	id := c.nextID.Add(1)
	pb, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	frame := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", id, method, pb}
	line, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}

	respCh := make(chan chatRPCResponse, 1)
	c.pending.Store(id, respCh)
	defer c.pending.Delete(id)

	if err := c.writeLine(line); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("acp subprocess exited")
	case resp := <-respCh:
		if resp.Error != nil {
			return nil, fmt.Errorf("acp error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (c *chatACPClient) sendNotification(method string, params any) error {
	if c.closed.Load() {
		return fmt.Errorf("acp client closed")
	}
	pb, err := json.Marshal(params)
	if err != nil {
		return err
	}
	frame := struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{"2.0", method, pb}
	line, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.writeLine(line)
}

func (c *chatACPClient) sendResponse(id int64, result any, errResp *chatJSONRPCError) error {
	if c.closed.Load() {
		return fmt.Errorf("acp client closed")
	}
	var line []byte
	if errResp != nil {
		frame := struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      int64             `json:"id"`
			Error   *chatJSONRPCError `json:"error"`
		}{"2.0", id, errResp}
		var err error
		line, err = json.Marshal(frame)
		if err != nil {
			return err
		}
	} else {
		rb, err := json.Marshal(result)
		if err != nil {
			return err
		}
		frame := struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int64           `json:"id"`
			Result  json.RawMessage `json:"result"`
		}{"2.0", id, rb}
		line, err = json.Marshal(frame)
		if err != nil {
			return err
		}
	}
	return c.writeLine(line)
}

func (c *chatACPClient) writeLine(line []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func (c *chatACPClient) readLoop() {
	defer close(c.done)
	defer c.closed.Store(true)
	sc := bufio.NewScanner(c.stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if c.onRawLine != nil {
			c.onRawLine(line)
		}
		var raw chatRawFrame
		if err := json.Unmarshal(line, &raw); err != nil {
			log.Printf("acp: bad frame: %v (%s)", err, string(line))
			continue
		}
		switch {
		case raw.Method == "" && raw.ID != nil:
			if chAny, ok := c.pending.LoadAndDelete(*raw.ID); ok {
				ch := chAny.(chan chatRPCResponse)
				ch <- chatRPCResponse{Result: raw.Result, Error: raw.Error}
			} else {
				if c.onUnmatchedResponse != nil {
					c.onUnmatchedResponse(*raw.ID, raw.Error)
				}
			}
		case raw.Method != "" && raw.ID != nil:
			id, method, params := *raw.ID, raw.Method, raw.Params
			go func() {
				if c.onReq == nil {
					_ = c.sendResponse(id, nil, &chatJSONRPCError{Code: -32601, Message: "method not found"})
					return
				}
				result, errResp := c.onReq(method, params)
				_ = c.sendResponse(id, result, errResp)
			}()
		case raw.Method != "" && raw.ID == nil:
			if strings.HasPrefix(raw.Method, "claude/") || raw.Method == "session/update" {
				log.Printf("acp-notif: %s (len=%d)", raw.Method, len(raw.Params))
			}
			if c.onNotif != nil {
				c.onNotif(raw.Method, raw.Params)
			}
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("acp: read error: %v", err)
	}
}

// ── Chat session (one chat-backed pokegent) ────────────────

// ChatSessionEvent is kept for compatibility with existing WebSocket tests.
type ChatSessionEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type ChatSession struct {
	PokegentID   string
	ACPID        string // ACP server's session ID (different from pokegent_id; opaque)
	Profile      string
	Cwd          string
	AgentBackend string // backend ID from backends.json (e.g., "codex", "claude")
	Created      time.Time
	dataDir      string

	client *chatACPClient

	lastUpdated atomic.Int64 // unix millis

	// Kept for chat_ws_test.go compatibility — no longer actively used for SSE.
	subsMu      sync.Mutex
	subscribers map[chan ChatSessionEvent]struct{}

	// Active background tasks — keyed by taskId. Populated from
	// claude/task_started, removed on claude/task_notification.
	tasksMu     sync.Mutex
	activeTasks map[string]json.RawMessage

	// Status / activity translation buffers. Updated on session/update events
	// and flushed to disk on each turn boundary. Same field set as the bash
	// hooks' status writer (hooks/status-update.sh) so the frontend never
	// branches on backend.
	stateMu            sync.Mutex
	recentActions      []string
	activityFeed       []ActivityItem
	contextTokens      int
	contextWindow      int
	lastSummary        string
	lastSummaryStaging string // accumulates during a turn; committed on busy→idle
	lastTrace          string
	currentDetail      string
	currentPrompt      string

	initialPromptMu      sync.Mutex
	initialPromptContext string

	// Kept for chat_ws_test.go / chat_ws.go compatibility — auto-allow means
	// this map is always empty, but DeliverPermission must still compile.
	permMu       sync.Mutex
	pendingPerms map[int64]*pendingPermission

	// intentionalClose is set by Close() to mark a deliberate teardown
	// (migration, user delete). The exit handler checks this and SKIPS
	// status/running file cleanup when true — those are owned by whatever
	// flow initiated the close. Without this, a migration that closes the
	// chat subprocess deletes the new presentation's freshly-written
	// running file in an out-of-order goroutine.
	intentionalClose atomic.Bool

	// stderrTail keeps the last ~32 lines of subprocess stderr so we can log
	// them when the subprocess exits unexpectedly. Without this, silent
	// crashes (e.g. "session_id not found" → SDK aborts) leave no trace and
	// we get a useless `read |0: file already closed` after the fact.
	stderrMu   sync.Mutex
	stderrTail []string

	// State machine — single source of truth for busy/idle
	smMu        sync.Mutex
	smState     string // "idle" | "busy" | "done" | "error"
	smBusySince time.Time

	// Phase 1: debounce status file writes — streaming chunks fire
	// translateUpdate on every chunk; the debouncer coalesces writes to
	// a 500ms trailing edge. State transitions (busy↔idle) flush immediately.
	statusDebounce *time.Timer

	// Phase 1: direct SSE broadcast to dashboard EventBus, bypassing
	// the file→fsnotify→rebuild indirection for state transitions.
	dashboardBus *EventBus

	usageLog *UsageLogger
	notifyFn func(pgid, state, name, summary string)

	// WebSocket relay — forwards raw ACP stdio frames to/from browser.
	wsMu      sync.RWMutex
	wsClients map[*wsClient]struct{}

	// Browser-submitted session/prompt request ids. Only responses for these
	// ids are allowed to mark the turn done; other unmatched responses can be
	// ACP housekeeping and must not clear the busy indicator.
	browserPromptMu  sync.Mutex
	browserPromptIDs map[int64]string
}

type pendingPermission struct {
	requestID int64
	payload   json.RawMessage
	ch        chan permissionDecision
}

type permissionDecision struct {
	OptionID  string
	Cancelled bool
}

func (s *ChatSession) addWSClient(c *wsClient) {
	s.wsMu.Lock()
	s.wsClients[c] = struct{}{}
	n := len(s.wsClients)
	s.wsMu.Unlock()
	log.Printf("chat-ws[%s]: client connected (now %d)", shortChat(s.PokegentID), n)
}

func (s *ChatSession) removeWSClient(c *wsClient) {
	s.wsMu.Lock()
	delete(s.wsClients, c)
	n := len(s.wsClients)
	s.wsMu.Unlock()
	log.Printf("chat-ws[%s]: client disconnected (now %d)", shortChat(s.PokegentID), n)
}

func (s *ChatSession) broadcastWSRaw(line []byte) {
	s.wsMu.RLock()
	defer s.wsMu.RUnlock()
	for c := range s.wsClients {
		if err := c.writeRaw(line); err != nil {
			log.Printf("chat-ws[%s]: write error: %v", shortChat(s.PokegentID), err)
		}
	}
}

func (s *ChatSession) trackBrowserPrompt(id int64, prompt string) {
	s.browserPromptMu.Lock()
	if s.browserPromptIDs == nil {
		s.browserPromptIDs = make(map[int64]string)
	}
	s.browserPromptIDs[id] = prompt
	s.browserPromptMu.Unlock()
}

func isCompactPrompt(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	return len(fields) > 0 && fields[0] == "/compact"
}

func (s *ChatSession) completeBrowserPrompt(id int64, errResp *chatJSONRPCError) bool {
	s.browserPromptMu.Lock()
	prompt, ok := s.browserPromptIDs[id]
	if ok {
		delete(s.browserPromptIDs, id)
	}
	s.browserPromptMu.Unlock()
	if !ok {
		return false
	}

	s.smMu.Lock()
	if errResp != nil {
		s.smState = "error"
		s.smBusySince = time.Time{}
	} else if s.smState == "busy" {
		s.smState = "done"
		s.smBusySince = time.Time{}
	}
	state := s.smState
	busySince := s.smBusySince
	s.smMu.Unlock()
	s.stateMu.Lock()
	if errResp == nil && isCompactPrompt(prompt) {
		// A successful context compaction should make the CTX/HP bar read as
		// full again. Keep the last known context window, but reset used tokens
		// to zero until the backend reports fresh usage on the next turn.
		s.contextTokens = 0
		s.lastSummary = "Compacted"
		s.currentDetail = "finished"
	} else if s.lastSummaryStaging != "" {
		s.lastSummary = s.lastSummaryStaging
	}
	s.lastSummaryStaging = ""
	if errResp != nil {
		s.currentDetail = formatBrowserPromptError(errResp, s.recentStderrError())
	}
	s.writeStatusFileLocked()
	s.stateMu.Unlock()
	s.publishAgentStatePatchWith(state, busySince)
	if wsFrame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "claude/session_state",
		"params":  map[string]any{"state": state},
	}); err == nil {
		s.broadcastWSRaw(wsFrame)
	}
	if refetch, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "pokegents/refetch_transcript",
	}); err == nil {
		s.broadcastWSRaw(refetch)
	}
	return true
}

func (s *ChatSession) recentStderrError() string {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	for i := len(s.stderrTail) - 1; i >= 0; i-- {
		line := stripANSI(s.stderrTail[i])
		if strings.Contains(line, "ERROR") ||
			strings.Contains(line, "response.failed") ||
			strings.Contains(line, "stream disconnected") ||
			strings.Contains(line, "content_filter") ||
			strings.Contains(line, "failed to parse function arguments") {
			return line
		}
	}
	return ""
}

func formatBrowserPromptError(errResp *chatJSONRPCError, stderrLine string) string {
	if errResp == nil {
		return ""
	}
	msg := strings.TrimSpace(errResp.Message)
	if msg == "" {
		msg = "ACP prompt failed"
	}
	if strings.Contains(stderrLine, "content_filter") {
		return msg + " — blocked by model content filter; reframe the task as defensive/summarization work and avoid generating operational harmful content"
	}
	if strings.Contains(stderrLine, "failed to parse function arguments") {
		return msg + " — Codex emitted malformed tool-call JSON; retry with a smaller/simpler command or ask the agent to use a short script file"
	}
	if strings.Contains(stderrLine, "Custom tool call output is missing") {
		return msg + " — Codex transcript is missing a tool-result record from an interrupted prior turn; restart this backend so Pokegents can repair the transcript before resume"
	}
	if strings.Contains(stderrLine, "response.failed") || strings.Contains(stderrLine, "stream disconnected") {
		return msg + " — model stream failed before completion; try /compact or reduce sensitive/large tool output"
	}
	return fmt.Sprintf("acp error %d: %s", errResp.Code, msg)
}

func formatACPStderrSystemMessage(line string) string {
	if strings.Contains(line, "content_filter") {
		return line + "\nHint: this turn was blocked by the model content filter. For red-team notebooks, ask for defensive analysis, summaries, or non-operational refactors; avoid prompts that generate or improve jailbreak/attack behavior."
	}
	if strings.Contains(line, "failed to parse function arguments") {
		return line + "\nHint: Codex emitted malformed tool-call JSON, often from a very long inline command/heredoc. Ask it to create or run a short script file instead of embedding a huge command."
	}
	if strings.Contains(line, "Custom tool call output is missing") {
		return line + "\nHint: this is an orphaned Codex transcript tool call, usually from a backend restart/kill during a tool call. Restart this backend once; Pokegents will repair the missing tool-result record before resuming."
	}
	if strings.Contains(line, "response.failed") || strings.Contains(line, "stream disconnected before completion") {
		return line + "\nHint: the model stream failed before completion. If this follows large or policy-sensitive tool output, try /compact and avoid printing bulky raw excerpts."
	}
	return line
}

// DeliverPermission is kept for chat_ws.go compatibility. With auto-allow
// enabled, no permissions are ever parked so this always returns false.
func (s *ChatSession) DeliverPermission(reqID int64, optionID string, cancelled bool) bool {
	s.permMu.Lock()
	pp, ok := s.pendingPerms[reqID]
	s.permMu.Unlock()
	if !ok {
		return false
	}
	select {
	case pp.ch <- permissionDecision{OptionID: optionID, Cancelled: cancelled}:
		return true
	default:
		return false // already resolved
	}
}

func (s *ChatSession) handleNotification(method string, params json.RawMessage) {
	// Sniff claude/* extension notifications for status/state updates.
	if strings.HasPrefix(method, "claude/") {
		// Track active background tasks for replay on new subscriber connect.
		if method == "claude/task_started" {
			var t struct {
				TaskId string `json:"taskId"`
			}
			if json.Unmarshal(params, &t) == nil && t.TaskId != "" {
				s.tasksMu.Lock()
				s.activeTasks[t.TaskId] = params
				s.tasksMu.Unlock()
			}
		} else if method == "claude/task_notification" {
			var t struct {
				TaskId string `json:"taskId"`
			}
			if json.Unmarshal(params, &t) == nil && t.TaskId != "" {
				s.tasksMu.Lock()
				delete(s.activeTasks, t.TaskId)
				s.tasksMu.Unlock()
			}
		}

		// Handle session state transitions (busy/idle sniff).
		if method == "claude/session_state" {
			var d struct {
				State string `json:"state"`
			}
			if json.Unmarshal(params, &d) == nil {
				s.smMu.Lock()
				if d.State == "idle" {
					// busy→done (turn completed); already-idle stays idle
					if s.smState == "busy" {
						s.smState = "done"
					}
					s.smBusySince = time.Time{}
					s.stateMu.Lock()
					if s.lastSummaryStaging != "" {
						s.lastSummary = s.lastSummaryStaging
					}
					s.lastSummaryStaging = ""
					s.stateMu.Unlock()
				} else {
					if s.smState != "busy" {
						s.smState = "busy"
						s.smBusySince = time.Now()
					}
				}
				state := s.smState
				busySince := s.smBusySince
				s.smMu.Unlock()
				s.publishAgentStatePatchWith(state, busySince)
				s.stateMu.Lock()
				s.writeStatusFileLocked()
				s.stateMu.Unlock()
			}
		}

		if method == "claude/result_meta" && s.usageLog != nil {
			s.usageLog.LogResultMeta(s.PokegentID, "", params)
		}

		return
	}
	if method != "session/update" {
		return
	}
	// Codex streams exec_command output as incrementally growing session/update
	// payloads (can exceed 5MB). Skip parsing these — they'd burn CPU on JSON
	// deserialization and contain no useful status data beyond what smaller
	// frames already provided.
	if len(params) > 256*1024 {
		return
	}
	s.lastUpdated.Store(time.Now().UnixMilli())
	s.translateUpdate(params)
}

func appendChatActivity(feed []ActivityItem, typ, text string, mergeLast bool) []ActivityItem {
	text = strings.TrimSpace(text)
	if text == "" {
		return feed
	}
	now := time.Now().Local().Format("15:04:05")
	if mergeLast && len(feed) > 0 && feed[len(feed)-1].Type == typ {
		feed[len(feed)-1].Text = truncateChat(feed[len(feed)-1].Text+text, 280)
		feed[len(feed)-1].Time = now
		return feed
	}
	feed = append(feed, ActivityItem{Time: now, Type: typ, Text: truncateChat(text, 280)})
	if len(feed) > 20 {
		feed = feed[len(feed)-20:]
	}
	return feed
}

// translateUpdate maps an ACP session/update notification onto the status/
// activity artifacts the iterm2 hook would have written. Keeps state.go,
// search.go, and the rest of the dashboard backend behind the same contract
// regardless of which backend produced the agent.
func (s *ChatSession) translateUpdate(params json.RawMessage) {
	var env struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &env); err != nil {
		return
	}
	var disc struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(env.Update, &disc); err != nil {
		return
	}
	if disc.SessionUpdate == "" {
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	switch disc.SessionUpdate {
	case "agent_message_chunk":
		var u struct {
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(env.Update, &u)
		if u.Content.Type == "text" {
			// Accumulate into staging — committed on busy→idle so the
			// AgentCard preview never flashes empty mid-turn.
			s.lastSummaryStaging = truncateChat(s.lastSummaryStaging+u.Content.Text, 280)
			s.activityFeed = appendChatActivity(s.activityFeed, "text", u.Content.Text, true)
			s.currentDetail = ""
		}
	case "agent_thought_chunk":
		var u struct {
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(env.Update, &u)
		if u.Content.Text != "" {
			s.activityFeed = appendChatActivity(s.activityFeed, "thinking", u.Content.Text, true)
			if s.currentDetail == "" {
				s.currentDetail = "thinking…"
			}
		}
	case "tool_call", "tool_call_update":
		// Reset the staging buffer when a new tool call starts so the
		// final committed summary is the LAST assistant text block (after
		// all tools), not the first 280 chars of the entire turn.
		if disc.SessionUpdate == "tool_call" {
			s.lastSummaryStaging = ""
			// Log MCP tool calls for usage tracking
			if s.usageLog != nil {
				var meta struct {
					Meta struct {
						ClaudeCode struct {
							ToolName string `json:"toolName"`
						} `json:"claudeCode"`
					} `json:"_meta"`
					RawInput json.RawMessage `json:"rawInput"`
				}
				if json.Unmarshal(env.Update, &meta) == nil && strings.HasPrefix(meta.Meta.ClaudeCode.ToolName, "mcp__") {
					target := ""
					var inp struct {
						To string `json:"to"`
					}
					if json.Unmarshal(meta.RawInput, &inp) == nil {
						target = inp.To
					}
					s.usageLog.LogMCPCall(s.PokegentID, "", meta.Meta.ClaudeCode.ToolName, target)
				}
			}
		}
		var u struct {
			Title     string `json:"title"`
			Kind      string `json:"kind"`
			Status    string `json:"status"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
			RawInput json.RawMessage `json:"rawInput"`
			Content  []struct {
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
				Text    string          `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(env.Update, &u)
		// Build a "Verb: args" label matching the bash hooks' format
		verb := chatVerbLabel(u.Kind)
		if verb == "" {
			verb = u.Title
		}
		// Normalize Codex's generic exec_command to descriptive names that match the chat panel.
		if verb == "exec_command" || verb == "Exec_command" || verb == "" {
			var ri struct {
				Cmd string `json:"cmd"`
			}
			if json.Unmarshal(u.RawInput, &ri) == nil && ri.Cmd != "" {
				verb = classifyCommand(ri.Cmd)
			}
		}
		args := chatToolArgs(u.RawInput, u.Locations)
		label := verb
		if args != "" {
			label = verb + ": " + truncateChat(args, 80)
		}
		if label != "" {
			s.recentActions = appendCappedChat(s.recentActions, label, 6)
			if disc.SessionUpdate == "tool_call" || len(s.activityFeed) == 0 {
				s.activityFeed = appendChatActivity(s.activityFeed, "tool", label, false)
			}
			s.currentDetail = label
		}
		// Extract the first fenced code block from tool result content as
		// last_trace — matches the iterm2 hook's extract_trace behavior.
		if disc.SessionUpdate == "tool_call_update" {
			if trace := chatExtractTrace(u.Content); trace != "" {
				s.lastTrace = trace
			}
		}
	case "usage_update":
		var u struct {
			Used int `json:"used"`
			Size int `json:"size"`
			Cost struct {
				Amount float64 `json:"amount"`
			} `json:"cost"`
		}
		_ = json.Unmarshal(env.Update, &u)
		s.contextTokens = u.Used
		if u.Size > 0 {
			s.contextWindow = u.Size
		}
		if s.usageLog != nil && u.Used > 0 {
			s.usageLog.LogUsageUpdate(s.PokegentID, "", u.Used, u.Size, u.Cost.Amount)
		}
	}
	s.debouncedStatusWrite()
}

// debouncedStatusWrite coalesces high-frequency status file writes (e.g. per
// streaming chunk) into a single write per 500ms trailing edge. State
// transitions (busy↔idle) call writeStatusFileLocked directly instead —
// they bypass the debounce for immediate on-disk visibility.
// Caller must hold s.stateMu.
func (s *ChatSession) debouncedStatusWrite() {
	if s.statusDebounce != nil {
		s.statusDebounce.Stop()
	}
	s.statusDebounce = time.AfterFunc(500*time.Millisecond, func() {
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		s.writeStatusFileLocked()
	})
}

// publishAgentStatePatchWith broadcasts a targeted state update to the
// dashboard SSE EventBus using the provided state values.
func (s *ChatSession) publishAgentStatePatchWith(state string, busySince time.Time) {
	if s.dashboardBus == nil {
		return
	}
	busySinceStr := ""
	if !busySince.IsZero() {
		busySinceStr = busySince.UTC().Format(time.RFC3339)
	}
	s.tasksMu.Lock()
	taskCount := len(s.activeTasks)
	s.tasksMu.Unlock()

	s.stateMu.Lock()
	summary := s.lastSummary
	if state == "busy" || state == "reconfiguring" {
		summary = s.lastSummaryStaging
	} else if s.lastSummaryStaging != "" {
		summary = s.lastSummaryStaging
	}
	previewAgent := AgentState{
		SessionID:       s.ACPID,
		PokegentID:      s.PokegentID,
		State:           state,
		Detail:          s.currentDetail,
		CWD:             s.Cwd,
		LastSummary:     summary,
		LastTrace:       s.lastTrace,
		UserPrompt:      s.currentPrompt,
		RecentActions:   append([]string(nil), s.recentActions...),
		ActivityFeed:    append([]ActivityItem(nil), s.activityFeed...),
		Interface:       "chat",
		AgentBackend:    s.AgentBackend,
		BackgroundTasks: taskCount,
		ContextTokens:   s.contextTokens,
		ContextWindow:   s.contextWindow,
	}
	s.stateMu.Unlock()
	previewAgent.BusySince = busySinceStr
	previewAgent.CardPreview = buildCardPreview(previewAgent)

	if s.notifyFn != nil && (state == "done" || state == "idle") {
		s.notifyFn(s.PokegentID, state, "", summary)
	}

	s.dashboardBus.Publish("agent_state_patch", map[string]any{
		"pokegent_id":      s.PokegentID,
		"state":            state,
		"detail":           previewAgent.Detail,
		"busy_since":       busySinceStr,
		"last_summary":     previewAgent.LastSummary,
		"last_trace":       previewAgent.LastTrace,
		"user_prompt":      previewAgent.UserPrompt,
		"recent_actions":   previewAgent.RecentActions,
		"activity_feed":    previewAgent.ActivityFeed,
		"card_preview":     previewAgent.CardPreview,
		"background_tasks": taskCount,
		"context_tokens":   previewAgent.ContextTokens,
		"context_window":   previewAgent.ContextWindow,
	})
}

func (s *ChatSession) writeStatusFileLocked() {
	// SessionID inside the status file is the *Claude conversation* ID.
	// Skip the write in the brief window before session/load returns ACPID.
	if s.ACPID == "" {
		return
	}
	s.smMu.Lock()
	state := s.smState
	busySince := ""
	if !s.smBusySince.IsZero() {
		busySince = s.smBusySince.UTC().Format(time.RFC3339)
	}
	s.smMu.Unlock()
	// During a busy turn, never fall back to the previous committed summary.
	// A new prompt should clear the card immediately; live text streams through
	// lastSummaryStaging and tools stream through activityFeed/recentActions.
	summary := s.lastSummary
	if state == "busy" || state == "reconfiguring" {
		summary = s.lastSummaryStaging
	} else if s.lastSummaryStaging != "" {
		summary = s.lastSummaryStaging
	}
	_ = writeStatusFile(s.dataDir, s.PokegentID, store.StatusFile{
		SessionID:     s.ACPID,
		State:         state,
		Detail:        s.currentDetail,
		CWD:           s.Cwd,
		BusySince:     busySince,
		LastSummary:   summary,
		LastTrace:     s.lastTrace,
		UserPrompt:    s.currentPrompt,
		RecentActions: append([]string(nil), s.recentActions...),
		ContextTokens: s.contextTokens,
		ContextWindow: s.contextWindow,
	})
}

func (s *ChatSession) handleRequest(method string, params json.RawMessage) (any, *chatJSONRPCError) {
	switch method {
	case "fs/read_text_file":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Path == "" {
			return nil, &chatJSONRPCError{Code: -32602, Message: "missing path"}
		}
		data, err := os.ReadFile(p.Path)
		if err != nil {
			return nil, &chatJSONRPCError{Code: -32000, Message: err.Error()}
		}
		return map[string]any{"content": string(data)}, nil
	case "fs/write_text_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Path == "" {
			return nil, &chatJSONRPCError{Code: -32602, Message: "missing path"}
		}
		if err := os.WriteFile(p.Path, []byte(p.Content), 0o644); err != nil {
			return nil, &chatJSONRPCError{Code: -32000, Message: err.Error()}
		}
		return map[string]any{}, nil
	case "session/request_permission":
		return s.requestPermission(params)
	}
	return nil, &chatJSONRPCError{Code: -32601, Message: "method not found"}
}

// requestPermission auto-allows every permission request — matching the
// `--dangerously-skip-permissions` behaviour of iTerm2-mode agents. The
// agent never blocks waiting for a human click.
func (s *ChatSession) requestPermission(params json.RawMessage) (any, *chatJSONRPCError) {
	var tmp struct {
		Options json.RawMessage `json:"options"`
	}
	_ = json.Unmarshal(params, &tmp)

	var opts []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	}
	_ = json.Unmarshal(tmp.Options, &opts)

	pick := ""
	for _, o := range opts {
		if o.Kind == "allow_always" {
			pick = o.OptionID
			break
		}
	}
	if pick == "" {
		for _, o := range opts {
			if o.Kind == "allow_once" {
				pick = o.OptionID
				break
			}
		}
	}
	if pick == "" && len(opts) > 0 {
		pick = opts[0].OptionID
	}
	if pick == "" {
		return nil, &chatJSONRPCError{Code: -32000, Message: "no permission options offered"}
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": pick}}, nil
}

// SendPrompt kicks off an ACP prompt (fire-and-forget). Updates status fields,
// echoes to WebSocket clients, and sends session/prompt to ACP in a goroutine.
func (s *ChatSession) SendPrompt(text string) error {
	// Reset message budget for this agent's new turn.
	budgetDir := filepath.Join(s.dataDir, "messages", s.PokegentID)
	os.MkdirAll(budgetDir, 0755)
	os.WriteFile(filepath.Join(budgetDir, "_msg_budget"), []byte("0"), 0644)

	// Update status for AgentCard
	s.stateMu.Lock()
	acpID := s.ACPID
	s.lastSummary = ""
	s.lastSummary = ""
	s.lastSummaryStaging = ""
	s.lastTrace = ""
	s.recentActions = nil
	s.activityFeed = nil
	s.currentPrompt = text
	s.currentDetail = "thinking…"
	tokensBefore := s.contextTokens
	s.stateMu.Unlock()

	s.smMu.Lock()
	s.smState = "busy"
	s.smBusySince = time.Now()
	state := s.smState
	busySince := s.smBusySince
	s.smMu.Unlock()

	s.publishAgentStatePatchWith(state, busySince)
	s.stateMu.Lock()
	s.writeStatusFileLocked()
	s.stateMu.Unlock()

	// Echo user message to WebSocket clients (for AgentCard-submitted prompts)
	if wsFrame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": acpID,
			"update": map[string]any{
				"sessionUpdate": "user_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
			},
		},
	}); err == nil {
		s.broadcastWSRaw(wsFrame)
	}

	// Build prompt blocks and send to ACP. Some ACP backends do not honor
	// `_meta.systemPrompt.append`, so clone context can be injected once into
	// the first real prompt while the UI still displays only the user's text.
	promptText := text
	s.initialPromptMu.Lock()
	if s.initialPromptContext != "" {
		promptText = s.initialPromptContext + "\n\nUser prompt:\n" + text
		s.initialPromptContext = ""
	}
	s.initialPromptMu.Unlock()
	prompt, err := buildACPPromptBlocks(promptText)
	if err != nil {
		return err
	}

	// Fire and forget in goroutine — sendRequest blocks until ACP finishes
	go func() {
		turnStart := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), chatPromptTimeout)
		defer cancel()
		_, sendErr := s.client.sendRequest(ctx, "session/prompt", map[string]any{
			"sessionId": acpID,
			"prompt":    prompt,
		})
		if sendErr != nil {
			log.Printf("chat prompt error (%s): %v", shortChat(s.PokegentID), sendErr)
		}
		// Log turn completion
		if s.usageLog != nil {
			s.stateMu.Lock()
			tokensAfter := s.contextTokens
			s.stateMu.Unlock()
			s.usageLog.LogTurnEnd(s.PokegentID, "", s.Profile, "", text, 0,
				tokensBefore, tokensAfter, time.Since(turnStart), sendErr)
		}
		// On error, transition to "error" so the card shows something went wrong.
		// On success, transition to "done".
		s.smMu.Lock()
		if sendErr != nil {
			s.smState = "error"
		} else {
			s.smState = "done"
		}
		s.smBusySince = time.Time{}
		s.smMu.Unlock()
		if sendErr != nil {
			s.stateMu.Lock()
			s.currentDetail = sendErr.Error()
			s.writeStatusFileLocked()
			s.stateMu.Unlock()
			s.publishAgentStatePatchWith("error", time.Time{})
			if wsFrame, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "claude/session_state",
				"params":  map[string]any{"sessionId": acpID, "state": "error"},
			}); err == nil {
				s.broadcastWSRaw(wsFrame)
			}
			return
		}
		s.stateMu.Lock()
		if s.lastSummaryStaging != "" {
			s.lastSummary = s.lastSummaryStaging
		}
		s.lastSummaryStaging = ""
		s.writeStatusFileLocked()
		s.stateMu.Unlock()
		s.publishAgentStatePatchWith("done", time.Time{})
		if wsFrame, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "claude/session_state",
			"params":  map[string]any{"sessionId": acpID, "state": "done"},
		}); err == nil {
			s.broadcastWSRaw(wsFrame)
		}
	}()
	return nil
}

func (s *ChatSession) Cancel() error {
	s.stateMu.Lock()
	acpID := s.ACPID
	s.stateMu.Unlock()

	// Tell ACP to stop.
	_ = s.client.sendNotification("session/cancel", map[string]any{"sessionId": acpID})

	// Immediately transition to idle.
	s.smMu.Lock()
	s.smState = "idle"
	s.smBusySince = time.Time{}
	s.smMu.Unlock()

	s.publishAgentStatePatchWith("idle", time.Time{})

	s.stateMu.Lock()
	s.writeStatusFileLocked()
	s.stateMu.Unlock()

	// Notify WebSocket clients so the frontend sees the cancel confirmation
	// without waiting for ACP's claude/session_state notification.
	if wsFrame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "claude/session_state",
		"params":  map[string]any{"sessionId": acpID, "state": "idle"},
	}); err == nil {
		s.broadcastWSRaw(wsFrame)
	}

	return nil
}

func (s *ChatSession) Close() error {
	s.intentionalClose.Store(true)
	if s.client.cmd.Process != nil {
		_ = s.client.cmd.Process.Kill()
	}
	return nil
}

// ForceIdle transitions the session to idle state and publishes the update.
// Used by debug/force-idle endpoint.
func (s *ChatSession) ForceIdle() {
	s.smMu.Lock()
	s.smState = "idle"
	s.smBusySince = time.Time{}
	s.smMu.Unlock()

	s.publishAgentStatePatchWith("idle", time.Time{})

	s.stateMu.Lock()
	s.writeStatusFileLocked()
	s.stateMu.Unlock()

	// Notify WebSocket clients
	if wsFrame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "claude/session_state",
		"params":  map[string]any{"state": "idle"},
	}); err == nil {
		s.broadcastWSRaw(wsFrame)
	}
}

func (s *ChatSession) StopTask(taskId string) error {
	return fmt.Errorf("StopTask not yet implemented")
}

// ── Manager ────────────────────────────────────────────────

type ChatManager struct {
	mu       sync.RWMutex
	sessions map[string]*ChatSession // by pokegent_id

	// wg tracks the cmd.Wait goroutine spawned per Launch so CloseAll
	// can block until every subprocess's exit handler has returned.
	wg sync.WaitGroup

	dataDir      string
	onChange     func()
	dashboardBus *EventBus // Phase 1: direct SSE broadcast
	usageLog     *UsageLogger
	notifyFn     func(pgid, state, agentName, summary string)
}

func NewChatManager(dataDir string, onChange func(), dashboardBus *EventBus, usageLog *UsageLogger) *ChatManager {
	return &ChatManager{
		sessions:     make(map[string]*ChatSession),
		dataDir:      dataDir,
		onChange:     onChange,
		dashboardBus: dashboardBus,
		usageLog:     usageLog,
	}
}

func (m *ChatManager) Get(pokegentID string) *ChatSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[pokegentID]
}

func (m *ChatManager) All() []*ChatSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*ChatSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

// ChatLaunchOptions describes a chat-mode launch invocation.
type ChatLaunchOptions struct {
	PokegentID           string
	Profile              string
	Cwd                  string
	SystemPromptAppend   string
	InitialPromptContext string
	Model                string
	Effort               string
	ResumeSessionID      string
	// ExistingTranscriptPath is the currently verified transcript for
	// ResumeSessionID, if any. For non-Claude backends, this is the durable
	// pointer we prefer on restart when a newer session_id has no transcript.
	ExistingTranscriptPath string
	// AgentBackend selects the ACP subprocess to spawn.
	// "claude" (default): node acp-fork/dist/index.js
	// "codex":            npx @openai/codex --full-auto
	// "codex-acp":        npx @zed-industries/codex-acp
	AgentBackend string
	// BackendConfigKey is the provider key in backends.json (e.g., "codex").
	// Written to the running file so reattach can resolve env vars.
	BackendConfigKey string
	// BackendEnv is extra environment variables from the backend config.
	// Merged into the subprocess env after the standard pokegent env vars.
	BackendEnv map[string]string
}

// effortToThinkingConfig maps the pokegents "effort" string onto the SDK's
// ThinkingConfig shape. Returns nil for empty/unknown effort.
func effortToThinkingConfig(effort string) map[string]any {
	switch effort {
	case "low":
		return map[string]any{"type": "enabled", "budgetTokens": 4000}
	case "medium":
		return map[string]any{"type": "enabled", "budgetTokens": 10000}
	case "high":
		return map[string]any{"type": "enabled", "budgetTokens": 32000}
	case "max":
		return map[string]any{"type": "adaptive"}
	}
	return nil
}

func (m *ChatManager) pokegentsMCPServers(opts ChatLaunchOptions) []any {
	root := resolvePokegentsRoot()
	serverPath := filepath.Join(root, "mcp", "server.js")
	if root == "" || !fileExists(serverPath) {
		log.Printf("chat[%s]: pokegents MCP server not found; tried %q", shortChat(opts.PokegentID), serverPath)
		return []any{}
	}
	return []any{map[string]any{
		"name":    "pokegents-messaging",
		"command": "node",
		"args":    []string{serverPath},
		"env": []map[string]string{
			{"name": "POKEGENTS_DATA", "value": m.dataDir},
			{"name": "POKEGENTS_SESSION_ID", "value": opts.PokegentID},
			{"name": "POKEGENT_ID", "value": opts.PokegentID},
			{"name": "POKEGENTS_PROFILE_NAME", "value": opts.Profile},
		},
	}}
}

func resolvePokegentsRoot() string {
	if root := os.Getenv("POKEGENTS_ROOT"); root != "" {
		return root
	}
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd, filepath.Dir(cwd))
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, dir, filepath.Dir(dir))
	}
	for _, root := range candidates {
		if fileExists(filepath.Join(root, "mcp", "server.js")) {
			return root
		}
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func (m *ChatManager) Launch(ctx context.Context, opts ChatLaunchOptions) (*ChatSession, error) {
	if opts.PokegentID == "" {
		return nil, fmt.Errorf("pokegent_id required")
	}
	if opts.Cwd == "" {
		home, _ := os.UserHomeDir()
		opts.Cwd = home
	}
	if !filepath.IsAbs(opts.Cwd) {
		return nil, fmt.Errorf("cwd must be absolute: %q", opts.Cwd)
	}
	if opts.AgentBackend != "" && opts.AgentBackend != "claude-acp" && opts.ExistingTranscriptPath != "" {
		if repaired, err := repairCodexTranscriptMissingCustomToolOutputs(opts.ExistingTranscriptPath); err != nil {
			log.Printf("chat[%s]: codex transcript repair skipped for %s: %v",
				shortChat(opts.PokegentID), opts.ExistingTranscriptPath, err)
		} else if repaired > 0 {
			log.Printf("chat[%s]: repaired %d missing custom tool output(s) in codex transcript %s",
				shortChat(opts.PokegentID), repaired, opts.ExistingTranscriptPath)
		}
	}

	var cmd *exec.Cmd
	switch opts.AgentBackend {
	case "codex-acp":
		args := []string{"@zed-industries/codex-acp"}
		if opts.Model != "" {
			args = append(args, "-c", "model="+strconv.Quote(opts.Model))
		}
		if opts.Effort != "" {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(opts.Effort))
		}
		if opts.SystemPromptAppend != "" {
			instructionsPath, err := writeCodexInstructionsFile(m.dataDir, opts.PokegentID, opts.SystemPromptAppend)
			if err != nil {
				return nil, err
			}
			args = append(args, "-c", "model_instructions_file="+strconv.Quote(instructionsPath))
		}
		cmd = exec.Command("npx", args...)
	case "codex":
		args := []string{"@openai/codex", "--full-auto"}
		if opts.Model != "" {
			args = append(args, "-c", "model="+strconv.Quote(opts.Model))
		}
		if opts.Effort != "" {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(opts.Effort))
		}
		if opts.SystemPromptAppend != "" {
			instructionsPath, err := writeCodexInstructionsFile(m.dataDir, opts.PokegentID, opts.SystemPromptAppend)
			if err != nil {
				return nil, err
			}
			args = append(args, "-c", "model_instructions_file="+strconv.Quote(instructionsPath))
		}
		cmd = exec.Command("npx", args...)
	default:
		acpBin := resolveClaudeACPPath()
		cmd = exec.Command("node", acpBin)
	}
	cmd.Dir = opts.Cwd
	overrides := map[string]string{
		"POKEGENT_ID":            opts.PokegentID,
		"POKEGENTS_SESSION_ID":   opts.PokegentID,
		"POKEGENTS_PROFILE_NAME": opts.Profile,
	}
	for k, v := range opts.BackendEnv {
		overrides[k] = v
	}
	env := os.Environ()
	clean := make([]string, 0, len(env)+len(overrides))
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			continue
		}
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		if _, overridden := overrides[key]; overridden {
			continue
		}
		clean = append(clean, kv)
	}
	for k, v := range overrides {
		clean = append(clean, k+"="+v)
	}
	cmd.Env = clean

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn claude-agent-acp: %w", err)
	}

	sess := &ChatSession{
		PokegentID:           opts.PokegentID,
		Profile:              opts.Profile,
		Cwd:                  opts.Cwd,
		Created:              time.Now(),
		dataDir:              m.dataDir,
		subscribers:          make(map[chan ChatSessionEvent]struct{}),
		activeTasks:          make(map[string]json.RawMessage),
		recentActions:        []string{},
		initialPromptContext: opts.InitialPromptContext,
		pendingPerms:         make(map[int64]*pendingPermission),
		smState:              "idle",
		dashboardBus:         m.dashboardBus,
		usageLog:             m.usageLog,
		notifyFn:             m.notifyFn,
		wsClients:            make(map[*wsClient]struct{}),
		browserPromptIDs:     make(map[int64]string),
	}
	sess.lastUpdated.Store(time.Now().UnixMilli())

	// Capture stderr: log, keep ring buffer, and forward errors/retries to WS clients.
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 0, 16*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			log.Printf("chat[%s/%d]: %s", shortChat(opts.PokegentID), cmd.Process.Pid, line)
			sess.stderrMu.Lock()
			sess.stderrTail = append(sess.stderrTail, line)
			if len(sess.stderrTail) > chatStderrTailLines {
				sess.stderrTail = sess.stderrTail[len(sess.stderrTail)-chatStderrTailLines:]
			}
			sess.stderrMu.Unlock()

			// Forward error/retry messages to WS clients as system notifications
			clean := stripANSI(line)
			if strings.Contains(clean, "ERROR") || strings.Contains(clean, "Reconnecting") {
				if msg, err := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "pokegents/system_message",
					"params":  map[string]any{"text": formatACPStderrSystemMessage(clean)},
				}); err == nil {
					sess.broadcastWSRaw(msg)
				}
			}
		}
	}()

	cli := &chatACPClient{
		cmd: cmd, stdin: stdin, stdout: stdout,
		done: make(chan struct{}),
	}
	cli.onNotif = sess.handleNotification
	cli.onReq = sess.handleRequest
	cli.onUnmatchedResponse = func(id int64, errResp *chatJSONRPCError) {
		sess.completeBrowserPrompt(id, errResp)
	}
	cli.onRawLine = func(line []byte) {
		// Skip forwarding fs/* and permission requests — handled server-side.
		var peek struct {
			Method string `json:"method,omitempty"`
			ID     *int64 `json:"id,omitempty"`
		}
		if json.Unmarshal(line, &peek) == nil && peek.ID != nil && peek.Method != "" {
			if peek.Method == "fs/read_text_file" || peek.Method == "fs/write_text_file" || peek.Method == "session/request_permission" {
				return
			}
		}
		if peek.Method == "session/update" {
			if compacted := compactSessionUpdateFrame(line); compacted != nil {
				sess.broadcastWSRaw(compacted)
				return
			}
		}
		sess.broadcastWSRaw(line)
	}
	sess.client = cli

	go cli.readLoop()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		err := cmd.Wait()
		intentional := sess.intentionalClose.Load()
		log.Printf("chat[%s]: process exited (intentional=%v, err=%v)",
			shortChat(opts.PokegentID), intentional, err)
		if !intentional {
			sess.stderrMu.Lock()
			tail := append([]string(nil), sess.stderrTail...)
			sess.stderrMu.Unlock()
			if len(tail) == 0 {
				log.Printf("chat[%s]: no stderr captured before exit — likely the subprocess died before writing anything (npx fetch error? auth?). exit err: %v",
					shortChat(opts.PokegentID), err)
			} else {
				log.Printf("chat[%s]: last %d stderr line(s) before exit:", shortChat(opts.PokegentID), len(tail))
				for _, line := range tail {
					log.Printf("chat[%s]:   %s", shortChat(opts.PokegentID), line)
				}
			}
		}
		// Unexpected chat backend exits are recoverable: keep the running file so
		// the UI can show the dead backend and offer a one-click restart.
		if !sess.intentionalClose.Load() {
			sess.smMu.Lock()
			sess.smState = "error"
			sess.smBusySince = time.Time{}
			sess.smMu.Unlock()
			sess.stateMu.Lock()
			sess.currentDetail = "backend process exited — restart backend to recover"
			sess.writeStatusFileLocked()
			sess.stateMu.Unlock()
			sess.publishAgentStatePatchWith("error", time.Time{})
		}
		m.mu.Lock()
		delete(m.sessions, opts.PokegentID)
		m.mu.Unlock()
		if m.onChange != nil {
			m.onChange()
		}
	}()

	if _, err := cli.sendRequest(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
		"clientInfo": map[string]any{"name": "pokegents-dashboard", "version": "0.1.0"},
	}); err != nil {
		sess.intentionalClose.Store(true)
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("acp initialize: %w", err)
	}

	// Either start a fresh session or load an existing one (migration / resume).
	method := "session/new"
	params := map[string]any{
		"cwd":        opts.Cwd,
		"mcpServers": m.pokegentsMCPServers(opts),
	}
	meta := map[string]any{}
	if opts.SystemPromptAppend != "" {
		meta["systemPrompt"] = map[string]any{"append": opts.SystemPromptAppend}
	}
	cc := map[string]any{}
	if opts.Model != "" {
		cc["model"] = opts.Model
	}
	if t := effortToThinkingConfig(opts.Effort); t != nil {
		cc["thinking"] = t
	}
	if len(cc) > 0 {
		meta["claudeCode"] = map[string]any{"options": cc}
	}
	if len(meta) > 0 {
		params["_meta"] = meta
	}
	resumeSessionID := opts.ResumeSessionID
	if opts.AgentBackend != "" && opts.AgentBackend != "claude-acp" && opts.ExistingTranscriptPath != "" {
		if _, statErr := os.Stat(opts.ExistingTranscriptPath); statErr == nil {
			pathSessionID := sessionIDFromTranscriptPath(opts.ExistingTranscriptPath)
			if pathSessionID != "" && resumeSessionID != "" && store.FindTranscriptPath(resumeSessionID, "") == "" {
				log.Printf("chat[%s]: resume session %s has no transcript; preferring verified transcript session %s",
					shortChat(opts.PokegentID), shortChat(resumeSessionID), shortChat(pathSessionID))
				resumeSessionID = pathSessionID
			}
		}
	}
	if resumeSessionID != "" {
		method = "session/load"
		params["sessionId"] = resumeSessionID
	}
	resp, err := cli.sendRequest(ctx, method, params)
	// If session/load fails, first try the session id encoded in the verified
	// transcript path. Only fall back to session/list/fresh when we do not have
	// a durable transcript pointer. This prevents dashboard restarts/dropdown
	// backend actions from overwriting a known-good Codex session with an
	// arbitrary newer session whose transcript cannot be found.
	if err != nil && method == "session/load" {
		if opts.ExistingTranscriptPath != "" {
			if _, statErr := os.Stat(opts.ExistingTranscriptPath); statErr == nil {
				if pathSessionID := sessionIDFromTranscriptPath(opts.ExistingTranscriptPath); pathSessionID != "" && pathSessionID != resumeSessionID {
					log.Printf("chat[%s]: session/load %s failed (%v), trying verified transcript session %s",
						shortChat(opts.PokegentID), shortChat(resumeSessionID), err, shortChat(pathSessionID))
					params["sessionId"] = pathSessionID
					resp, err = cli.sendRequest(ctx, "session/load", params)
				}
			}
			if err != nil {
				sess.intentionalClose.Store(true)
				_ = cmd.Process.Kill()
				return nil, fmt.Errorf("acp session/load failed for verified transcript %s: %w", opts.ExistingTranscriptPath, err)
			}
		} else {
			log.Printf("chat[%s]: session/load failed (%v), trying session/list", shortChat(opts.PokegentID), err)
			if listResp, listErr := cli.sendRequest(ctx, "session/list", map[string]any{}); listErr == nil {
				var listResult struct {
					Sessions []struct {
						SessionID string `json:"sessionId"`
						CWD       string `json:"cwd"`
					} `json:"sessions"`
				}
				if json.Unmarshal(listResp, &listResult) == nil && len(listResult.Sessions) > 0 {
					// Pick the most recent session (first in list) matching our cwd
					picked := ""
					for _, s := range listResult.Sessions {
						if s.CWD == opts.Cwd {
							picked = s.SessionID
							break
						}
					}
					if picked == "" {
						picked = listResult.Sessions[0].SessionID
					}
					log.Printf("chat[%s]: found %d sessions via list, loading %s", shortChat(opts.PokegentID), len(listResult.Sessions), shortChat(picked))
					params["sessionId"] = picked
					resp, err = cli.sendRequest(ctx, "session/load", params)
				}
			}
			// Final fallback: start fresh
			if err != nil {
				log.Printf("chat[%s]: all session/load attempts failed, starting fresh", shortChat(opts.PokegentID))
				delete(params, "sessionId")
				resp, err = cli.sendRequest(ctx, "session/new", params)
				method = "session/new"
			}
		}
	}
	if err != nil {
		sess.intentionalClose.Store(true)
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("acp %s: %w", method, err)
	}
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		sess.intentionalClose.Store(true)
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("acp %s bad response: %s", method, string(resp))
	}
	if result.SessionID == "" && opts.ResumeSessionID != "" {
		result.SessionID = opts.ResumeSessionID
	}
	if result.SessionID == "" {
		sess.intentionalClose.Store(true)
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("acp %s did not return a session id", method)
	}
	sess.stateMu.Lock()
	sess.ACPID = result.SessionID
	sess.stateMu.Unlock()

	// Patch claude_pid and agent_backend into the running file.
	sess.AgentBackend = opts.AgentBackend
	// Write the backend CONFIG KEY (e.g. "codex") to the running file, not the
	// resolved type (e.g. "codex-acp"). Reattach needs the config key to look up
	// env vars from backends.json.
	backendForFile := opts.BackendConfigKey
	if backendForFile == "" {
		backendForFile = opts.AgentBackend
	}
	patchRunningFileChat(m.dataDir, opts.PokegentID, opts.Profile, cmd.Process.Pid, result.SessionID, opts.Cwd, "", backendForFile)

	// For non-Claude backends, discover the transcript path in the background.
	// The file may not exist immediately after session/new.
	if opts.AgentBackend != "" && opts.AgentBackend != "claude-acp" {
		go func() {
			for attempt := 0; attempt < 15; attempt++ {
				time.Sleep(2 * time.Second)
				path := store.FindTranscriptPath(result.SessionID, "")
				if path != "" {
					patchTranscriptPath(m.dataDir, opts.PokegentID, opts.Profile, path)
					return
				}
			}
		}()
	}

	// Register the session BEFORE the initial status write.
	m.mu.Lock()
	m.sessions[opts.PokegentID] = sess
	m.mu.Unlock()

	// Initial "idle" status write so the dashboard sees us immediately.
	sess.stateMu.Lock()
	sess.writeStatusFileLocked()
	sess.stateMu.Unlock()

	if m.onChange != nil {
		m.onChange()
	}
	return sess, nil
}

func resolveClaudeACPPath() string {
	if p := os.Getenv("POKEGENTS_CLAUDE_ACP_PATH"); p != "" {
		return p
	}
	if root := resolvePokegentsRoot(); root != "" {
		if p := filepath.Join(root, "dashboard", "acp-fork", "dist", "index.js"); fileExists(p) {
			return p
		}
		if p := filepath.Join(root, "acp-fork", "dist", "index.js"); fileExists(p) {
			return p
		}
	}
	selfPath, _ := os.Executable()
	return filepath.Join(filepath.Dir(selfPath), "acp-fork", "dist", "index.js")
}

func writeCodexInstructionsFile(dataDir, pokegentID, instructions string) (string, error) {
	if pokegentID == "" {
		return "", fmt.Errorf("pokegent_id required for Codex instructions")
	}
	dir := filepath.Join(dataDir, "codex-instructions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create Codex instructions dir: %w", err)
	}
	path := filepath.Join(dir, pokegentID+".md")
	if err := os.WriteFile(path, []byte(instructions), 0o600); err != nil {
		return "", fmt.Errorf("write Codex instructions: %w", err)
	}
	return path, nil
}

// repatchRunningFile rewrites the running file for an active chat session
// using its current pid and session_id.
func (m *ChatManager) repatchRunningFile(pokegentID string) {
	m.mu.RLock()
	sess, ok := m.sessions[pokegentID]
	m.mu.RUnlock()
	if !ok || sess == nil {
		return
	}
	pid := 0
	if sess.client != nil && sess.client.cmd != nil && sess.client.cmd.Process != nil {
		pid = sess.client.cmd.Process.Pid
	}
	patchRunningFileChat(m.dataDir, pokegentID, sess.Profile, pid, sess.ACPID, sess.Cwd, "", sess.AgentBackend)
}

// CloseAll cleanly shuts down every active chat session and waits for
// each subprocess's exit handler to return.
func (m *ChatManager) CloseAll(timeout time.Duration) {
	m.mu.Lock()
	pgids := make([]string, 0, len(m.sessions))
	for pgid := range m.sessions {
		pgids = append(pgids, pgid)
	}
	m.mu.Unlock()
	for _, pgid := range pgids {
		m.Close(pgid)
	}
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("chat: CloseAll timed out after %s with %d session(s) still draining", timeout, len(pgids))
	}
}

func (m *ChatManager) Close(pokegentID string) {
	m.mu.Lock()
	sess, ok := m.sessions[pokegentID]
	if ok {
		delete(m.sessions, pokegentID)
	}
	m.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
		if m.onChange != nil {
			m.onChange()
		}
	}
}

const (
	chatMaxForwardFrameBytes = 32 * 1024
	// Keep enough inline script/patch text for the browser to recover all
	// touched files from Codex exec_command calls. Transcript backfill already
	// preserves 12KB; use the same cap for streaming so live UI and refresh UI
	// don't disagree.
	chatMaxForwardCmdBytes = 12000
)

// compactSessionUpdateFrame keeps tool-call metadata but removes bulky Codex
// exec output before it reaches the browser. The dashboard needs command,
// status, and tool id metadata; it does not need full terminal output.
func compactSessionUpdateFrame(line []byte) []byte {
	var frame map[string]any
	if json.Unmarshal(line, &frame) != nil {
		return nil
	}
	params, _ := frame["params"].(map[string]any)
	if params == nil {
		return nil
	}
	update, _ := params["update"].(map[string]any)
	if update == nil {
		return nil
	}

	su, _ := update["sessionUpdate"].(string)
	if su != "tool_call" && su != "tool_call_update" {
		return nil
	}

	rawInput, _ := update["rawInput"].(map[string]any)
	cmd, _ := rawInput["cmd"].(string)
	title, _ := update["title"].(string)
	isCodexExec := title == "exec_command" || cmd != ""
	changed := false

	if isCodexExec {
		if cmd != "" {
			update["title"] = classifyCommand(cmd)
		}
		if _, hasContent := update["content"]; hasContent {
			update["content"] = omittedExecOutputContent()
			changed = true
		}
		if _, hasRawOutput := update["rawOutput"]; hasRawOutput {
			update["rawOutput"] = "[exec output omitted by dashboard]"
			changed = true
		}
	}

	if rawInput, ok := update["rawInput"].(map[string]any); ok {
		if cmd, ok := rawInput["cmd"].(string); ok && len(cmd) > chatMaxForwardCmdBytes {
			rawInput["cmd"] = cmd[:chatMaxForwardCmdBytes] + "..."
			changed = true
		}
	}

	if !changed && len(line) <= chatMaxForwardFrameBytes {
		return nil
	}
	if !changed {
		update["content"] = omittedExecOutputContent()
		update["rawOutput"] = "[large tool output omitted by dashboard]"
		changed = true
	}

	out, err := json.Marshal(frame)
	if err != nil {
		return nil
	}
	return out
}

func omittedExecOutputContent() []any {
	return []any{map[string]any{
		"type":           "terminal",
		"terminalOutput": "[exec output omitted by dashboard]",
	}}
}

// classifyCommand maps a shell command to a Claude-style tool name.
func classifyCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	switch {
	case hasShellPrefix(cmd, "cat", "head", "tail", "less", "nl", "sed", "awk"):
		return "Read"
	case hasShellPrefix(cmd, "rg", "grep", "find", "ag"):
		return "Search"
	case hasShellPrefix(cmd, "ls", "tree"):
		return "List"
	case hasShellPrefix(cmd, "git"):
		return "Git"
	case hasShellPrefix(cmd, "python", "python3", "node", "npm", "pnpm", "yarn", "go", "cargo", "make", "pytest", "uv"):
		return "Run"
	default:
		return "Bash"
	}
}

func hasShellPrefix(cmd string, names ...string) bool {
	for _, name := range names {
		if cmd == name || strings.HasPrefix(cmd, name+" ") || strings.HasPrefix(cmd, name+"\t") {
			return true
		}
	}
	return false
}

// patchRunningFileChat updates the placeholder running file with the actual
// claude_pid (subprocess PID) and Claude session_id (from session/new response).
func patchRunningFileChat(dataDir, pokegentID, profile string, claudePID int, claudeSessionID, cwd, transcriptPath, agentBackend string) {
	path := filepath.Join(dataDir, "running", profile+"-"+pokegentID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		// Profile in the filename may not match opts.Profile (e.g. after a role
		// change). Fall back to glob by pokegent_id.
		matches, _ := filepath.Glob(filepath.Join(dataDir, "running", "*-"+pokegentID+".json"))
		if len(matches) == 0 {
			return
		}
		path = matches[0]
		data, err = os.ReadFile(path)
		if err != nil {
			return
		}
	}
	var rs map[string]any
	if err := json.Unmarshal(data, &rs); err != nil {
		return
	}
	rs["claude_pid"] = claudePID
	rs["pid"] = claudePID
	if existingTranscript, _ := rs["transcript_path"].(string); existingTranscript != "" {
		if _, err := os.Stat(existingTranscript); err == nil {
			if existingSession, _ := rs["session_id"].(string); existingSession != "" {
				rs["last_good_session_id"] = existingSession
			}
			rs["last_good_transcript_path"] = existingTranscript
		}
	}
	rs["session_id"] = claudeSessionID
	rs["pokegent_id"] = pokegentID
	if cwd != "" {
		rs["cwd"] = cwd
	}
	if transcriptPath != "" {
		rs["transcript_path"] = transcriptPath
		if _, err := os.Stat(transcriptPath); err == nil {
			rs["last_good_session_id"] = claudeSessionID
			rs["last_good_transcript_path"] = transcriptPath
		}
	} else {
		// Keep any existing verified transcript_path until a new native
		// transcript is discovered. This gives dashboard restart/revive a
		// durable "last known good" conversation pointer instead of replacing it
		// with a transient ACP session id that may never write a transcript.
	}
	if agentBackend != "" {
		rs["agent_backend"] = agentBackend
	}
	out, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func sessionIDFromTranscriptPath(path string) string {
	base := filepath.Base(path)
	matches := regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`).FindAllString(base, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

// patchTranscriptPath writes just the transcript_path field to the running file.
func patchTranscriptPath(dataDir, pokegentID, profile, transcriptPath string) {
	path := filepath.Join(dataDir, "running", profile+"-"+pokegentID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rs map[string]any
	if json.Unmarshal(data, &rs) != nil {
		return
	}
	rs["transcript_path"] = transcriptPath
	rs["last_good_transcript_path"] = transcriptPath
	if sid, _ := rs["session_id"].(string); sid != "" {
		rs["last_good_session_id"] = sid
	}
	out, _ := json.MarshalIndent(rs, "", "  ")
	if out == nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, out, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}

// ── Helpers ────────────────────────────────────────────────

// buildACPPromptBlocks turns a text string with embedded `[Image: <path>]`
// tokens into the array of content blocks that ACP `session/prompt` expects.
func buildACPPromptBlocks(text string) ([]any, error) {
	tokenRE := regexp.MustCompile(`\[Image:\s+([^\]]+)\]`)
	matches := tokenRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []any{map[string]any{"type": "text", "text": text}}, nil
	}
	blocks := make([]any, 0, len(matches)*2+1)
	cursor := 0
	for _, m := range matches {
		if m[0] > cursor {
			pre := strings.TrimSpace(text[cursor:m[0]])
			if pre != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": pre})
			}
		}
		path := strings.TrimSpace(text[m[2]:m[3]])
		data, err := os.ReadFile(path)
		if err != nil {
			blocks = append(blocks, map[string]any{"type": "text", "text": text[m[0]:m[1]]})
		} else {
			mime := mimeForImagePath(path)
			blocks = append(blocks, map[string]any{
				"type":     "image",
				"mimeType": mime,
				"data":     base64.StdEncoding.EncodeToString(data),
			})
		}
		cursor = m[1]
	}
	if cursor < len(text) {
		post := strings.TrimSpace(text[cursor:])
		if post != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": post})
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}
	return blocks, nil
}

func mimeForImagePath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

func shortChat(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

func truncateChat(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func appendCappedChat(slice []string, s string, cap int) []string {
	slice = append(slice, s)
	if len(slice) > cap {
		slice = slice[len(slice)-cap:]
	}
	return slice
}

// chatVerbLabel maps an ACP tool kind onto the same compact label the chat
// transcript's frontend parser uses, so card previews and chat rows agree.
func chatVerbLabel(kind string) string {
	switch kind {
	case "execute":
		return "Bash"
	case "read":
		return "Read"
	case "edit":
		return "Update"
	case "search":
		return "Search"
	case "fetch":
		return "Fetch"
	case "think":
		return "Agent"
	case "other":
		return "Tool"
	}
	if kind == "" {
		return ""
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

// chatToolArgs derives a one-line "args" string for a tool call.
func chatToolArgs(raw json.RawMessage, locations []struct {
	Path string `json:"path"`
}) string {
	if len(raw) > 0 {
		var fields struct {
			Cmd         string `json:"cmd"`
			Command     string `json:"command"`
			FilePath    string `json:"file_path"`
			Path        string `json:"path"`
			Pattern     string `json:"pattern"`
			Query       string `json:"query"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &fields); err == nil {
			for _, v := range []string{fields.Command, fields.Cmd, fields.FilePath, fields.Path, fields.Pattern, fields.Query, fields.Description} {
				if v != "" {
					return v
				}
			}
		}
	}
	if len(locations) > 0 {
		return locations[0].Path
	}
	return ""
}

// chatExtractTrace pulls the first fenced code block out of a tool result's
// content array.
func chatExtractTrace(content []struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Text    string          `json:"text"`
}) string {
	for _, c := range content {
		var text string
		if c.Text != "" {
			text = c.Text
		} else if len(c.Content) > 0 {
			var inner struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(c.Content, &inner); err == nil {
				text = inner.Text
			} else {
				_ = json.Unmarshal(c.Content, &text)
			}
		}
		if text == "" {
			continue
		}
		if i := strings.Index(text, "```"); i >= 0 {
			rest := text[i+3:]
			if nl := strings.Index(rest, "\n"); nl >= 0 {
				rest = rest[nl+1:]
			}
			if end := strings.Index(rest, "```"); end >= 0 {
				return strings.TrimSpace(rest[:end])
			}
		}
		return truncateChat(strings.TrimSpace(text), 500)
	}
	return ""
}
