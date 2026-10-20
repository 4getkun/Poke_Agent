package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"pokegents/dashboard/server/store"
)

// /api/sessions/{id}/runtime-config — updates an agent's runtime config
// (currently model + effort). The two paths diverge by interface:
//
//   - iterm2: types `/model X` (or `/effort X`) into the agent's TTY so
//     Claude CLI parses it natively.
//   - chat:   updates the identity file, rewrites the running file with
//     the new fields, then closes + relaunches the ACP backend with the
//     new options threaded through `_meta.claudeCode.options`. Same Claude
//     session_id (so JSONL transcript continues), brief reconnect blip on
//     the chat panel.
//
// Persistence in both cases means switching agents back and forth keeps the
// new model/effort. Empty fields in the request leave the existing value
// unchanged.
type runtimeConfigBody struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// legacyModelAliases exists only so old role/project/running files and manual /model shortcuts keep working.
// New backends.json defaults use exact provider model IDs.
var legacyModelAliases = map[string]string{
	"opus":       "claude-opus-4-6[1m]",
	"opus4":      "claude-opus-4-6[1m]",
	"sonnet":     "claude-sonnet-4-6",
	"sonnet4":    "claude-sonnet-4-6",
	"haiku":      "haiku",
	"haiku4":     "haiku",
	"opus-4-7":   "claude-opus-4-7",
	"opus-4-6":   "claude-opus-4-6[1m]",
	"sonnet-4-6": "claude-sonnet-4-6",
	"haiku-4-5":  "haiku",
}

func resolveModelAlias(name string) string {
	if name == "" {
		return ""
	}
	if resolved, ok := legacyModelAliases[name]; ok {
		return resolved
	}
	return name
}

func (s *Server) handleSetRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	var body runtimeConfigBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Model == "" && body.Effort == "" {
		http.Error(w, "need at least one of {model, effort}", http.StatusBadRequest)
		return
	}
	body.Model = resolveModelAlias(body.Model)

	agent, _, done := s.resolveAgentAndRuntime(w, r)
	if done {
		return
	}
	pgid := agent.PokegentID
	if pgid == "" {
		http.Error(w, "agent has no pokegent_id", http.StatusBadRequest)
		return
	}

	// Persist to identity so the change survives reattach / revive.
	_ = s.fileStore.Agents.Update(pgid, func(id *store.AgentIdentity) {
		if body.Model != "" {
			id.Model = body.Model
		}
		if body.Effort != "" {
			id.Effort = body.Effort
		}
	})

	switch agent.Interface {
	case "chat":
		if err := s.applyChatRuntimeConfig(r.Context(), pgid, body.Model, body.Effort); err != nil {
			http.Error(w, "apply chat config: "+err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		// iterm2 / unknown — type the slash command into the TTY. Claude CLI
		// parses it natively.
		if body.Model != "" {
			_ = s.iterm2WriteSlash(agent, "/model "+body.Model)
		}
		if body.Effort != "" {
			_ = s.iterm2WriteSlash(agent, "/effort "+body.Effort)
		}
	}

	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]any{
		"ok":     true,
		"model":  body.Model,
		"effort": body.Effort,
	})
}

// applyChatRuntimeConfig is the chat-specific path: update the running file
// fields, close the ACP backend, relaunch with the new options. Same Claude
// session_id is preserved by relaunchChatSession's ResumeSessionID flow.
func (s *Server) applyChatRuntimeConfig(_ context.Context, pgid, model, effort string) error {
	runningGlob := filepath.Join(s.dataDir, "running", "*-"+pgid+".json")
	matches, _ := filepath.Glob(runningGlob)
	if len(matches) == 0 {
		return fmt.Errorf("no running file for %s", pgid)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return fmt.Errorf("read running file: %w", err)
	}
	var rs store.RunningSession
	if err := json.Unmarshal(raw, &rs); err != nil {
		return fmt.Errorf("parse running file: %w", err)
	}
	if model != "" {
		rs.Model = model
	}
	if effort != "" {
		rs.Effort = effort
	}
	out, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(matches[0], out, 0o644); err != nil {
		return fmt.Errorf("write running file: %w", err)
	}

	// Close current ACP backend (intentional — exit handler skips cleanup).
	// Brief sleep so the subprocess fully releases the JSONL lock before
	// session/load on the new one.
	s.chatMgr.Close(pgid)
	time.Sleep(500 * time.Millisecond)

	if err := s.relaunchChatSession(rs); err != nil {
		return fmt.Errorf("relaunch chat: %w", err)
	}
	return nil
}

// iterm2WriteSlash is a tiny helper that types a slash-command line into
// the agent's iTerm2 TTY. We can't use ITerm2Runtime.SendPrompt because
// that's reserved for user prompts — the slash-command verbs need to land
// in claude's REPL, not be queued as a "send to model" message. Same call
// shape as the runtime's internal writes.
func (s *Server) iterm2WriteSlash(agent *AgentState, line string) error {
	if agent.TTY == "" {
		return fmt.Errorf("agent has no TTY")
	}
	go s.terminal.WriteText(agent.ITermSessionID, agent.TTY, line)
	return nil
}
