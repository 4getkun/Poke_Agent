package services

import (
	"testing"

	"pokegents/dashboard/server/core"
	"pokegents/dashboard/server/store"
)

// TestFullMessageWorkflow tests the complete send → deliver → consume pipeline.
func TestFullMessageWorkflow(t *testing.T) {
	dir := t.TempDir()
	fs := store.NewFileStore(dir)
	agents := map[string]*AgentInfo{
		"sender-11111111": {State: "busy", IsAlive: true},
		"recver-22222222": {State: "idle", IsAlive: true, TTY: "/dev/ttys001", ITermSessionID: "iterm-r", LastUpdated: "2020-01-01T00:00:00Z"},
	}
	svc := NewMessagingService(fs.Messages, nil, func(id string) *AgentInfo { return agents[id] }, nil)

	// 1. Send message
	msg, needsNudge, err := svc.Send("sender-11111111", "Sender", "recver-22222222", "Receiver", "hello from sender")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg.Content != "hello from sender" {
		t.Errorf("msg.Content = %q", msg.Content)
	}
	if !needsNudge {
		t.Error("receiver is done — should need nudge")
	}

	// 2. Hook delivers (marks delivered, returns content for systemMessage)
	delivered, err := svc.Deliver("recver-22222222")
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(delivered) != 1 {
		t.Fatalf("expected 1 delivered, got %d", len(delivered))
	}
	if !delivered[0].Delivered {
		t.Error("delivered message should be marked")
	}

	// 3. GetPending now empty (all delivered)
	pending, _ := svc.GetPending("recver-22222222")
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after deliver, got %d", len(pending))
	}

	// 4. Agent calls check_messages (consume)
	consumed, err := svc.Consume("recver-22222222")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(consumed) != 1 {
		t.Fatalf("expected 1 consumed, got %d", len(consumed))
	}

	// 5. Mailbox completely empty now
	consumed, _ = svc.Consume("recver-22222222")
	if len(consumed) != 0 {
		t.Errorf("expected empty after consume, got %d", len(consumed))
	}
}

// TestStateMachineWorkflow tests a full agent lifecycle through the state machine.
func TestStateMachineWorkflow(t *testing.T) {
	state := core.SessionState("")
	detail := ""

	// 1. Session starts
	r := core.ApplyEvent(state, detail, core.HookEvent{HookEventName: "SessionStart"})
	if r.NewState != core.StateIdle {
		t.Fatalf("SessionStart: expected idle, got %s", r.NewState)
	}
	state = r.NewState

	// 2. User sends prompt
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "UserPromptSubmit", Prompt: "fix the bug"})
	if r.NewState != core.StateBusy {
		t.Fatalf("UserPromptSubmit: expected busy, got %s", r.NewState)
	}
	state = r.NewState

	// 3. Tool use
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "PreToolUse", ToolName: "Edit"})
	if r.NewState != core.StateBusy {
		t.Fatalf("PreToolUse: expected busy, got %s", r.NewState)
	}

	// 4. Permission needed
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "PermissionRequest", ToolName: "Bash"})
	if r.NewState != core.StateNeedsInput {
		t.Fatalf("PermissionRequest: expected needs_input, got %s", r.NewState)
	}
	state = r.NewState

	// 5. User grants permission, agent resumes → new prompt
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "UserPromptSubmit"})
	if r.NewState != core.StateBusy {
		t.Fatalf("UserPromptSubmit after permission: expected busy, got %s", r.NewState)
	}
	state = r.NewState

	// 6. Agent finishes
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "Stop", LastAssistantMessage: "Bug fixed"})
	if r.NewState != core.StateIdle {
		t.Fatalf("Stop: expected idle, got %s", r.NewState)
	}
	if r.Summary != "Bug fixed" {
		t.Errorf("Summary = %q, want %q", r.Summary, "Bug fixed")
	}
	state = r.NewState

	// 7. Late PostToolUse (race) — should be dropped
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "PostToolUse", ToolName: "Bash"})
	if !r.Skip {
		t.Error("late PostToolUse after Stop should be skipped")
	}

	// 8. New prompt starts next turn
	r = core.ApplyEvent(state, "", core.HookEvent{HookEventName: "UserPromptSubmit", Prompt: "next task"})
	if r.NewState != core.StateBusy {
		t.Fatalf("new UserPromptSubmit: expected busy, got %s", r.NewState)
	}
}

// TestActivityOverlapWorkflow tests the full activity recording and overlap detection flow.
func TestActivityOverlapWorkflow(t *testing.T) {
	dir := t.TempDir()
	fs := store.NewFileStore(dir)
	actSvc := NewActivityService(fs.Activity)

	// Agent A edits server.go
	actSvc.RecordTurn("proj-overlap", "agent-a-1111", "Agent A", []string{"server.go", "models.go"})

	// Agent B checks for recent activity — sees Agent A's changes
	entries, _ := actSvc.GetRecent("proj-overlap", "agent-b-2222", 10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry from Agent A, got %d", len(entries))
	}

	// Agent B is working on server.go too — detect overlap
	overlaps := actSvc.DetectOverlaps(entries, []string{"server.go"})
	if len(overlaps) != 1 {
		t.Fatalf("expected 1 overlap, got %d", len(overlaps))
	}

	// Agent B is NOT working on models.go — no overlap on different file
	overlaps = actSvc.DetectOverlaps(entries, []string{"frontend.tsx"})
	if len(overlaps) != 0 {
		t.Errorf("expected 0 overlaps, got %d", len(overlaps))
	}

	// Agent A checks — should not see own entry
	entries, _ = actSvc.GetRecent("proj-overlap", "agent-a-1111", 10)
	if len(entries) != 0 {
		t.Errorf("Agent A should not see own entries, got %d", len(entries))
	}
}

// TestCloneIsolation tests that clones with different pokegent_ids
// have isolated mailboxes.
func TestCloneIsolation(t *testing.T) {
	dir := t.TempDir()
	fs := store.NewFileStore(dir)
	svc := NewMessagingService(fs.Messages, nil, func(id string) *AgentInfo {
		return &AgentInfo{State: "idle", IsAlive: true}
	}, nil)

	// Send to original and clone separately
	svc.Send("boss-11111111", "Boss", "original-pokegent-id", "Original", "task for original")
	svc.Send("boss-11111111", "Boss", "clone-pokegent-iddd", "Clone", "task for clone")

	// Each gets only their message
	origMsgs, _ := svc.GetPending("original-pokegent-id")
	cloneMsgs, _ := svc.GetPending("clone-pokegent-iddd")

	if len(origMsgs) != 1 || origMsgs[0].Content != "task for original" {
		t.Errorf("original got wrong messages: %v", origMsgs)
	}
	if len(cloneMsgs) != 1 || cloneMsgs[0].Content != "task for clone" {
		t.Errorf("clone got wrong messages: %v", cloneMsgs)
	}

	// Consuming one doesn't affect the other
	svc.Consume("original-pokegent-id")
	cloneMsgs, _ = svc.GetPending("clone-pokegent-iddd")
	if len(cloneMsgs) != 1 {
		t.Errorf("clone should still have message after original consumed, got %d", len(cloneMsgs))
	}
}
