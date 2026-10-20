package server

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// ──── Test 1: Send + Consume roundtrip ───────────────────────────────────────

func TestSendAndConsumeRoundtrip(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	msg, err := ms.Send("sender-1", "Sender", "recipient-1", "Recipient", "hello world")
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil message")
	}
	if msg.Content != "hello world" {
		t.Errorf("content = %q, want %q", msg.Content, "hello world")
	}
	if msg.From != "sender-1" {
		t.Errorf("from = %q, want %q", msg.From, "sender-1")
	}
	if msg.To != "recipient-1" {
		t.Errorf("to = %q, want %q", msg.To, "recipient-1")
	}
	if msg.Delivered {
		t.Error("new message should have delivered=false")
	}
	if msg.ID == "" {
		t.Error("message ID should not be empty")
	}

	// ConsumePending should return the message
	consumed := ms.ConsumePending("recipient-1")
	if len(consumed) != 1 {
		t.Fatalf("ConsumePending returned %d messages, want 1", len(consumed))
	}
	if consumed[0].Content != "hello world" {
		t.Errorf("consumed content = %q, want %q", consumed[0].Content, "hello world")
	}
	if consumed[0].ID != msg.ID {
		t.Errorf("consumed ID = %q, want %q", consumed[0].ID, msg.ID)
	}

	// After consume, mailbox should be empty
	again := ms.ConsumePending("recipient-1")
	if len(again) != 0 {
		t.Errorf("mailbox should be empty after consume, got %d messages", len(again))
	}
}

// ──── Test 2: Send + GetPending ──────────────────────────────────────────────

func TestSendAndGetPending(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	msg, err := ms.Send("alice", "Alice", "bob", "Bob", "are you there?")
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	pending := ms.GetPending("bob")
	if len(pending) != 1 {
		t.Fatalf("GetPending returned %d messages, want 1", len(pending))
	}
	if pending[0].ID != msg.ID {
		t.Errorf("pending ID = %q, want %q", pending[0].ID, msg.ID)
	}
	if pending[0].Delivered {
		t.Error("pending message should have delivered=false")
	}
	if pending[0].Content != "are you there?" {
		t.Errorf("content = %q, want %q", pending[0].Content, "are you there?")
	}

	// GetPending is non-destructive: calling again should return same result
	pending2 := ms.GetPending("bob")
	if len(pending2) != 1 {
		t.Fatalf("second GetPending returned %d messages, want 1", len(pending2))
	}
}

// ──── Test 3: DeliverPending ─────────────────────────────────────────────────

func TestDeliverPending(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	_, err := ms.Send("alice", "Alice", "bob", "Bob", "check this out")
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// DeliverPending should return the message and mark it delivered
	delivered := ms.DeliverPending("bob")
	if len(delivered) != 1 {
		t.Fatalf("DeliverPending returned %d messages, want 1", len(delivered))
	}
	if !delivered[0].Delivered {
		t.Error("delivered message should have delivered=true")
	}
	if delivered[0].Content != "check this out" {
		t.Errorf("content = %q, want %q", delivered[0].Content, "check this out")
	}

	// GetPending should now return empty (filters out delivered=true)
	pending := ms.GetPending("bob")
	if len(pending) != 0 {
		t.Errorf("GetPending after DeliverPending should be empty, got %d", len(pending))
	}

	// ConsumePending should still return it (reads all messages in mailbox)
	consumed := ms.ConsumePending("bob")
	if len(consumed) != 1 {
		t.Fatalf("ConsumePending after DeliverPending returned %d, want 1", len(consumed))
	}
	if consumed[0].Content != "check this out" {
		t.Errorf("consumed content = %q, want %q", consumed[0].Content, "check this out")
	}

	// Second DeliverPending should return nothing (all already delivered)
	delivered2 := ms.DeliverPending("bob")
	// After ConsumePending deleted the files, there's nothing left
	if len(delivered2) != 0 {
		t.Errorf("second DeliverPending after Consume should be empty, got %d", len(delivered2))
	}
}

// ──── Test 4: Budget tracking ────────────────────────────────────────────────
// Budget is tracked via _msg_budget files in the mailbox directory.
// The file stores a plain integer count. This tests the file-level behavior
// described in CLAUDE.md: "5 per turn, tracked in _msg_budget, reset on UserPromptSubmit".

func TestBudgetTracking(t *testing.T) {
	dir := setupTestDir(t)
	budgetDir := filepath.Join(dir, "messages", "agent-123")
	os.MkdirAll(budgetDir, 0755)
	budgetFile := filepath.Join(budgetDir, "_msg_budget")

	// getMessageCount helper reads the budget file
	getMessageCount := func() int {
		data, err := os.ReadFile(budgetFile)
		if err != nil {
			return 0
		}
		n, err := strconv.Atoi(string(data))
		if err != nil {
			return 0
		}
		return n
	}

	// incrementMessageCount helper increments the budget file
	incrementMessageCount := func() {
		current := getMessageCount()
		os.WriteFile(budgetFile, []byte(strconv.Itoa(current+1)), 0644)
	}

	// Write "0" to budget file, verify returns 0
	os.WriteFile(budgetFile, []byte("0"), 0644)
	if got := getMessageCount(); got != 0 {
		t.Errorf("after writing 0: getMessageCount = %d, want 0", got)
	}

	// Increment, verify returns 1
	incrementMessageCount()
	if got := getMessageCount(); got != 1 {
		t.Errorf("after increment: getMessageCount = %d, want 1", got)
	}

	// Increment again
	incrementMessageCount()
	if got := getMessageCount(); got != 2 {
		t.Errorf("after second increment: getMessageCount = %d, want 2", got)
	}

	// Reset (write "0"), verify returns 0
	os.WriteFile(budgetFile, []byte("0"), 0644)
	if got := getMessageCount(); got != 0 {
		t.Errorf("after reset: getMessageCount = %d, want 0", got)
	}

	// Non-existent budget file returns 0
	os.Remove(budgetFile)
	if got := getMessageCount(); got != 0 {
		t.Errorf("missing file: getMessageCount = %d, want 0", got)
	}
}

// ──── Test 5: Clone routing ──────────────────────────────────────────────────
// Two agents share the same Claude session_id (one is a clone) but have
// different pokegent_id values. Messages sent to each pokegent_id
// should land in separate mailboxes.

func TestCloneRouting(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)
	runDir := filepath.Join(dir, "running")

	sharedSessionID := "claude-shared-session"
	pokegentA := "pokegent-original-uuid"
	pokegentB := "pokegent-clone-uuid"

	// Create two running files with same session_id but different pokegent_id
	writeTestJSON(t, filepath.Join(runDir, "client-"+pokegentA+".json"), RunningSession{
		Profile:     "client",
		SessionID:   sharedSessionID,
		PokegentID:  pokegentA,
		PID:         1001,
		DisplayName: "Original",
	})
	writeTestJSON(t, filepath.Join(runDir, "client-"+pokegentB+".json"), RunningSession{
		Profile:     "client",
		SessionID:   sharedSessionID,
		PokegentID:  pokegentB,
		PID:         1002,
		DisplayName: "Clone",
	})

	// Send messages to each pokegent_id (messages are routed by the "to" field)
	_, err := ms.Send("coordinator", "Coordinator", pokegentA, "Original", "task A for original")
	if err != nil {
		t.Fatalf("Send to original failed: %v", err)
	}

	_, err = ms.Send("coordinator", "Coordinator", pokegentB, "Clone", "task B for clone")
	if err != nil {
		t.Fatalf("Send to clone failed: %v", err)
	}

	// Each agent's mailbox should only contain their own messages
	originalMsgs := ms.ConsumePending(pokegentA)
	if len(originalMsgs) != 1 {
		t.Fatalf("original mailbox has %d messages, want 1", len(originalMsgs))
	}
	if originalMsgs[0].Content != "task A for original" {
		t.Errorf("original got %q, want %q", originalMsgs[0].Content, "task A for original")
	}

	cloneMsgs := ms.ConsumePending(pokegentB)
	if len(cloneMsgs) != 1 {
		t.Fatalf("clone mailbox has %d messages, want 1", len(cloneMsgs))
	}
	if cloneMsgs[0].Content != "task B for clone" {
		t.Errorf("clone got %q, want %q", cloneMsgs[0].Content, "task B for clone")
	}

	// Verify no cross-contamination: both mailboxes are now empty
	if msgs := ms.ConsumePending(pokegentA); len(msgs) != 0 {
		t.Errorf("original mailbox should be empty after consume, got %d", len(msgs))
	}
	if msgs := ms.ConsumePending(pokegentB); len(msgs) != 0 {
		t.Errorf("clone mailbox should be empty after consume, got %d", len(msgs))
	}
}

// ──── Test 6: Empty inbox ────────────────────────────────────────────────────

func TestEmptyInbox(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	// ConsumePending on a nonexistent session should return empty slice, not nil
	result := ms.ConsumePending("nonexistent-session-id")
	if result == nil {
		// nil is acceptable too since Go range works fine on nil slices,
		// but we verify it doesn't panic and has length 0
		result = []Message{}
	}
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %d messages", len(result))
	}

	// GetPending on nonexistent session should also return empty/nil
	pending := ms.GetPending("nonexistent-session-id")
	if len(pending) != 0 {
		t.Errorf("GetPending on nonexistent session: got %d messages, want 0", len(pending))
	}

	// DeliverPending on nonexistent session should return empty/nil
	delivered := ms.DeliverPending("nonexistent-session-id")
	if len(delivered) != 0 {
		t.Errorf("DeliverPending on nonexistent session: got %d messages, want 0", len(delivered))
	}
}

// ──── Test 7: History ────────────────────────────────────────────────────────

func TestHistoryBasic(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	// Send a few messages
	_, _ = ms.Send("alice", "Alice", "bob", "Bob", "msg 1")
	time.Sleep(time.Millisecond) // ensure different timestamps/IDs
	_, _ = ms.Send("bob", "Bob", "alice", "Alice", "msg 2")
	time.Sleep(time.Millisecond)
	_, _ = ms.Send("alice", "Alice", "charlie", "Charlie", "msg 3")

	history := ms.GetHistory()
	if len(history) != 3 {
		t.Fatalf("history has %d messages, want 3", len(history))
	}

	// Verify messages are in order (oldest first)
	if history[0].Content != "msg 1" {
		t.Errorf("history[0].Content = %q, want %q", history[0].Content, "msg 1")
	}
	if history[1].Content != "msg 2" {
		t.Errorf("history[1].Content = %q, want %q", history[1].Content, "msg 2")
	}
	if history[2].Content != "msg 3" {
		t.Errorf("history[2].Content = %q, want %q", history[2].Content, "msg 3")
	}

	// Verify sender/receiver data preserved in history
	if history[0].From != "alice" || history[0].To != "bob" {
		t.Errorf("history[0] from/to = %q/%q, want alice/bob", history[0].From, history[0].To)
	}
	if history[1].From != "bob" || history[1].To != "alice" {
		t.Errorf("history[1] from/to = %q/%q, want bob/alice", history[1].From, history[1].To)
	}
}

func TestHistoryCapsAt200(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	// Send 210 messages to exceed the 200 cap
	for i := 0; i < 210; i++ {
		_, err := ms.Send(
			"sender", "Sender",
			"recipient", "Recipient",
			"message "+strconv.Itoa(i),
		)
		if err != nil {
			t.Fatalf("Send #%d failed: %v", i, err)
		}
	}

	history := ms.GetHistory()
	if len(history) != 200 {
		t.Fatalf("history has %d messages, want 200 (capped)", len(history))
	}

	// The oldest 10 messages should be trimmed; first message should be #10
	if history[0].Content != "message 10" {
		t.Errorf("history[0].Content = %q, want %q", history[0].Content, "message 10")
	}
	// Last message should be #209
	if history[199].Content != "message 209" {
		t.Errorf("history[199].Content = %q, want %q", history[199].Content, "message 209")
	}
}

func TestHistoryPersistence(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	_, _ = ms.Send("alice", "Alice", "bob", "Bob", "persistent msg")

	// Create a new MessageStore from the same directory — should load saved history
	ms2 := NewMessageStore(dir)
	history := ms2.GetHistory()
	if len(history) != 1 {
		t.Fatalf("reloaded history has %d messages, want 1", len(history))
	}
	if history[0].Content != "persistent msg" {
		t.Errorf("reloaded content = %q, want %q", history[0].Content, "persistent msg")
	}
}

// ──── Test: Multiple messages ordering ───────────────────────────────────────

func TestMultipleMessagesOrdering(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	// Send multiple messages to the same recipient
	_, _ = ms.Send("alice", "Alice", "bob", "Bob", "first")
	time.Sleep(time.Millisecond)
	_, _ = ms.Send("charlie", "Charlie", "bob", "Bob", "second")
	time.Sleep(time.Millisecond)
	_, _ = ms.Send("alice", "Alice", "bob", "Bob", "third")

	// GetPending should return all three in timestamp order
	pending := ms.GetPending("bob")
	if len(pending) != 3 {
		t.Fatalf("GetPending returned %d messages, want 3", len(pending))
	}
	if pending[0].Content != "first" {
		t.Errorf("pending[0] = %q, want %q", pending[0].Content, "first")
	}
	if pending[1].Content != "second" {
		t.Errorf("pending[1] = %q, want %q", pending[1].Content, "second")
	}
	if pending[2].Content != "third" {
		t.Errorf("pending[2] = %q, want %q", pending[2].Content, "third")
	}
}

// ──── Test: ConsumePending updates history delivered flag ─────────────────────

func TestConsumePendingUpdatesHistory(t *testing.T) {
	dir := setupTestDir(t)
	ms := NewMessageStore(dir)

	_, _ = ms.Send("alice", "Alice", "bob", "Bob", "to be consumed")

	// Before consume: history shows delivered=false
	history := ms.GetHistory()
	if len(history) != 1 {
		t.Fatalf("history has %d messages, want 1", len(history))
	}
	if history[0].Delivered {
		t.Error("history should show delivered=false before consume")
	}

	// Consume
	ms.ConsumePending("bob")

	// After consume: history should show delivered=true
	history = ms.GetHistory()
	if len(history) != 1 {
		t.Fatalf("history after consume has %d messages, want 1", len(history))
	}
	if !history[0].Delivered {
		t.Error("history should show delivered=true after consume")
	}
}
