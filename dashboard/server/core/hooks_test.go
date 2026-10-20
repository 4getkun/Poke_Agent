package core

import "testing"

func TestApplyEvent(t *testing.T) {
	tests := []struct {
		name          string
		current       SessionState
		currentDetail string
		event         HookEvent
		wantState     SessionState
		wantSkip      bool
	}{
		// UserPromptSubmit → busy from any state
		{"idle→busy", StateIdle, "", HookEvent{HookEventName: "UserPromptSubmit"}, StateBusy, false},
		{"idle→busy (was done→busy)", StateIdle, "", HookEvent{HookEventName: "UserPromptSubmit"}, StateBusy, false},
		{"error→busy", StateError, "", HookEvent{HookEventName: "UserPromptSubmit"}, StateBusy, false},
		{"needs_input→busy", StateNeedsInput, "", HookEvent{HookEventName: "UserPromptSubmit"}, StateBusy, false},
		{"empty→busy", "", "", HookEvent{HookEventName: "UserPromptSubmit"}, StateBusy, false},

		// Tool events — only if busy
		{"busy+PreToolUse", StateBusy, "", HookEvent{HookEventName: "PreToolUse", ToolName: "Bash"}, StateBusy, false},
		{"done blocks PreToolUse", StateIdle, "", HookEvent{HookEventName: "PreToolUse"}, "", true},
		{"idle blocks PostToolUse", StateIdle, "", HookEvent{HookEventName: "PostToolUse"}, "", true},
		{"error blocks PreToolUse", StateError, "", HookEvent{HookEventName: "PreToolUse"}, "", true},

		// Stop → idle (Phase 2: done collapsed)
		{"busy→idle on Stop", StateBusy, "", HookEvent{HookEventName: "Stop"}, StateIdle, false},

		// StopFailure → error
		{"busy→error", StateBusy, "", HookEvent{HookEventName: "StopFailure"}, StateError, false},

		// PermissionRequest → needs_input
		{"busy→needs_input", StateBusy, "", HookEvent{HookEventName: "PermissionRequest", ToolName: "Bash"}, StateNeedsInput, false},

		// idle_prompt: busy→idle, others skip
		{"busy→idle on idle_prompt", StateBusy, "", HookEvent{HookEventName: "Notification", NotificationType: "idle_prompt"}, StateIdle, false},
		{"idle stays on idle_prompt", StateIdle, "", HookEvent{HookEventName: "Notification", NotificationType: "idle_prompt"}, "", true},
		{"idle stays on idle_prompt", StateIdle, "", HookEvent{HookEventName: "Notification", NotificationType: "idle_prompt"}, "", true},

		// SessionStart
		{"new→idle", "", "", HookEvent{HookEventName: "SessionStart"}, StateIdle, false},
		{"busy blocks SessionStart (clone protection)", StateBusy, "", HookEvent{HookEventName: "SessionStart"}, "", true},
		{"compacting→idle+Compacted", "", "compacting", HookEvent{HookEventName: "SessionStart"}, StateIdle, false},

		// SessionEnd
		{"end removes", StateBusy, "", HookEvent{HookEventName: "SessionEnd"}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ApplyEvent(tt.current, tt.currentDetail, tt.event)
			if tt.wantSkip {
				if !result.Skip {
					t.Errorf("expected Skip, got state=%s", result.NewState)
				}
				return
			}
			if result.Skip {
				t.Fatalf("unexpected Skip for %s", tt.name)
			}
			if result.NewState != tt.wantState {
				t.Errorf("state = %q, want %q", result.NewState, tt.wantState)
			}
		})
	}
}

func TestPostToolUseRaceGuard(t *testing.T) {
	// Simulate: busy → Stop(idle) → late PostToolUse arrives
	r1 := ApplyEvent(StateBusy, "", HookEvent{HookEventName: "Stop"})
	if r1.NewState != StateIdle {
		t.Fatalf("Stop should produce idle, got %s", r1.NewState)
	}

	r2 := ApplyEvent(StateIdle, "", HookEvent{HookEventName: "PostToolUse", ToolName: "Bash"})
	if !r2.Skip {
		t.Errorf("late PostToolUse after Stop should be skipped, got state=%s", r2.NewState)
	}
}

func TestCompactionPreservation(t *testing.T) {
	r := ApplyEvent("", "compacting", HookEvent{HookEventName: "SessionStart"})
	if r.NewState != StateIdle {
		t.Errorf("compacting SessionStart should produce idle, got %s", r.NewState)
	}
	if r.Summary != "Compacted" {
		t.Errorf("summary should be 'Compacted', got %q", r.Summary)
	}
}
