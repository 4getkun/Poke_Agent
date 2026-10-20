package server

import "pokegents/dashboard/server/store"

// Type aliases — these types are defined in store/ and used throughout server/.
// Using aliases means existing code (state.go, server.go, etc.) compiles unchanged.
// In Phase 2, direct references to store.X will replace these.
type RunningSession = store.RunningSession
type StatusFile = store.StatusFile
type Profile = store.Profile
type Message = store.Message
type AgentConnection = store.Connection
type EphemeralAgent = store.EphemeralAgent
type AgentIdentity = store.AgentIdentity

// AgentState is the merged view sent to the frontend.
// This is server-only (not in store) because it combines data from multiple sources.
type AgentState struct {
	SessionID       string         `json:"session_id"`
	PokegentID      string         `json:"pokegent_id,omitempty"`
	ProfileName     string         `json:"profile_name"`
	Role            string         `json:"role,omitempty"`
	Project         string         `json:"project,omitempty"`
	RoleEmoji       string         `json:"role_emoji,omitempty"`
	ProjectColor    [3]int         `json:"project_color,omitempty"`
	TaskGroup       string         `json:"task_group,omitempty"`
	DisplayName     string         `json:"display_name"`
	Emoji           string         `json:"emoji"`
	Color           [3]int         `json:"color"`
	State           string         `json:"state"`
	Detail          string         `json:"detail"`
	CWD             string         `json:"cwd"`
	LastSummary     string         `json:"last_summary"`
	LastTrace       string         `json:"last_trace"`
	UserPrompt      string         `json:"user_prompt"`
	RecentActions   []string       `json:"recent_actions,omitempty"`
	ActivityFeed    []ActivityItem `json:"activity_feed,omitempty"`
	CardPreview     CardPreview    `json:"card_preview"`
	BackgroundTasks int            `json:"background_tasks,omitempty"`
	ContextTokens   int            `json:"context_tokens"`
	ContextWindow   int            `json:"context_window"`
	LastUpdated     string         `json:"last_updated"`
	BusySince       string         `json:"busy_since,omitempty"`
	PID             int            `json:"pid"`
	TTY             string         `json:"tty"`
	ITermSessionID  string         `json:"iterm_session_id,omitempty"`
	IsAlive         bool           `json:"is_alive"`
	DurationSec     int            `json:"duration_sec"`
	CreatedAt       string         `json:"created_at,omitempty"`
	Model           string         `json:"model,omitempty"`
	Effort          string         `json:"effort,omitempty"`
	Sprite          string         `json:"sprite,omitempty"`
	Ephemeral       bool           `json:"ephemeral,omitempty"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	SubagentType    string         `json:"subagent_type,omitempty"`
	// Interface routes click-handling on the frontend: "iterm2" focuses a
	// terminal tab, "chat" opens an in-dashboard ACP panel. Empty defaults
	// to iterm2 for legacy agents.
	Interface    string `json:"interface,omitempty"`
	AgentBackend string `json:"agent_backend,omitempty"`
}

// ActivityItem is a single entry in the agent's activity feed.
type ActivityItem struct {
	Time string `json:"time"` // HH:MM:SS
	Type string `json:"type"` // "tool", "text", "thinking"
	Text string `json:"text"`
}

// CardPreview is the normalized, backend-agnostic payload the agent card renders.
// It is derived server-side from hooks/ACP/transcript state so the frontend does
// not need to guess between last_trace, last_summary, detail, and activity_feed.
type CardPreview struct {
	State     string         `json:"state"`
	Phase     string         `json:"phase"` // thinking | tool | streaming | waiting | complete | error | empty
	Prompt    string         `json:"prompt,omitempty"`
	Text      string         `json:"text,omitempty"`
	Feed      []ActivityItem `json:"feed,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
}

// HookEvent is the JSON posted by Claude Code hooks.
type HookEvent struct {
	SessionID            string `json:"session_id"`
	HookEventName        string `json:"hook_event_name"`
	ToolName             string `json:"tool_name,omitempty"`
	ToolInput            any    `json:"tool_input,omitempty"`
	CWD                  string `json:"cwd"`
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
	NotificationType     string `json:"notification_type,omitempty"`
	TranscriptPath       string `json:"transcript_path,omitempty"`
	Prompt               string `json:"prompt,omitempty"`
}
