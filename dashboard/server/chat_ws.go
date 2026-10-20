package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  16 * 1024,
	WriteBufferSize: 16 * 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

type wsClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func (c *wsClient) writeRaw(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *wsClient) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.conn.WriteJSON(v)
}

func (s *Server) handleChatWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess := s.chatMgr.Get(id)
	if sess == nil {
		http.Error(w, "chat session not found", http.StatusNotFound)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("chat-ws[%s]: upgrade failed: %v", shortChat(id), err)
		return
	}
	defer conn.Close()

	wsc := &wsClient{conn: conn}
	sess.addWSClient(wsc)
	defer sess.removeWSClient(wsc)

	sess.stateMu.Lock()
	acpID := sess.ACPID
	sess.stateMu.Unlock()

	sess.smMu.Lock()
	state := sess.smState
	sess.smMu.Unlock()

	_ = wsc.writeJSON(map[string]any{
		"type":       "ws_connected",
		"session_id": acpID,
		"state":      state,
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("chat-ws[%s]: read error: %v", shortChat(id), err)
			}
			return
		}

		var peek chatRawFrame
		if json.Unmarshal(msg, &peek) != nil {
			continue
		}

		if peek.ID != nil && peek.Method != "" {
			if peek.Method == "fs/read_text_file" || peek.Method == "fs/write_text_file" {
				result, errResp := sess.handleRequest(peek.Method, peek.Params)
				_ = sendWSResponse(wsc, *peek.ID, result, errResp)
				continue
			}
		}

		if peek.ID != nil && peek.Method == "" {
			if sess.DeliverPermission(*peek.ID, extractPermOptionID(peek.Result), false) {
				continue
			}
		}

		// When the browser sends session/prompt, transition to busy so the
		// dashboard shows the correct state. Claude emits claude/session_state
		// but Codex doesn't, so without this the agent stays idle. Keep enough
		// metadata for AgentCard/status parity with REST-submitted prompts.
		isPrompt := peek.Method == "session/prompt"
		if isPrompt {
			promptPreview := extractPromptPreview(peek.Params)
			if peek.ID != nil {
				sess.trackBrowserPrompt(*peek.ID, promptPreview)
			}
			if s.state != nil {
				s.state.BeginPrompt(id, promptPreview)
			}
			if s.state != nil && s.eventBus != nil {
				s.eventBus.Publish("state_update", s.state.GetAgents())
			}
			sess.stateMu.Lock()
			sess.lastSummary = ""
			sess.lastSummaryStaging = ""
			sess.lastTrace = ""
			sess.recentActions = nil
			sess.activityFeed = nil
			sess.currentPrompt = promptPreview
			if isCompactPrompt(promptPreview) {
				sess.currentDetail = "compacting"
			} else {
				sess.currentDetail = "thinking…"
			}
			sess.stateMu.Unlock()

			sess.smMu.Lock()
			if sess.smState != "busy" {
				sess.smState = "busy"
				sess.smBusySince = time.Now()
			}
			busySince := sess.smBusySince
			sess.smMu.Unlock()
			sess.stateMu.Lock()
			sess.writeStatusFileLocked()
			sess.stateMu.Unlock()
			sess.publishAgentStatePatchWith("busy", busySince)
		}

		if err := sess.client.writeLine(msg); err != nil {
			log.Printf("chat-ws[%s]: stdin write error: %v", shortChat(id), err)
			if isPrompt {
				sess.smMu.Lock()
				sess.smState = "error"
				sess.smBusySince = time.Time{}
				sess.smMu.Unlock()
				sess.stateMu.Lock()
				sess.currentDetail = "send failed: backend connection closed; reconnect and retry"
				sess.writeStatusFileLocked()
				sess.stateMu.Unlock()
				sess.publishAgentStatePatchWith("error", time.Time{})
				if peek.ID != nil {
					_ = sendWSResponse(wsc, *peek.ID, nil, &chatJSONRPCError{Code: -32000, Message: "backend connection closed; reconnect and retry"})
				}
			}
			return
		}
	}
}

func extractPromptPreview(params json.RawMessage) string {
	var p struct {
		Prompt []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	parts := make([]string, 0, len(p.Prompt))
	for _, b := range p.Prompt {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return truncateChat(strings.Join(parts, "\n"), 280)
}

func sendWSResponse(c *wsClient, id int64, result any, errResp *chatJSONRPCError) error {
	if errResp != nil {
		return c.writeJSON(struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      int64             `json:"id"`
			Error   *chatJSONRPCError `json:"error"`
		}{"2.0", id, errResp})
	}
	rb, _ := json.Marshal(result)
	return c.writeJSON(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{"2.0", id, rb})
}

func extractPermOptionID(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	var r struct {
		Outcome struct {
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(result, &r) == nil {
		return r.Outcome.OptionID
	}
	return ""
}
