package core

import "strings"

// Agent is the minimal info needed for identity resolution.
// Implemented by store.RunningSession and server.AgentState.
type Agent interface {
	GetSessionID() string
	GetPokegentID() string
	GetDisplayName() string
	GetTTY() string
}

// ResolveToSessionID finds the Claude session ID for a given ID (which could
// be a session_id, pokegent_id, or 8-char prefix of either).
// Returns the input unchanged if no match is found.
func ResolveToSessionID(agents []Agent, id string) string {
	// Pass 1: exact or prefix match on session_id
	for _, a := range agents {
		sid := a.GetSessionID()
		if sid == id || strings.HasPrefix(sid, id) {
			return sid
		}
	}
	// Pass 2: exact or prefix match on pokegent_id (skip if same as session_id)
	for _, a := range agents {
		pgid := a.GetPokegentID()
		if pgid != "" && pgid != a.GetSessionID() {
			if pgid == id || strings.HasPrefix(pgid, id) {
				return a.GetSessionID()
			}
		}
	}
	return id
}

// ResolveToPokegentID finds the stable pokegent ID for a given ID.
func ResolveToPokegentID(agents []Agent, id string) string {
	// Pass 1: match on pokegent_id
	for _, a := range agents {
		pgid := a.GetPokegentID()
		if pgid != "" && (pgid == id || strings.HasPrefix(pgid, id)) {
			return pgid
		}
	}
	// Pass 2: match on session_id → return its pokegent_id
	for _, a := range agents {
		sid := a.GetSessionID()
		if sid == id || strings.HasPrefix(sid, id) {
			if pgid := a.GetPokegentID(); pgid != "" {
				return pgid
			}
			return sid
		}
	}
	return id
}

// ResolveAgent finds the agent matching the given ID (any type, any prefix length).
func ResolveAgent(agents []Agent, id string) Agent {
	// Pass 1: session_id
	for _, a := range agents {
		sid := a.GetSessionID()
		if sid == id || strings.HasPrefix(sid, id) {
			return a
		}
	}
	// Pass 2: pokegent_id
	for _, a := range agents {
		pgid := a.GetPokegentID()
		if pgid != "" && (pgid == id || strings.HasPrefix(pgid, id)) {
			return a
		}
	}
	return nil
}
