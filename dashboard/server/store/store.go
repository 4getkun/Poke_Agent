// Package store provides file-backed storage for pokegents session data.
// All file I/O is centralized here — consumers use interfaces, never os.ReadFile directly.
package store

import (
	"pokegents/dashboard/server/core"
	"time"
)

// Type aliases — canonical definitions live in core/types.go.
// Store uses these so consumers don't need to import both packages.
type RunningSession = core.RunningSession
type StatusFile = core.StatusFile
type Profile = core.Profile
type AppConfig = core.AppConfig
type Message = core.Message
type Connection = core.Connection
type ActivityEntry = core.ActivityEntry
type FileEvent = core.FileEvent
type ProjectConfig = core.ProjectConfig
type RoleConfig = core.RoleConfig
type EphemeralAgent = core.EphemeralAgent
type AgentIdentity = core.AgentIdentity

// Store aggregates all sub-stores. Pass this to SessionManager, MessageService, etc.
type Store struct {
	Running   RunningStore
	Status    StatusStore
	Profiles  ProfileStore
	Projects  ProjectStore
	Roles     RoleStore
	Config    ConfigStore
	Messages  MessageStore
	Activity  ActivityStore
	Metadata  MetadataStore
	Ephemeral EphemeralStore
	Agents    AgentIdentityStore
}

// ProjectStore manages project configuration files (~/.pokegents/projects/*.json).
type ProjectStore interface {
	// Get returns a project by name.
	Get(name string) (*ProjectConfig, error)
	// List returns all projects.
	List() ([]ProjectConfig, error)
}

// RoleStore manages role configuration files (~/.pokegents/roles/*.json).
type RoleStore interface {
	// Get returns a role by name.
	Get(name string) (*RoleConfig, error)
	// List returns all roles.
	List() ([]RoleConfig, error)
}

// EphemeralStore manages ephemeral subagent files (~/.pokegents/ephemeral/*.json).
type EphemeralStore interface {
	// Get returns an ephemeral agent by agent_id.
	Get(agentID string) (*EphemeralAgent, error)
	// List returns all ephemeral agents.
	List() ([]EphemeralAgent, error)
	// Create writes a new ephemeral agent file.
	Create(ea EphemeralAgent) error
	// Complete marks an ephemeral agent as completed.
	Complete(agentID string, lastMessage string, transcriptPath string) error
	// Delete removes an ephemeral agent file.
	Delete(agentID string) error
	// Cleanup removes completed ephemeral agents older than maxAge.
	Cleanup(maxAge time.Duration) (int, error)
}

// AgentIdentityStore manages persistent agent identity files (~/.pokegents/agents/*.json).
type AgentIdentityStore interface {
	// Get returns an agent identity by pokegent_id.
	Get(pokegentID string) (*AgentIdentity, error)
	// List returns all agent identities.
	List() ([]AgentIdentity, error)
	// Save writes an agent identity file.
	Save(identity AgentIdentity) error
	// Update atomically reads, modifies, and writes an agent identity.
	Update(pokegentID string, fn func(*AgentIdentity)) error
}

// MetadataStore manages small JSON metadata files (name overrides, session ID map, agent order).
type MetadataStore interface {
	// LoadJSON reads a JSON file into dest. Returns nil error if file doesn't exist.
	LoadJSON(filename string, dest any) error
	// SaveJSON writes data as JSON to a file.
	SaveJSON(filename string, data any) error
}

// RunningStore manages active session registry files (~/.pokegents/running/*.json).
type RunningStore interface {
	// Get returns a single running session by Claude session ID.
	Get(sessionID string) (*RunningSession, error)
	// GetByPokegentID returns a running session by stable pokegent ID.
	GetByPokegentID(pokegentID string) (*RunningSession, error)
	// List returns all running sessions.
	List() ([]RunningSession, error)
	// Create writes a new running session file.
	Create(rs RunningSession) error
	// Update atomically reads, modifies, and writes a running session.
	Update(sessionID string, fn func(*RunningSession)) error
	// Delete removes a running session file.
	Delete(sessionID string) error
	// Watch returns a channel of file change events.
	Watch() <-chan FileEvent
}

// StatusStore manages agent status files (~/.pokegents/status/*.json).
type StatusStore interface {
	// Get returns a single status file by session ID.
	Get(sessionID string) (*StatusFile, error)
	// Upsert creates or updates a status file.
	Upsert(sf StatusFile) error
	// Delete removes a status file.
	Delete(sessionID string) error
	// List returns all status files.
	List() ([]StatusFile, error)
	// Watch returns a channel of file change events.
	Watch() <-chan FileEvent
}

// ProfileStore manages profile configuration files (~/.pokegents/profiles/*.json).
type ProfileStore interface {
	// Get returns a profile by name.
	Get(name string) (*Profile, error)
	// List returns all profiles.
	List() ([]Profile, error)
}

// ConfigStore manages the global config file (~/.pokegents/config.json).
type ConfigStore interface {
	// Get returns the current configuration.
	Get() (*AppConfig, error)
}

// MessageStore manages inter-agent message files (~/.pokegents/messages/).
type MessageStore interface {
	// Send stores a new message in the recipient's mailbox.
	Send(from, fromName, to, toName, content string) (*Message, error)
	// GetUndelivered returns messages with delivered=false for a session.
	GetUndelivered(sessionID string) ([]Message, error)
	// MarkDelivered marks specific messages as delivered (keeps files on disk).
	MarkDelivered(msgIDs []string) error
	// Consume returns all messages and deletes their files (agent acknowledged receipt).
	Consume(sessionID string) ([]Message, error)
	// GetBudget returns the current message count for a session.
	GetBudget(sessionID string) (int, error)
	// ResetBudget resets the message count to 0.
	ResetBudget(sessionID string) error
	// GetHistory returns recent message history (for UI display).
	GetHistory() ([]Message, error)
	// AppendHistory adds a message to the history log.
	AppendHistory(msg Message) error
	// GetConnections returns unique agent pairs that have communicated.
	GetConnections() ([]Connection, error)
}

// ActivityStore manages the shared activity log (~/.pokegents/activity/).
type ActivityStore interface {
	// Append adds an entry to a project's activity log.
	Append(projectHash string, entry ActivityEntry) error
	// GetSince returns entries after a given line number for a project.
	GetSince(projectHash string, afterLine int) ([]ActivityEntry, int, error)
	// GetLastReadLine returns the last-read line number for a session.
	GetLastReadLine(projectHash, sessionID string) (int, error)
	// SetLastReadLine updates the last-read line number.
	SetLastReadLine(projectHash, sessionID string, line int) error
}

// FileEvent is aliased from core/types.go above.

// Data types (RunningSession, StatusFile, etc.) are defined in core/types.go
// and aliased above. The aliases ensure store consumers can use store.RunningSession
// without importing core/ directly.
