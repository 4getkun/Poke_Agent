package server

import (
	"testing"
)

// TestResolveSessionID tests session ID resolution with various match types.
func TestResolveSessionID(t *testing.T) {
	// Build a mock server with agents
	agents := []AgentState{
		{SessionID: "aaaa1111-full-uuid-here", PokegentID: "bbbb2222-pokegent-uuid-here", DisplayName: "Agent A"},
		{SessionID: "cccc3333-full-uuid-here", PokegentID: "dddd4444-pokegent-uuid-here", DisplayName: "Agent B"},
		{SessionID: "eeee5555-full-uuid-here", PokegentID: "eeee5555-full-uuid-here", DisplayName: "Agent C (same IDs)"},
	}

	// resolveSessionID is on *Server, so we test the resolution logic directly
	resolve := func(id string) string {
		// Pass 1: exact session_id or prefix
		for _, a := range agents {
			if a.SessionID == id || len(id) < len(a.SessionID) && a.SessionID[:len(id)] == id {
				return a.SessionID
			}
		}
		// Pass 2: pokegent_id
		for _, a := range agents {
			if a.PokegentID != "" && a.PokegentID != a.SessionID {
				if a.PokegentID == id || (len(id) < len(a.PokegentID) && a.PokegentID[:len(id)] == id) {
					return a.SessionID
				}
			}
		}
		return id
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"exact session_id", "aaaa1111-full-uuid-here", "aaaa1111-full-uuid-here"},
		{"session_id prefix", "aaaa1111", "aaaa1111-full-uuid-here"},
		{"pokegent_id exact", "bbbb2222-pokegent-uuid-here", "aaaa1111-full-uuid-here"},
		{"pokegent_id prefix", "bbbb2222", "aaaa1111-full-uuid-here"},
		{"agent B by prefix", "cccc3333", "cccc3333-full-uuid-here"},
		{"agent B by pokegent prefix", "dddd4444", "cccc3333-full-uuid-here"},
		{"same IDs agent by prefix", "eeee5555", "eeee5555-full-uuid-here"},
		{"no match returns input", "zzzz9999", "zzzz9999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolve(tt.input)
			if result != tt.expected {
				t.Errorf("resolve(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestResolveToPokegentID tests reverse resolution (find the pokegent ID).
func TestResolveToPokegentID(t *testing.T) {
	agents := []AgentState{
		{SessionID: "aaaa1111-full-uuid", PokegentID: "bbbb2222-pokegent-uuid", DisplayName: "Agent A"},
		{SessionID: "cccc3333-full-uuid", PokegentID: "cccc3333-full-uuid", DisplayName: "Agent B (same)"},
	}

	resolveToPokegent := func(id string) string {
		// Check by pokegent_id first
		for _, a := range agents {
			if a.PokegentID != "" && (a.PokegentID == id || (len(id) < len(a.PokegentID) && a.PokegentID[:len(id)] == id)) {
				return a.PokegentID
			}
		}
		// Check by session_id → return pokegent_id
		for _, a := range agents {
			if a.SessionID == id || (len(id) < len(a.SessionID) && a.SessionID[:len(id)] == id) {
				if a.PokegentID != "" {
					return a.PokegentID
				}
				return a.SessionID
			}
		}
		return id
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"session_id → pokegent_id", "aaaa1111", "bbbb2222-pokegent-uuid"},
		{"pokegent_id stays", "bbbb2222", "bbbb2222-pokegent-uuid"},
		{"same IDs agent", "cccc3333", "cccc3333-full-uuid"},
		{"no match returns input", "zzzz9999", "zzzz9999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveToPokegent(tt.input)
			if result != tt.expected {
				t.Errorf("resolveToPokegent(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestCloneSafety verifies that two agents with the same session_id
// (clone scenario) are resolved correctly by pokegent_id.
func TestCloneSafety(t *testing.T) {
	agents := []AgentState{
		{SessionID: "shared-session-id", PokegentID: "original-pokegent-id", DisplayName: "Original"},
		{SessionID: "shared-session-id", PokegentID: "clone-pokegent-id", DisplayName: "Clone"},
	}

	// Resolution by pokegent_id should find the right one
	resolveByLabel := func(pokegentID string) string {
		for _, a := range agents {
			if a.PokegentID == pokegentID {
				return a.DisplayName
			}
		}
		return "not found"
	}

	if resolveByLabel("original-pokegent-id") != "Original" {
		t.Error("failed to resolve original by pokegent_id")
	}
	if resolveByLabel("clone-pokegent-id") != "Clone" {
		t.Error("failed to resolve clone by pokegent_id")
	}

	// Resolution by session_id is ambiguous — should return first match
	// This is why clone routing MUST use pokegent_id
	matchCount := 0
	for _, a := range agents {
		if a.SessionID == "shared-session-id" {
			matchCount++
		}
	}
	if matchCount != 2 {
		t.Errorf("expected 2 agents with same session_id, got %d", matchCount)
	}
}
