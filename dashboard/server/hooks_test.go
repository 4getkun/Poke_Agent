package server

import (
	"testing"
)

// TestStateMachine tests all state transitions in UpdateFromEvent.
// These must match the bash hook's state machine in status-update.sh.
func TestStateMachine(t *testing.T) {
	tests := []struct {
		name          string
		initialState  string
		event         string
		notification  string // for Notification events
		expectedState string
		expectedNil   bool // expect nil return (event dropped)
	}{
		// UserPromptSubmit → busy (from any state)
		{"idle to busy", "idle", "UserPromptSubmit", "", "busy", false},
		{"idle(done) to busy", "idle", "UserPromptSubmit", "", "busy", false},
		{"error to busy", "error", "UserPromptSubmit", "", "busy", false},
		{"needs_input to busy", "needs_input", "UserPromptSubmit", "", "busy", false},
		{"new session to busy", "", "UserPromptSubmit", "", "busy", false},

		// PreToolUse/PostToolUse — only if currently busy
		{"busy stays busy on PreToolUse", "busy", "PreToolUse", "", "busy", false},
		{"busy stays busy on PostToolUse", "busy", "PostToolUse", "", "busy", false},
		{"idle blocks PreToolUse", "idle", "PreToolUse", "", "", true},   // dropped
		{"idle blocks PostToolUse", "idle", "PostToolUse", "", "", true}, // dropped
		{"error blocks PreToolUse", "error", "PreToolUse", "", "", true}, // dropped

		// Stop → idle
		{"busy to idle on Stop", "busy", "Stop", "", "idle", false},

		// StopFailure → error
		{"busy to error on StopFailure", "busy", "StopFailure", "", "error", false},

		// PermissionRequest → needs_input
		{"busy to needs_input", "busy", "PermissionRequest", "", "needs_input", false},

		// Notification(idle_prompt) — only busy → idle
		{"busy to idle on idle_prompt", "busy", "Notification", "idle_prompt", "idle", false},
		{"idle unchanged on idle_prompt", "idle", "Notification", "idle_prompt", "idle", false},

		// SessionStart → idle
		{"new session idle", "", "SessionStart", "", "idle", false},

		// SessionEnd → removed (nil)
		{"session end", "busy", "SessionEnd", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewStateManager("/tmp/test-pokegents", "/tmp/test-claude-projects")
			sm.statuses = make(map[string]StatusFile)
			sm.running = make(map[string]RunningSession)
			sm.agents = make(map[string]*AgentState)
			sm.contexts = make(map[string]ContextUsage)
			sm.activityFeeds = make(map[string][]ActivityItem)
			sm.nameOverrides = make(map[string]string)
			sm.sessionToPokegent = make(map[string]string)

			sid := "test-session-id"

			// Set initial state
			if tt.initialState != "" {
				sm.statuses[sid] = StatusFile{
					SessionID: sid,
					State:     tt.initialState,
				}
			}

			evt := HookEvent{
				SessionID:        sid,
				HookEventName:    tt.event,
				NotificationType: tt.notification,
			}

			result := sm.UpdateFromEvent(evt)

			if tt.expectedNil {
				if result != nil {
					t.Errorf("expected nil result, got state=%s", result.State)
				}
				return
			}

			if result == nil {
				// Check if state was updated in statuses map directly
				if sf, ok := sm.statuses[sid]; ok {
					if sf.State != tt.expectedState {
						t.Errorf("expected state %q, got %q (from statuses map)", tt.expectedState, sf.State)
					}
					return
				}
				t.Fatalf("expected non-nil result with state %q", tt.expectedState)
				return
			}

			if result.State != tt.expectedState {
				t.Errorf("expected state %q, got %q", tt.expectedState, result.State)
			}
		})
	}
}

// TestPostToolUseRaceGuard verifies that slow PostToolUse events arriving
// after Stop don't overwrite "idle" with "busy".
func TestPostToolUseRaceGuard(t *testing.T) {
	sm := NewStateManager("/tmp/test-pokegents", "/tmp/test-claude-projects")
	sm.statuses = make(map[string]StatusFile)
	sm.running = make(map[string]RunningSession)
	sm.agents = make(map[string]*AgentState)
	sm.contexts = make(map[string]ContextUsage)
	sm.activityFeeds = make(map[string][]ActivityItem)
	sm.nameOverrides = make(map[string]string)
	sm.sessionToPokegent = make(map[string]string)

	sid := "race-test"

	// Agent finishes (Stop → idle)
	sm.UpdateFromEvent(HookEvent{SessionID: sid, HookEventName: "UserPromptSubmit"})
	sm.UpdateFromEvent(HookEvent{SessionID: sid, HookEventName: "Stop"})

	// Late PostToolUse arrives — must NOT overwrite idle
	result := sm.UpdateFromEvent(HookEvent{SessionID: sid, HookEventName: "PostToolUse", ToolName: "Bash"})
	if result != nil {
		t.Errorf("late PostToolUse should be dropped, got state=%s", result.State)
	}

	// Verify state is still idle
	if sf, ok := sm.statuses[sid]; ok {
		if sf.State != "idle" {
			t.Errorf("state should still be idle, got %s", sf.State)
		}
	}
}
