package server

import (
	"context"
	"fmt"
)

// chatRuntime implements the Runtime interface for agents running through
// the @zed-industries/claude-agent-acp adapter. Operations bridge to the
// ChatManager which holds the JSON-RPC connection per session.
//
// Unexported because consumers should program against the Runtime
// interface; NewChatRuntime returns it as a Runtime.
type chatRuntime struct {
	mgr *ChatManager
}

// NewChatRuntime constructs the chat-backed Runtime. Returns the
// interface, not the concrete type, to discourage coupling to internals.
func NewChatRuntime(mgr *ChatManager) Runtime {
	return &chatRuntime{mgr: mgr}
}

func (r *chatRuntime) Name() string { return "chat" }

func (r *chatRuntime) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{
		CanFocus:        false, // ChatPanel opening is a frontend concern, not a runtime op
		CanClone:        true,  // Clone via session/load — new agent loads source's conversation
		CanCancel:       true,  // ACP session/cancel
		HasStreamingUI:  true,  // SSE stream from /api/chat/{id}/stream
		HasPermissionUI: true,  // Inline approve/deny buttons via ACP request_permission
	}
}

func (r *chatRuntime) session(pgid string) (*ChatSession, error) {
	sess := r.mgr.Get(pgid)
	if sess == nil {
		return nil, fmt.Errorf("chat session not found: %s", pgid)
	}
	return sess, nil
}

func (r *chatRuntime) SendPrompt(_ context.Context, pgid, text string) error {
	sess, err := r.session(pgid)
	if err != nil {
		return err
	}
	return sess.SendPrompt(text)
}

func (r *chatRuntime) Cancel(_ context.Context, pgid string) error {
	sess, err := r.session(pgid)
	if err != nil {
		return err
	}
	return sess.Cancel()
}

func (r *chatRuntime) Close(_ context.Context, pgid string) error {
	r.mgr.Close(pgid)
	return nil
}

func (r *chatRuntime) Focus(_ context.Context, _ string) error {
	// Chat agents are addressed via the dashboard's right-pane ChatPanel;
	// the dashboard handles opening it (via the `open-chat-panel` event
	// the frontend listens for). Nothing for the runtime to do.
	return nil
}

func (r *chatRuntime) CheckMessages(ctx context.Context, pgid string) error {
	// Send "check messages" as a regular prompt — the agent's system
	// prompt teaches it to call the MCP `check_messages` tool when it
	// sees this trigger phrase, same as in the iTerm2 flow (which types
	// the phrase into the TTY). Fire-and-forget for the same reason as
	// SendPrompt above.
	return r.SendPrompt(ctx, pgid, "check messages")
}

func (r *chatRuntime) StopTask(_ context.Context, pgid, taskId string) error {
	sess, err := r.session(pgid)
	if err != nil {
		return err
	}
	return sess.StopTask(taskId)
}
