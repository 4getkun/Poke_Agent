package core

import (
	"time"
)

// ApplyEvent is the canonical state machine for pokegent hook events.
// It is a pure function — no file I/O, no side effects. The caller applies
// the result to the status store.
//
// This MUST match the bash hook's state machine in hooks/status-update.sh.
// Cross-validated by tests/test_hook_integration.sh.
func ApplyEvent(current SessionState, currentDetail string, evt HookEvent) StateTransitionResult {
	now := time.Now().UTC().Format(time.RFC3339)

	// Guard: slow PreToolUse/PostToolUse arriving after Stop must NOT
	// overwrite done/error/idle. Only UserPromptSubmit can transition out.
	switch evt.HookEventName {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
		if !current.CanInterrupt() {
			return StateTransitionResult{Skip: true}
		}
	}

	switch evt.HookEventName {
	case "UserPromptSubmit":
		return StateTransitionResult{
			NewState:     StateBusy,
			Detail:       "processing prompt",
			BusySince:    now,
			ClearActions: true,
		}

	case "PreToolUse":
		toolInput := extractToolInput(evt)
		action := evt.ToolName + ": " + toolInput
		return StateTransitionResult{
			NewState:   StateBusy,
			Detail:     action,
			ToolAction: action,
		}

	case "PostToolUse":
		return StateTransitionResult{
			NewState: StateBusy,
			Detail:   "completed " + evt.ToolName,
		}

	case "PostToolUseFailure":
		action := evt.ToolName + " failed"
		return StateTransitionResult{
			NewState:   StateBusy,
			Detail:     action,
			ToolAction: action,
		}

	case "Stop":
		return StateTransitionResult{
			NewState:     StateIdle,
			Detail:       "finished",
			Summary:      truncate(evt.LastAssistantMessage, 200),
			ClearActions: true,
		}

	case "StopFailure":
		return StateTransitionResult{
			NewState:     StateError,
			Detail:       "API error — reprompt to retry",
			ClearActions: true,
		}

	case "PermissionRequest":
		return StateTransitionResult{
			NewState: StateNeedsInput,
			Detail:   "needs permission for " + evt.ToolName,
		}

	case "Notification":
		if evt.NotificationType == "idle_prompt" {
			// idle_prompt only transitions busy → idle
			// Never sets needs_input (that's exclusively PermissionRequest)
			if current == StateBusy {
				return StateTransitionResult{
					NewState: StateIdle,
					Detail:   "finished",
				}
			}
			// All other states: no change
			return StateTransitionResult{Skip: true}
		}
		return StateTransitionResult{Skip: true}

	case "SessionStart":
		// Don't overwrite busy agents — this happens when a clone does
		// --resume <original-id> and SessionStart fires for the original
		if current == StateBusy {
			return StateTransitionResult{Skip: true}
		}
		// Preserve compaction state
		if currentDetail == "compacting" {
			return StateTransitionResult{
				NewState: StateIdle,
				Detail:   "finished",
				Summary:  "Compacted",
			}
		}
		return StateTransitionResult{
			NewState: StateIdle,
			Detail:   "session started",
		}

	case "SessionEnd":
		// Signal to caller to remove the session entirely
		return StateTransitionResult{
			NewState: "", // empty = remove
			Detail:   "session ended",
		}

	default:
		return StateTransitionResult{Skip: true}
	}
}

// extractToolInput pulls the most relevant field from the tool input.
func extractToolInput(evt HookEvent) string {
	m, ok := evt.ToolInput.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "file_path", "pattern", "query", "description"} {
		if v, ok := m[key]; ok {
			return truncate(toString(v), 80)
		}
	}
	return ""
}

func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		return ""
	}
}
