package server

import (
	"context"
	"fmt"
	"time"
)

// iterm2Runtime implements the Runtime interface for agents running in an
// iTerm2 tab via pokegent.sh. Operations are AppleScript / TTY-injection
// based — the actual Claude CLI is the agent process; this runtime just
// addresses it through its terminal session.
//
// Unexported because consumers should program against the Runtime
// interface; NewITerm2Runtime returns it as a Runtime.
type iterm2Runtime struct {
	state    *StateManager
	terminal TerminalIntegration
}

// NewITerm2Runtime constructs the iTerm2-backed Runtime. Returns the
// interface, not the concrete type, to discourage coupling to internals.
func NewITerm2Runtime(state *StateManager, terminal TerminalIntegration) Runtime {
	return &iterm2Runtime{state: state, terminal: terminal}
}

func (r *iterm2Runtime) Name() string { return "iterm2" }

func (r *iterm2Runtime) Capabilities() RuntimeCapabilities {
	return RuntimeCapabilities{
		CanFocus:        true,
		CanClone:        true, // pokegent --resume --fork-session in a new tab
		CanCancel:       true, // Cancel() sends Esc into the TTY (claude-cli cancel)
		HasStreamingUI:  false,
		HasPermissionUI: false,
	}
}

// resolveTTY returns the agent's iTerm session id + tty, or an error.
// Centralizes the "is this iterm2 agent reachable?" check that every
// method needs.
func (r *iterm2Runtime) resolveTTY(pgid string) (itermSID, tty string, err error) {
	agent := r.state.GetAgent(pgid)
	if agent == nil {
		return "", "", fmt.Errorf("agent not found: %s", pgid)
	}
	if agent.TTY == "" {
		return "", "", fmt.Errorf("agent has no TTY (not iterm2-backed?): %s", pgid)
	}
	return agent.ITermSessionID, agent.TTY, nil
}

func (r *iterm2Runtime) SendPrompt(_ context.Context, pgid, text string) error {
	itermSID, tty, err := r.resolveTTY(pgid)
	if err != nil {
		return err
	}
	go r.terminal.WriteText(itermSID, tty, text)
	return nil
}

func (r *iterm2Runtime) Cancel(_ context.Context, pgid string) error {
	// claude-cli supports Esc-to-cancel. We send the literal escape character;
	// iTerm passes it through to claude as a SIGINT-equivalent.
	itermSID, tty, err := r.resolveTTY(pgid)
	if err != nil {
		return err
	}
	go r.terminal.WriteText(itermSID, tty, "\x1b")
	return nil
}

func (r *iterm2Runtime) Close(_ context.Context, pgid string) error {
	itermSID, tty, err := r.resolveTTY(pgid)
	if err != nil {
		return err
	}
	go func() {
		r.terminal.WriteText(itermSID, tty, "/exit")
		// Give claude time to flush its history + exit gracefully before
		// closing the tab. Without this delay, the tab dies mid-exit and
		// the JSONL transcript may not record the final compaction.
		time.Sleep(2 * time.Second)
		r.terminal.CloseSession(itermSID, tty)
	}()
	return nil
}

func (r *iterm2Runtime) Focus(_ context.Context, pgid string) error {
	itermSID, tty, err := r.resolveTTY(pgid)
	if err != nil {
		return err
	}
	return r.terminal.FocusSession(itermSID, tty)
}

func (r *iterm2Runtime) CheckMessages(_ context.Context, pgid string) error {
	itermSID, tty, err := r.resolveTTY(pgid)
	if err != nil {
		return err
	}
	go r.terminal.WriteText(itermSID, tty, "check messages")
	return nil
}

func (r *iterm2Runtime) StopTask(_ context.Context, _, _ string) error {
	return fmt.Errorf("iterm2 runtime does not support StopTask")
}
