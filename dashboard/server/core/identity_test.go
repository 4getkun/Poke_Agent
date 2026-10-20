package core

import "testing"

// mockAgent implements the Agent interface for testing.
type mockAgent struct {
	sessionID         string
	pokegentSessionID string
	displayName       string
	tty               string
}

func (a *mockAgent) GetSessionID() string   { return a.sessionID }
func (a *mockAgent) GetPokegentID() string  { return a.pokegentSessionID }
func (a *mockAgent) GetDisplayName() string { return a.displayName }
func (a *mockAgent) GetTTY() string         { return a.tty }

func agents() []Agent {
	return []Agent{
		&mockAgent{"aaaa1111-full", "bbbb2222-pokegent", "Agent A", "/dev/ttys001"},
		&mockAgent{"cccc3333-full", "dddd4444-pokegent", "Agent B", "/dev/ttys002"},
		&mockAgent{"eeee5555-same", "eeee5555-same", "Agent C", "/dev/ttys003"},
	}
}

func TestResolveToSessionID(t *testing.T) {
	a := agents()
	tests := []struct {
		name, input, want string
	}{
		{"exact session_id", "aaaa1111-full", "aaaa1111-full"},
		{"prefix session_id", "aaaa1111", "aaaa1111-full"},
		{"exact pokegent_id", "bbbb2222-pokegent", "aaaa1111-full"},
		{"prefix pokegent_id", "bbbb2222", "aaaa1111-full"},
		{"agent B prefix", "cccc3333", "cccc3333-full"},
		{"agent B pokegent prefix", "dddd4444", "cccc3333-full"},
		{"same IDs agent", "eeee5555", "eeee5555-same"},
		{"no match", "zzzz9999", "zzzz9999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveToSessionID(a, tt.input)
			if got != tt.want {
				t.Errorf("ResolveToSessionID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveToPokegentID(t *testing.T) {
	a := agents()
	tests := []struct {
		name, input, want string
	}{
		{"session_id → pokegent", "aaaa1111", "bbbb2222-pokegent"},
		{"pokegent_id stays", "bbbb2222", "bbbb2222-pokegent"},
		{"same IDs", "eeee5555", "eeee5555-same"},
		{"no match", "zzzz9999", "zzzz9999"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveToPokegentID(a, tt.input)
			if got != tt.want {
				t.Errorf("ResolveToPokegentID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveAgent(t *testing.T) {
	a := agents()

	got := ResolveAgent(a, "aaaa1111")
	if got == nil || got.GetDisplayName() != "Agent A" {
		t.Error("failed to resolve Agent A by session_id prefix")
	}

	got = ResolveAgent(a, "dddd4444")
	if got == nil || got.GetDisplayName() != "Agent B" {
		t.Error("failed to resolve Agent B by pokegent_id prefix")
	}

	got = ResolveAgent(a, "zzzz9999")
	if got != nil {
		t.Error("expected nil for no match")
	}
}

func TestCloneSafety(t *testing.T) {
	// Two agents share session_id but have different pokegent_ids
	cloneAgents := []Agent{
		&mockAgent{"shared-id", "original-pokegent", "Original", ""},
		&mockAgent{"shared-id", "clone-pokegent", "Clone", ""},
	}

	// ResolveToPokegentID should find the right one
	got := ResolveToPokegentID(cloneAgents, "original-pokegent")
	if got != "original-pokegent" {
		t.Errorf("expected original-pokegent, got %s", got)
	}
	got = ResolveToPokegentID(cloneAgents, "clone-pokegent")
	if got != "clone-pokegent" {
		t.Errorf("expected clone-pokegent, got %s", got)
	}

	// ResolveAgent by pokegent_id finds correct clone
	a := ResolveAgent(cloneAgents, "clone-pokegent")
	if a == nil || a.GetDisplayName() != "Clone" {
		t.Error("ResolveAgent should find Clone by pokegent_id")
	}
}
