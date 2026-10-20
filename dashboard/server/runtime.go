package server

import (
	"context"
	"fmt"
)

// Runtime is the polymorphic interface every backend implements. The HTTP
// handlers branch only on capabilities — never on the runtime name — so
// adding a third runtime (Codex ACP, native server, etc.) is a single-file
// affair: implement `Runtime`, register it with the server.
//
// State semantics: a runtime has a 1:N relationship with active agents. It
// looks them up internally by `pokegent_id` (the abstraction-layer ID). The
// runtime is stateless from the dashboard's view — all agent state still
// lives in the canonical status / running files.
type Runtime interface {
	Name() string
	Capabilities() RuntimeCapabilities

	// SendPrompt delivers a user prompt to the agent. The text may contain
	// `[Image: <path>]` tokens which the runtime resolves however it sees
	// fit (chat: read file → ACP image block; iterm2: types the literal
	// token into the TTY, which claude-cli parses).
	SendPrompt(ctx context.Context, pgid, text string) error

	// Cancel interrupts the currently running turn. iTerm2: types Esc into
	// the TTY. Chat: ACP `session/cancel`.
	Cancel(ctx context.Context, pgid string) error

	// Close shuts down the agent process. iTerm2: types `/exit` and closes
	// the tab. Chat: kills the ACP subprocess. Idempotent.
	Close(ctx context.Context, pgid string) error

	// Focus brings the agent's UI surface forward. iTerm2: focuses the tab.
	// Chat: no-op (the dashboard handles opening the side panel itself —
	// the runtime doesn't own the chat panel).
	Focus(ctx context.Context, pgid string) error

	// CheckMessages prompts the agent to read its mailbox. iTerm2: types
	// "check messages" into the TTY. Chat: sends as a session/prompt.
	CheckMessages(ctx context.Context, pgid string) error

	// StopTask cancels a running background task by taskId.
	StopTask(ctx context.Context, pgid, taskId string) error
}

// RuntimeCapabilities tells the dashboard what features each backend
// supports. The frontend reads these via `/api/runtimes` and gates UI
// affordances accordingly — e.g. the AgentMenu hides "Spawn clone" for
// chat agents because ChatRuntime.Capabilities().CanClone is false.
type RuntimeCapabilities struct {
	// CanFocus → "Go to terminal" menu item shown.
	CanFocus bool `json:"can_focus"`
	// CanClone → "Spawn clone" menu item shown (iterm2-only — clone is
	// `pokegent --resume --fork-session` in a new tab, no chat equivalent
	// today).
	CanClone bool `json:"can_clone"`
	// CanCancel → "Cancel" button shown when state=busy.
	CanCancel bool `json:"can_cancel"`
	// HasStreamingUI → ChatPanel SSE stream is available.
	HasStreamingUI bool `json:"has_streaming_ui"`
	// HasPermissionUI → permission prompts surface as inline UI (vs.
	// being handled inside Claude's terminal flow).
	HasPermissionUI bool `json:"has_permission_ui"`
}

// runtimeRegistry indexes runtimes by their Name(). Constructed once at
// server startup and shared by all handlers.
type runtimeRegistry map[string]Runtime

func (r runtimeRegistry) For(iface string) (Runtime, error) {
	runtimeName := runtimeNameForSurface(iface)
	rt, ok := r[runtimeName]
	if !ok {
		return nil, fmt.Errorf("unknown runtime %q", iface)
	}
	return rt, nil
}
