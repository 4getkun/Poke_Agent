package server

import "testing"

func TestBuildCardPreviewChatBusyUsesStreamingSummary(t *testing.T) {
	got := buildCardPreview(AgentState{
		State:       "busy",
		Interface:   "chat",
		LastSummary: "streaming assistant text",
	})
	if got.Phase != "streaming" || got.Text != "streaming assistant text" {
		t.Fatalf("preview = %+v, want streaming summary", got)
	}
}

func TestBuildCardPreviewBusyPrefersFeed(t *testing.T) {
	feed := []ActivityItem{{Time: "12:00:00", Type: "tool", Text: "Read: file.go"}}
	got := buildCardPreview(AgentState{
		State:        "busy",
		Interface:    "chat",
		LastSummary:  "streaming assistant text",
		ActivityFeed: feed,
	})
	if got.Phase != "tool" || len(got.Feed) != 1 || got.Text != "" {
		t.Fatalf("preview = %+v, want tool feed", got)
	}
}

func TestBuildCardPreviewBusyDetailSuppressesStaleSummary(t *testing.T) {
	got := buildCardPreview(AgentState{
		State:       "busy",
		Interface:   "chat",
		Detail:      "thinking…",
		LastSummary: "previous prompt response",
	})
	if got.Phase != "thinking" || got.Text != "thinking…" {
		t.Fatalf("preview = %+v, want active detail instead of stale summary", got)
	}
}

func TestBuildCardPreviewIdleUsesSummary(t *testing.T) {
	got := buildCardPreview(AgentState{State: "idle", LastSummary: "final answer"})
	if got.Phase != "complete" || got.Text != "final answer" {
		t.Fatalf("preview = %+v, want complete summary", got)
	}
}

func TestDisplayModelForBackendUsesBackendColonModel(t *testing.T) {
	cases := []struct{ model, backend, backendType, want string }{
		{"GPT 5.4 (Azure)", "codex", "codex-acp", "Codex: GPT 5.4 (Azure)"},
		{"haiku", "claude", "claude-acp", "Claude: Haiku"},
		{"claude-opus-4-6[1m]", "claude", "claude-acp", "Claude: Opus 1M"},
		{"Codex [unknown model]", "codex", "codex-acp", "Codex: unknown model"},
	}
	for _, c := range cases {
		if got := displayModelForBackend(c.model, c.backend, c.backendType); got != c.want {
			t.Fatalf("displayModelForBackend(%q, %q, %q) = %q, want %q", c.model, c.backend, c.backendType, got, c.want)
		}
	}
}

func TestBeginPromptClearsStaleActivityFeed(t *testing.T) {
	sm := NewStateManager(t.TempDir(), t.TempDir())
	const pgid = "poke-1"
	sm.statuses[pgid] = StatusFile{
		SessionID:     "session-1",
		State:         "idle",
		Detail:        "finished",
		LastSummary:   "previous response",
		LastTrace:     "previous trace",
		RecentActions: []string{"Read: stale.py"},
	}
	sm.activityFeeds[pgid] = []ActivityItem{{Time: "12:00:00", Type: "tool", Text: "Read: stale.py"}}
	sm.agents[pgid] = &AgentState{
		SessionID:     "session-1",
		PokegentID:    pgid,
		State:         "idle",
		LastSummary:   "previous response",
		RecentActions: []string{"Read: stale.py"},
		ActivityFeed:  []ActivityItem{{Time: "12:00:00", Type: "tool", Text: "Read: stale.py"}},
	}

	sm.BeginPrompt(pgid, "new prompt")

	got := sm.GetAgent(pgid)
	if got == nil {
		t.Fatal("agent missing")
	}
	if got.State != "busy" || got.UserPrompt != "new prompt" {
		t.Fatalf("agent state = %+v, want busy new prompt", got)
	}
	if len(got.ActivityFeed) != 0 || len(got.RecentActions) != 0 || got.LastSummary != "" || got.LastTrace != "" {
		t.Fatalf("stale turn fields were not cleared: %+v", got)
	}
	if got.CardPreview.Phase != "thinking" || got.CardPreview.Text != "Working..." {
		t.Fatalf("preview = %+v, want clean thinking preview", got.CardPreview)
	}
}
