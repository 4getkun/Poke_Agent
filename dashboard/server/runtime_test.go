package server

import (
	"testing"
)

// TestRuntimeRegistryFor covers the lookup behavior the unified HTTP
// handlers depend on: empty interface defaults to iterm2, unknown
// interface returns an error.
func TestRuntimeRegistryFor(t *testing.T) {
	// Use stubs to avoid needing a real StateManager / ChatManager.
	rt2 := NewITerm2Runtime(nil, nil)
	rtChat := NewChatRuntime(nil)
	r := runtimeRegistry{
		"iterm2": rt2,
		"chat":   rtChat,
	}

	cases := []struct {
		name    string
		input   string
		want    Runtime
		wantErr bool
	}{
		{"empty defaults to iterm2", "", rt2, false},
		{"explicit iterm2", "iterm2", rt2, false},
		{"explicit chat", "chat", rtChat, false},
		{"unknown errors", "wat", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.For(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("For(%q): err = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("For(%q): got %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// TestRuntimeCapabilitiesShape pins the capability values both
// runtimes advertise. Frontend menu gating reads these directly via
// /api/runtimes; a regression here silently breaks the AgentMenu.
func TestRuntimeCapabilitiesShape(t *testing.T) {
	t.Run("iterm2", func(t *testing.T) {
		c := NewITerm2Runtime(nil, nil).Capabilities()
		if !c.CanFocus || !c.CanClone || !c.CanCancel {
			t.Errorf("iterm2 should support focus, clone, cancel; got %+v", c)
		}
		if c.HasStreamingUI || c.HasPermissionUI {
			t.Errorf("iterm2 should not advertise streaming/permission UI; got %+v", c)
		}
	})
	t.Run("chat", func(t *testing.T) {
		c := NewChatRuntime(nil).Capabilities()
		// CanFocus is iterm2-only — chat panels open via a CustomEvent
		// dispatched by the dashboard, not a runtime call.
		if c.CanFocus {
			t.Errorf("chat shouldn't advertise focus; got %+v", c)
		}
		// CanClone IS supported for chat (clone via session/load — a new
		// agent loads the source's conversation). Was iterm2-only earlier
		// in the refactor; now both runtimes implement clone.
		if !c.CanClone || !c.CanCancel || !c.HasStreamingUI || !c.HasPermissionUI {
			t.Errorf("chat should support clone/cancel/streaming/permission UI; got %+v", c)
		}
	})
}
