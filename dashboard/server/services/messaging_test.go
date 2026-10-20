package services

import (
	"testing"

	"pokegents/dashboard/server/store"
)

func newTestMessaging(t *testing.T) (*MessagingService, string) {
	dir := t.TempDir()
	fs := store.NewFileStore(dir)
	var nudgedSessions []string
	wake := func(pgid string) error {
		nudgedSessions = append(nudgedSessions, pgid)
		return nil
	}
	_ = nudgedSessions // kept for future assertions; compiler-quiet for now
	agents := map[string]*AgentInfo{
		"agent-aaa-1111": {State: "idle", IsAlive: true, TTY: "/dev/ttys001", ITermSessionID: "iterm-a", LastUpdated: "2020-01-01T00:00:00Z"},
		"agent-bbb-2222": {State: "busy", IsAlive: true, TTY: "/dev/ttys002", ITermSessionID: "iterm-b"},
		"clone-aaa-3333": {State: "idle", IsAlive: true, TTY: "/dev/ttys003", ITermSessionID: "iterm-c", LastUpdated: "2020-01-01T00:00:00Z"},
	}
	getAgent := func(id string) *AgentInfo { return agents[id] }

	svc := NewMessagingService(fs.Messages, wake, getAgent, nil)
	return svc, dir
}

func TestSendReturnsNeedsNudge(t *testing.T) {
	svc, _ := newTestMessaging(t)

	// Agent A is done → needs nudge
	msg, needsNudge, err := svc.Send("sender", "Sender", "agent-aaa-1111", "Agent A", "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}
	if !needsNudge {
		t.Error("expected needsNudge=true for done agent")
	}

	// Agent B is busy → no nudge
	_, needsNudge, err = svc.Send("sender", "Sender", "agent-bbb-2222", "Agent B", "hello")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if needsNudge {
		t.Error("expected needsNudge=false for busy agent")
	}

	// Clone A is idle → needs nudge
	_, needsNudge, _ = svc.Send("sender", "Sender", "clone-aaa-3333", "Clone A", "hello")
	if !needsNudge {
		t.Error("expected needsNudge=true for idle agent")
	}
}

func TestDeliverAndConsume(t *testing.T) {
	svc, _ := newTestMessaging(t)

	// Send 2 messages
	svc.Send("sender", "Sender", "agent-aaa-1111", "Agent A", "msg 1")
	svc.Send("sender", "Sender", "agent-aaa-1111", "Agent A", "msg 2")

	// GetPending returns both
	pending, err := svc.GetPending("agent-aaa-1111")
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}

	// Deliver marks as delivered
	delivered, err := svc.Deliver("agent-aaa-1111")
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(delivered) != 2 {
		t.Fatalf("expected 2 delivered, got %d", len(delivered))
	}
	for _, m := range delivered {
		if !m.Delivered {
			t.Error("delivered message should have Delivered=true")
		}
	}

	// GetPending now returns empty (all delivered)
	pending, _ = svc.GetPending("agent-aaa-1111")
	if len(pending) != 0 {
		t.Errorf("expected 0 pending after deliver, got %d", len(pending))
	}

	// Consume deletes files
	consumed, err := svc.Consume("agent-aaa-1111")
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(consumed) != 2 {
		t.Fatalf("expected 2 consumed, got %d", len(consumed))
	}

	// Consume again → empty
	consumed, _ = svc.Consume("agent-aaa-1111")
	if len(consumed) != 0 {
		t.Errorf("expected 0 after second consume, got %d", len(consumed))
	}
}

func TestBudget(t *testing.T) {
	svc, _ := newTestMessaging(t)

	budget, _ := svc.GetBudget("agent-aaa-1111")
	if budget != 0 {
		t.Errorf("initial budget = %d, want 0", budget)
	}

	svc.ResetBudget("agent-aaa-1111")
	budget, _ = svc.GetBudget("agent-aaa-1111")
	if budget != 0 {
		t.Errorf("after reset budget = %d, want 0", budget)
	}
}

func TestCloneRouting(t *testing.T) {
	svc, _ := newTestMessaging(t)

	// Send to agent-a and clone-a — separate mailboxes
	svc.Send("sender", "Sender", "agent-aaa-1111", "Agent A", "for original")
	svc.Send("sender", "Sender", "clone-aaa-3333", "Clone A", "for clone")

	// Each has exactly 1 message
	pendingA, _ := svc.GetPending("agent-aaa-1111")
	if len(pendingA) != 1 {
		t.Errorf("agent-a: expected 1 pending, got %d", len(pendingA))
	}
	if pendingA[0].Content != "for original" {
		t.Errorf("agent-a got wrong message: %q", pendingA[0].Content)
	}

	pendingClone, _ := svc.GetPending("clone-aaa-3333")
	if len(pendingClone) != 1 {
		t.Errorf("clone-a: expected 1 pending, got %d", len(pendingClone))
	}
	if pendingClone[0].Content != "for clone" {
		t.Errorf("clone-a got wrong message: %q", pendingClone[0].Content)
	}
}

func TestNudgeSkipsBusy(t *testing.T) {
	svc, _ := newTestMessaging(t)

	// Busy agent — should not queue a nudge
	svc.QueueNudge("agent-bbb-2222")
	if svc.HasPendingNudge("agent-bbb-2222") {
		t.Error("should not queue nudge for busy agent")
	}
}

func TestFormatMessages(t *testing.T) {
	msgs := []store.Message{
		{FromName: "Agent A", Content: "hello"},
		{FromName: "Agent B", Content: "world"},
	}
	result := FormatMessages(msgs)
	if result == "" {
		t.Error("expected non-empty formatted string")
	}
	if !contains(result, "[Message from Agent A]: hello") {
		t.Errorf("missing Agent A message in: %s", result)
	}
	if !contains(result, "[Message from Agent B]: world") {
		t.Errorf("missing Agent B message in: %s", result)
	}
}

func TestFormatMessagesEmpty(t *testing.T) {
	result := FormatMessages(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
