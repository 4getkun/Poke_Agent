package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	storelib "pokegents/dashboard/server/store"
)

// StateManager holds the in-memory merged view of all agent state.
type StateManager struct {
	mu                sync.RWMutex
	store             *storelib.Store            // Layer 0 file-backed store
	profiles          map[string]Profile         // keyed by profile name
	running           map[string]RunningSession  // keyed by pokegent_id
	statuses          map[string]StatusFile      // keyed by pokegent_id
	agents            map[string]*AgentState     // keyed by pokegent_id
	ephemeral         map[string]*EphemeralAgent // keyed by agent_id
	contexts          map[string]ContextUsage    // keyed by pokegent_id, survives rebuilds
	activityFeeds     map[string][]ActivityItem  // keyed by pokegent_id, survives rebuilds
	feedClearedAt     map[string]time.Time       // when UserPromptSubmit last cleared the feed
	nameOverrides     map[string]string          // keyed by pokegent_id, persisted to disk
	sessionToPokegent map[string]string          // Claude session_id → pokegent_id (reverse lookup for hooks)
	agentOrder        []string                   // user-defined display order (pokegent_ids), persisted
	identities        map[string]*AgentIdentity  // keyed by pokegent_id, persistent identity store
	pendingRelaunches map[string]string          // pokegent_id → pokegent command to run when idle
	transcriptPaths   map[string]string          // pokegent_id → transcript JSONL path (cache)
	backendStore      *storelib.BackendStore     // backend config for model name resolution

	dataDir          string // ~/.ccsession — kept for paths not yet migrated to store
	claudeProjectDir string // ~/.claude/projects
}

func NewStateManager(dataDir, claudeProjectDir string) *StateManager {
	// Legacy constructor — creates its own store
	return NewStateManagerWithStore(storelib.NewFileStore(dataDir), dataDir, claudeProjectDir)
}

func NewStateManagerWithStore(s *storelib.Store, dataDir, claudeProjectDir string) *StateManager {
	return &StateManager{
		store:             s,
		profiles:          make(map[string]Profile),
		running:           make(map[string]RunningSession),
		statuses:          make(map[string]StatusFile),
		agents:            make(map[string]*AgentState),
		ephemeral:         make(map[string]*EphemeralAgent),
		contexts:          make(map[string]ContextUsage),
		activityFeeds:     make(map[string][]ActivityItem),
		feedClearedAt:     make(map[string]time.Time),
		identities:        make(map[string]*AgentIdentity),
		nameOverrides:     make(map[string]string),
		sessionToPokegent: make(map[string]string),
		transcriptPaths:   make(map[string]string),
		dataDir:           dataDir,
		claudeProjectDir:  claudeProjectDir,
	}
}

// SetBackendStore sets the backend config store for model name resolution.
func (sm *StateManager) SetBackendStore(bs *storelib.BackendStore) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.backendStore = bs
}

// LoadAll reads all profiles, running sessions, and status files from disk.
func (sm *StateManager) LoadAll() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := sm.loadProfiles(); err != nil {
		return err
	}
	if err := sm.loadIdentities(); err != nil {
		return err
	}
	if err := sm.loadRunning(); err != nil {
		return err
	}
	if err := sm.loadStatuses(); err != nil {
		return err
	}
	if err := sm.loadEphemeral(); err != nil {
		return err
	}
	// Cleanup completed ephemeral agents older than 1 hour on startup
	if sm.store.Ephemeral != nil {
		if removed, err := sm.store.Ephemeral.Cleanup(time.Hour); err == nil && removed > 0 {
			log.Printf("Cleaned up %d stale ephemeral agent(s)", removed)
			sm.loadEphemeral() // reload after cleanup
		}
	}
	sm.loadNameOverrides()
	sm.loadAgentOrder()
	sm.rebuildAgents()

	// Load initial context/messages/activity from transcripts (single pass per agent).
	for pgid := range sm.agents {
		path := sm.findTranscriptPathLocked(pgid)
		if path != "" {
			sm.transcriptPaths[pgid] = path
			batch := storelib.BatchExtract(path)
			if batch.ContextUsage.Tokens > 0 {
				sm.contexts[pgid] = ContextUsage{Tokens: batch.ContextUsage.Tokens, Window: batch.ContextUsage.Window}
				if a, ok := sm.agents[pgid]; ok {
					a.ContextTokens = batch.ContextUsage.Tokens
					a.ContextWindow = batch.ContextUsage.Window
				}
			}
			if a, ok := sm.agents[pgid]; ok {
				if a.UserPrompt == "" && batch.LastUserPrompt != "" {
					a.UserPrompt = batch.LastUserPrompt
				}
				if a.State != "busy" && a.LastSummary == "" && batch.LastSummary != "" {
					a.LastSummary = batch.LastSummary
				}
			}
			if _, hasFeed := sm.activityFeeds[pgid]; !hasFeed {
				if len(batch.ActivityFeed) > 0 {
					feed := make([]ActivityItem, len(batch.ActivityFeed))
					for i, item := range batch.ActivityFeed {
						feed[i] = ActivityItem{Time: item.Time, Type: item.Type, Text: item.Text}
					}
					sm.activityFeeds[pgid] = feed
					if a, ok := sm.agents[pgid]; ok {
						a.ActivityFeed = feed
					}
				}
			}
		}
	}

	return nil
}

// GetAgents returns only agents that have an active running session registration,
// plus any ephemeral subagents tracked via the Agent tool.
func (sm *StateManager) GetAgents() []AgentState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]AgentState, 0, len(sm.agents)+len(sm.ephemeral))
	for pgid, a := range sm.agents {
		// Only show agents with a running session file
		if _, hasRunning := sm.running[pgid]; !hasRunning {
			continue
		}
		cp := *a
		cp.CardPreview = buildCardPreview(cp)
		result = append(result, cp)
	}

	// Append ephemeral agents (subagents from Agent tool)
	for _, ea := range sm.ephemeral {
		state := "busy"
		if ea.State == "completed" {
			state = "idle"
		}
		a := AgentState{
			SessionID:       ea.AgentID,
			ProfileName:     ea.AgentType,
			DisplayName:     ea.Description,
			State:           state,
			Detail:          ea.AgentType + " subagent",
			CWD:             ea.CWD,
			LastSummary:     ea.LastMessage,
			CreatedAt:       ea.CreatedAt,
			Ephemeral:       true,
			ParentSessionID: ea.ParentSessionID,
			SubagentType:    ea.AgentType,
			IsAlive:         ea.State == "running",
		}
		if ea.DurationSec > 0 {
			a.DurationSec = ea.DurationSec
		}
		a.CardPreview = buildCardPreview(a)
		// Inherit parent's task group, or auto-create one if parent has none
		if ea.ParentSessionID != "" {
			for pgid, parent := range sm.agents {
				if parent.SessionID == ea.ParentSessionID || parent.PokegentID == ea.ParentSessionID {
					a.Project = parent.Project
					a.ProjectColor = parent.ProjectColor
					if parent.TaskGroup != "" {
						a.TaskGroup = parent.TaskGroup
					} else {
						// Auto-group: create an implicit group so parent and ephemeral are visually linked
						groupName := parent.DisplayName + " + subagents"
						a.TaskGroup = groupName
						// Also tag the parent in the result slice so it ends up in the same group
						for i := range result {
							if result[i].PokegentID == pgid {
								result[i].TaskGroup = groupName
								break
							}
						}
					}
					break
				}
			}
		}
		result = append(result, a)
	}

	// Sort by user-defined order. Agents in agentOrder come first (in that order),
	// unordered agents go at the end sorted by creation time.
	orderIndex := make(map[string]int, len(sm.agentOrder))
	for i, pgid := range sm.agentOrder {
		orderIndex[pgid] = i + 1 // 1-based so 0 means "not in list"
	}
	sort.SliceStable(result, func(i, j int) bool {
		oi, oj := orderIndex[result[i].PokegentID], orderIndex[result[j].PokegentID]
		if oi != 0 && oj != 0 {
			return oi < oj
		}
		if oi != 0 {
			return true // ordered agents before unordered
		}
		if oj != 0 {
			return false
		}
		// Both unordered: sort by task_group first (ungrouped last), then creation time
		gi, gj := result[i].TaskGroup, result[j].TaskGroup
		if gi != gj {
			if gi == "" {
				return false // ungrouped after grouped
			}
			if gj == "" {
				return true // grouped before ungrouped
			}
			return gi < gj
		}
		return result[i].CreatedAt < result[j].CreatedAt
	})

	return result
}

var hiddenCardPreviewDetails = map[string]bool{
	"":                  true,
	"finished":          true,
	"session started":   true,
	"processing prompt": true,
}

func buildCardPreview(a AgentState) CardPreview {
	state := a.State
	if state == "" {
		state = "idle"
	}
	preview := CardPreview{
		State:     state,
		Prompt:    a.UserPrompt,
		UpdatedAt: a.LastUpdated,
	}

	trimmedDetail := strings.TrimSpace(a.Detail)
	safeDetail := ""
	if !hiddenCardPreviewDetails[trimmedDetail] {
		safeDetail = trimmedDetail
	}

	switch state {
	case "busy", "reconfiguring":
		if trimmedDetail == "compacting" {
			preview.Phase = "thinking"
			preview.Text = "Compacting conversation history..."
			return preview
		}
		feed := a.ActivityFeed
		if len(feed) == 0 && len(a.RecentActions) > 0 {
			feed = feedFromRecentActions(a.RecentActions, a.LastUpdated)
		}
		if len(feed) > 0 {
			preview.Feed = feed
			last := feed[len(feed)-1]
			if last.Type == "tool" {
				preview.Phase = "tool"
			} else {
				preview.Phase = "streaming"
			}
			return preview
		}
		if strings.TrimSpace(a.LastTrace) != "" {
			preview.Phase = "streaming"
			preview.Text = a.LastTrace
			return preview
		}
		if safeDetail != "" {
			preview.Phase = "thinking"
			preview.Text = safeDetail
			return preview
		}
		// Chat backends keep live assistant deltas in last_summary while busy.
		// This intentionally comes after active detail/trace so a new turn with
		// stale committed summary does not show the previous prompt's response.
		if a.Interface == "chat" && strings.TrimSpace(a.LastSummary) != "" {
			preview.Phase = "streaming"
			preview.Text = a.LastSummary
			return preview
		}
		if strings.TrimSpace(a.LastSummary) != "" {
			preview.Phase = "streaming"
			preview.Text = a.LastSummary
			return preview
		}
		preview.Phase = "thinking"
		preview.Text = "Working..."
		return preview
	case "needs_input", "waiting":
		preview.Phase = "waiting"
		preview.Text = firstNonEmpty(safeDetail, a.LastSummary, a.LastTrace, "Needs input")
		return preview
	case "error":
		preview.Phase = "error"
		preview.Text = firstNonEmpty(safeDetail, a.LastSummary, a.LastTrace, "API error - reprompt to retry")
		return preview
	}

	if strings.TrimSpace(a.LastSummary) != "" {
		preview.Phase = "complete"
		preview.Text = a.LastSummary
		return preview
	}
	if strings.TrimSpace(a.LastTrace) != "" {
		preview.Phase = "complete"
		preview.Text = a.LastTrace
		return preview
	}
	if safeDetail != "" {
		preview.Phase = "complete"
		preview.Text = safeDetail
		return preview
	}
	preview.Phase = "empty"
	preview.Text = "No output yet"
	return preview
}

func feedFromRecentActions(actions []string, updatedAt string) []ActivityItem {
	if len(actions) == 0 {
		return nil
	}
	timeLabel := ""
	if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
		timeLabel = t.Local().Format("15:04:05")
	}
	feed := make([]ActivityItem, 0, len(actions))
	for _, action := range actions {
		text := strings.TrimSpace(action)
		if text == "" {
			continue
		}
		feed = append(feed, ActivityItem{Time: timeLabel, Type: "tool", Text: text})
	}
	return feed
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// GetAgent returns a single agent by pokegent_id or session ID.
func (sm *StateManager) GetAgent(id string) *AgentState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	// Direct lookup by pokegent_id (primary key)
	if a, ok := sm.agents[id]; ok {
		cp := *a
		cp.CardPreview = buildCardPreview(cp)
		return &cp
	}
	// Fallback: scan for matching SessionID
	for _, a := range sm.agents {
		if a.SessionID != "" && a.SessionID == id {
			cp := *a
			cp.CardPreview = buildCardPreview(cp)
			return &cp
		}
	}
	return nil
}

// RenameAgent updates the display name in the running file, name overrides, and in-memory state.
// pokegentID is the primary map key.
func (sm *StateManager) RenameAgent(pokegentID, newName string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Update in-memory
	if a, ok := sm.agents[pokegentID]; ok {
		a.DisplayName = newName
	}

	// Update running file on disk via store
	if rs, ok := sm.running[pokegentID]; ok {
		rs.DisplayName = newName
		sm.running[pokegentID] = rs
		sm.store.Running.Update(pokegentID, func(r *RunningSession) {
			r.DisplayName = newName
		})
	}

	// JSONL custom-title is written by server.go persistCustomTitle() which
	// also updates the search index. Don't duplicate the write here.

	// Store name override and persist to identity store
	sm.nameOverrides[pokegentID] = newName
	if rs, ok := sm.running[pokegentID]; ok {
		if pgid := rs.GetPokegentID(); pgid != "" && sm.store.Agents != nil {
			sm.store.Agents.Update(pgid, func(id *AgentIdentity) {
				id.DisplayName = newName
			})
			if ident, ok := sm.identities[pgid]; ok {
				ident.DisplayName = newName
			}
		}
	}
	sm.saveNameOverrides()
}

// SetAgentRole updates the role field on a running agent and recalculates the profile composite key.
func (sm *StateManager) SetAgentRole(pokegentID, role string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if rs, ok := sm.running[pokegentID]; ok {
		rs.Role = role
		rs.Profile = sm.composeProfileKey(rs.Role, rs.Project, rs.Profile)
		sm.running[pokegentID] = rs
		sm.store.Running.Update(pokegentID, func(r *RunningSession) {
			r.Role = role
			r.Profile = rs.Profile
		})
		// Also persist to identity store
		if pgid := rs.GetPokegentID(); pgid != "" && sm.store.Agents != nil {
			sm.store.Agents.Update(pgid, func(id *AgentIdentity) {
				id.Role = role
			})
			if ident, ok := sm.identities[pgid]; ok {
				ident.Role = role
			}
		}
	}
	sm.rebuildAgents()
}

// SetAgentProject updates the project field on a running agent and recalculates the profile composite key.
func (sm *StateManager) SetAgentProject(pokegentID, project string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if rs, ok := sm.running[pokegentID]; ok {
		rs.Project = project
		rs.Profile = sm.composeProfileKey(rs.Role, rs.Project, rs.Profile)
		sm.running[pokegentID] = rs
		sm.store.Running.Update(pokegentID, func(r *RunningSession) {
			r.Project = project
			r.Profile = rs.Profile
		})
		// Also persist to identity store
		if pgid := rs.GetPokegentID(); pgid != "" && sm.store.Agents != nil {
			sm.store.Agents.Update(pgid, func(id *AgentIdentity) {
				id.Project = project
			})
			if ident, ok := sm.identities[pgid]; ok {
				ident.Project = project
			}
		}
	}
	sm.rebuildAgents()
}

// SetAgentTaskGroup updates the task_group field on a running agent.
// Task group is organizational metadata — no relaunch needed.
func (sm *StateManager) SetAgentTaskGroup(pokegentID, taskGroup string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	rs, ok := sm.running[pokegentID]
	if !ok {
		return fmt.Errorf("agent not found: %s", pokegentID)
	}
	rs.TaskGroup = taskGroup
	sm.running[pokegentID] = rs
	sm.store.Running.Update(pokegentID, func(r *RunningSession) {
		r.TaskGroup = taskGroup
	})
	// Also persist to identity store
	if pgid := rs.GetPokegentID(); pgid != "" && sm.store.Agents != nil {
		sm.store.Agents.Update(pgid, func(id *AgentIdentity) {
			id.TaskGroup = taskGroup
		})
		if ident, ok := sm.identities[pgid]; ok {
			ident.TaskGroup = taskGroup
		}
	}
	sm.rebuildAgents()
	return nil
}

// SetAgentSprite updates the sprite field on a running agent (single source of truth).
func (sm *StateManager) SetAgentSprite(pokegentID, sprite string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	rs, ok := sm.running[pokegentID]
	if !ok {
		return fmt.Errorf("agent not found: %s", pokegentID)
	}
	rs.Sprite = sprite
	sm.running[pokegentID] = rs
	sm.store.Running.Update(pokegentID, func(r *RunningSession) {
		r.Sprite = sprite
	})
	// Also persist to identity store
	if pgid := rs.GetPokegentID(); pgid != "" && sm.store.Agents != nil {
		sm.store.Agents.Update(pgid, func(id *AgentIdentity) {
			id.Sprite = sprite
		})
		if ident, ok := sm.identities[pgid]; ok {
			ident.Sprite = sprite
		}
	}
	sm.rebuildAgents()
	return nil
}

// getPokegentID returns the pokegent_id for a running session keyed by pokegentID.
func (sm *StateManager) getPokegentID(pokegentID string) string {
	if rs, ok := sm.running[pokegentID]; ok {
		return rs.GetPokegentID()
	}
	return ""
}

// GetIdentity returns the persistent identity for a pokegent_id.
func (sm *StateManager) GetIdentities() map[string]*AgentIdentity {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	cp := make(map[string]*AgentIdentity, len(sm.identities))
	for k, v := range sm.identities {
		cp[k] = v
	}
	return cp
}

func (sm *StateManager) GetIdentity(pokegentID string) *AgentIdentity {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if ident, ok := sm.identities[pokegentID]; ok {
		cp := *ident
		return &cp
	}
	return nil
}

// SaveIdentity persists an agent identity to disk and updates the in-memory cache.
func (sm *StateManager) SaveIdentity(identity AgentIdentity) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.store.Agents == nil {
		return fmt.Errorf("agent identity store not available")
	}
	if err := sm.store.Agents.Save(identity); err != nil {
		return err
	}
	cp := identity
	sm.identities[identity.PokegentID] = &cp
	return nil
}

// GetAgentsByTaskGroup returns all agents that belong to the given task group.
func (sm *StateManager) GetAgentsByTaskGroup(taskGroup string) []AgentState {
	agents := sm.GetAgents()
	var result []AgentState
	for _, a := range agents {
		if a.TaskGroup == taskGroup {
			result = append(result, a)
		}
	}
	return result
}

// composeProfileKey builds the composite profile key from role and project.
func (sm *StateManager) composeProfileKey(role, project, fallback string) string {
	if role != "" && project != "" {
		return role + "@" + project
	}
	if project != "" {
		return "@" + project
	}
	if role != "" {
		return role + "@"
	}
	return fallback
}

// ComposeProfileKey builds a composite key from role and project (public version).
func (sm *StateManager) ComposeProfileKey(role, project, fallback string) string {
	return sm.composeProfileKey(role, project, fallback)
}

// SetPendingRelaunch stores a command to execute when the agent becomes idle.
func (sm *StateManager) SetPendingRelaunch(pokegentID, cmd string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.pendingRelaunches == nil {
		sm.pendingRelaunches = make(map[string]string)
	}
	sm.pendingRelaunches[pokegentID] = cmd
}

// GetPendingRelaunch returns and clears the pending relaunch command for an agent.
func (sm *StateManager) GetPendingRelaunch(pokegentID string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	cmd := sm.pendingRelaunches[pokegentID]
	delete(sm.pendingRelaunches, pokegentID)
	return cmd
}

// TransitionDoneToIdle is a no-op after Phase 2 collapsed done→idle.
// Kept for backward compatibility — callers still invoke it from the
// poller tick, but there are no "done" agents to transition anymore.
func (sm *StateManager) TransitionDoneToIdle() bool {
	return false
}

// TransitionState forces a state/detail change for an agent (used by poller for interrupt detection).
func (sm *StateManager) TransitionState(pokegentID, state, detail string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	if a, ok := sm.agents[pokegentID]; ok {
		a.State = state
		a.Detail = detail
		a.LastUpdated = now
	}
	if sf, ok := sm.statuses[pokegentID]; ok {
		sf.State = state
		sf.Detail = detail
		sf.Timestamp = now
		if sf.FileKey == "" {
			sf.FileKey = pokegentID
		}
		sm.statuses[pokegentID] = sf
		sm.store.Status.Upsert(sf)
	}
}

// CleanStale removes running files for dead sessions and rebuilds state. Returns true if anything changed.
func (sm *StateManager) CleanStale() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	changed := false
	runningDir := filepath.Join(sm.dataDir, "running")
	for pgid, rs := range sm.running {
		alive := false

		// Check 0: Grace period — don't kill files less than 30s old (hook hasn't patched yet)
		pattern := filepath.Join(runningDir, "*-"+pgid+".json")
		matches, _ := filepath.Glob(pattern)
		if len(matches) > 0 {
			if info, err := os.Stat(matches[0]); err == nil {
				if time.Since(info.ModTime()) < 30*time.Second {
					continue // too new to judge
				}
			}
		}

		// Check 1: Claude's PID (most reliable)
		if rs.ClaudePID > 0 && isProcessAlive(rs.ClaudePID) {
			alive = true
		}

		// Check 2: Shell PID (legacy)
		if !alive && rs.PID > 0 && isProcessAlive(rs.PID) {
			alive = true
		}

		// Check 3: Claude session registry (authoritative fallback)
		if !alive {
			alive = sm.isClaudeSessionAlive(pgid)
		}

		if !alive {
			log.Printf("CleanStale: %.8s (%s) iface=%s — NOT alive (claude_pid=%d, pid=%d, tty=%q)",
				pgid, rs.DisplayName, rs.Interface, rs.ClaudePID, rs.PID, rs.TTY)
			if rs.Interface == "chat" {
				// Chat-backed agents can be recovered by respawning their ACP backend.
				// Keep the running file so the card remains visible and the UI can offer
				// a first-class "Restart backend" action instead of disappearing.
				now := time.Now().UTC().Format(time.RFC3339)
				sf := sm.statuses[pgid]
				if sf.State != "error" || !strings.Contains(sf.Detail, "backend process exited") {
					sf.FileKey = pgid
					sf.SessionID = rs.SessionID
					sf.State = "error"
					sf.Detail = "backend process exited — restart backend to recover"
					sf.CWD = rs.CWD
					sf.Timestamp = now
					sf.BusySince = ""
					sm.statuses[pgid] = sf
					_ = sm.store.Status.Upsert(sf)
					changed = true
				}
				continue
			}
			log.Printf("CleanStale: REMOVING iterm2 running file for %.8s (%s)", pgid, rs.DisplayName)
			pattern := filepath.Join(runningDir, "*-"+pgid+".json")
			matches, _ := filepath.Glob(pattern)
			for _, f := range matches {
				os.Remove(f)
			}
			// Cascade: remove completed ephemeral children
			sm.cleanupEphemeralsByParentLocked(pgid)
			delete(sm.running, pgid)
			changed = true
		}
	}
	if changed {
		sm.rebuildAgents()
	}
	return changed
}

// isClaudeSessionAlive checks if there's a live Claude process on the TTY
// associated with this running session.
func (sm *StateManager) isClaudeSessionAlive(pokegentID string) bool {
	rs, ok := sm.running[pokegentID]
	if !ok || rs.TTY == "" {
		return false
	}
	// Check if any Claude process is on this TTY
	sessionsDir := filepath.Join(filepath.Dir(sm.claudeProjectDir), "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionsDir, e.Name()))
		if err != nil {
			continue
		}
		var sess struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(data, &sess) != nil || !isProcessAlive(sess.PID) {
			continue
		}
		out, _ := exec.Command("ps", "-p", fmt.Sprintf("%d", sess.PID), "-o", "tty=").Output()
		tty := "/dev/" + strings.TrimSpace(string(out))
		if tty == rs.TTY {
			return true
		}
	}
	return false
}

// ReconcileRunningFiles checks Claude's session registry for running files that have
// mismatched session IDs and patches them. Returns true if anything changed.
func (sm *StateManager) ReconcileRunningFiles() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	changed := false
	sessionsDir := filepath.Join(filepath.Dir(sm.claudeProjectDir), "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return false
	}

	// Build map of TTY → Claude PID from session registry
	// NOTE: we only backfill claude_pid here, NOT session_id.
	// The session ID in ~/.claude/sessions/ is a process ID, not the conversation ID.
	ttyToPID := make(map[string]int)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionsDir, e.Name()))
		if err != nil {
			continue
		}
		var sess struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(data, &sess) != nil || !isProcessAlive(sess.PID) {
			continue
		}
		out, err := exec.Command("ps", "-p", fmt.Sprintf("%d", sess.PID), "-o", "tty=").Output()
		if err == nil {
			tty := "/dev/" + strings.TrimSpace(string(out))
			if tty != "/dev/" {
				ttyToPID[tty] = sess.PID
			}
		}
	}

	for pgid, rs := range sm.running {
		if rs.ClaudePID > 0 {
			continue
		}
		if pid, ok := ttyToPID[rs.TTY]; ok {
			if err := sm.store.Running.Update(pgid, func(r *RunningSession) {
				r.ClaudePID = pid
			}); err == nil {
				changed = true
			}
		}
	}

	if changed {
		sm.loadRunning()
		sm.rebuildAgents()
	}
	return changed
}

// UpdateUserPrompt sets the user prompt for an agent.
func (sm *StateManager) UpdateUserPrompt(pokegentID, prompt string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sf, ok := sm.statuses[pokegentID]; ok {
		sf.UserPrompt = prompt
		sm.statuses[pokegentID] = sf
	}
	if a, ok := sm.agents[pokegentID]; ok {
		a.UserPrompt = prompt
	}
}

// BeginPrompt resets current-turn display state as soon as the dashboard
// accepts a prompt. Hook/ACP events will fill in the live output shortly after,
// but clearing here prevents stale prior-turn commands from lingering in card
// previews during that handoff window.
func (sm *StateManager) BeginPrompt(pokegentID, prompt string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	busySince := now.UTC().Format(time.RFC3339)
	truncatedPrompt := truncate(prompt, 200)

	sf, ok := sm.statuses[pokegentID]
	if ok {
		sf.State = "busy"
		sf.Detail = "processing prompt"
		sf.BusySince = busySince
		sf.Timestamp = busySince
		sf.LastSummary = ""
		sf.LastTrace = ""
		sf.RecentActions = nil
		sf.UserPrompt = truncatedPrompt
		sm.statuses[pokegentID] = sf
	}

	sm.activityFeeds[pokegentID] = nil
	sm.feedClearedAt[pokegentID] = now

	if a, ok := sm.agents[pokegentID]; ok {
		a.State = "busy"
		a.Detail = "processing prompt"
		a.BusySince = busySince
		a.LastUpdated = busySince
		a.LastSummary = ""
		a.LastTrace = ""
		a.RecentActions = nil
		a.ActivityFeed = nil
		a.UserPrompt = truncatedPrompt
	}
}

// UpdateContext updates token usage for an agent.
func (sm *StateManager) UpdateContext(pokegentID string, tokens, window int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.contexts[pokegentID] = ContextUsage{Tokens: tokens, Window: window}
	if a, ok := sm.agents[pokegentID]; ok {
		a.ContextTokens = tokens
		a.ContextWindow = window
	}
}

// UpdateSummary updates the last summary for an agent.
func (sm *StateManager) UpdateSummary(pokegentID, summary string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sf, ok := sm.statuses[pokegentID]; ok {
		sf.LastSummary = summary
		sm.statuses[pokegentID] = sf
	}
	if a, ok := sm.agents[pokegentID]; ok {
		a.LastSummary = summary
	}
}

// UpdateTrace updates just the trace for an agent.
func (sm *StateManager) UpdateTrace(pokegentID, trace string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sf, ok := sm.statuses[pokegentID]; ok {
		sf.LastTrace = trace
		sm.statuses[pokegentID] = sf
	}
	if a, ok := sm.agents[pokegentID]; ok {
		a.LastTrace = trace
	}
}

// UpdateActivityFeed merges transcript-extracted text/thinking items into the
// hook-built activity feed. Hooks provide real-time tool calls; the poller
// provides text/thinking blocks that appear between tools in the transcript.
func (sm *StateManager) UpdateActivityFeed(pokegentID string, transcriptFeed []ActivityItem) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Filter out transcript entries that predate the last feed clear (UserPromptSubmit).
	// This prevents stale entries from the previous turn from re-appearing.
	if clearedAt, ok := sm.feedClearedAt[pokegentID]; ok {
		filtered := make([]ActivityItem, 0, len(transcriptFeed))
		for _, item := range transcriptFeed {
			if item.Time == "" {
				filtered = append(filtered, item)
				continue
			}
			// Parse HH:MM:SS time, assume today
			if t, err := time.ParseInLocation("15:04:05", item.Time, time.Now().Location()); err == nil {
				now := time.Now()
				itemTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
				if itemTime.Before(clearedAt) {
					continue // skip stale entry
				}
			}
			filtered = append(filtered, item)
		}
		transcriptFeed = filtered
	}

	existing := sm.activityFeeds[pokegentID]

	// If no existing feed, use the transcript feed directly
	if len(existing) == 0 {
		if len(transcriptFeed) == 0 {
			return
		}
		sm.activityFeeds[pokegentID] = transcriptFeed
		if a, ok := sm.agents[pokegentID]; ok {
			a.ActivityFeed = transcriptFeed
		}
		return
	}

	// Only ADD text/thinking items from the transcript that aren't already
	// in the feed. Hook events are the primary source for tool calls (immediate),
	// and the transcript supplements with text/thinking between tools.
	// Never replace or reorder existing items.
	existingTexts := make(map[string]bool)
	for _, item := range existing {
		if item.Type == "text" || item.Type == "thinking" {
			existingTexts[item.Text] = true
		}
	}

	added := false
	for _, item := range transcriptFeed {
		if item.Type == "text" || item.Type == "thinking" {
			if !existingTexts[item.Text] {
				existing = append(existing, item)
				existingTexts[item.Text] = true
				added = true
			}
		}
	}

	if added {
		if len(existing) > 20 {
			existing = existing[len(existing)-20:]
		}
		sm.activityFeeds[pokegentID] = existing
		if a, ok := sm.agents[pokegentID]; ok {
			a.ActivityFeed = existing
		}
	}
}

// FindTranscriptPath locates the transcript JSONL for an agent. Thread-safe.
// Searches across all backends (Claude, Codex, etc.). Results are cached by pokegent_id.
// Internally uses the Claude session_id for file discovery.
func (sm *StateManager) FindTranscriptPath(pokegentID string) string {
	sm.mu.RLock()
	explicit := sm.explicitRunningTranscriptPathLocked(pokegentID)
	cached, ok := sm.transcriptPaths[pokegentID]
	primaryID, rs, hasRunning := sm.runningSessionForIDLocked(pokegentID)
	sm.mu.RUnlock()
	if explicit != "" {
		if cached != explicit {
			sm.mu.Lock()
			sm.transcriptPaths[primaryID] = explicit
			sm.mu.Unlock()
		}
		return explicit
	}
	if ok && cached != "" {
		staleMigrationSource := hasRunning && rs.TranscriptPath == "" && rs.SourceTranscriptPath != "" && samePath(cached, rs.SourceTranscriptPath)
		if !staleMigrationSource {
			if _, err := os.Stat(cached); err == nil {
				return cached
			}
		}
		// Cached path is stale — evict and re-discover. This is especially
		// important after Claude→Codex migration: the cache can still point at the
		// immutable source_transcript_path, while the fresh Codex transcript appears
		// later (often after the first prompt).
		sm.mu.Lock()
		delete(sm.transcriptPaths, pokegentID)
		if primaryID != pokegentID {
			delete(sm.transcriptPaths, primaryID)
		}
		sm.mu.Unlock()
	}
	path := sm.findTranscriptPathLocked(pokegentID)
	if path != "" {
		shouldPersist := false
		sm.mu.Lock()
		sm.transcriptPaths[primaryID] = path
		if hasRunning && rs.TranscriptPath == "" && !samePath(path, rs.SourceTranscriptPath) {
			rs.TranscriptPath = path
			sm.running[primaryID] = rs
			shouldPersist = true
		}
		sm.mu.Unlock()
		if shouldPersist && sm.store != nil && sm.store.Running != nil {
			_ = sm.store.Running.Update(primaryID, func(r *RunningSession) {
				r.TranscriptPath = path
			})
		}
	}
	return path
}

func (sm *StateManager) runningSessionForIDLocked(id string) (string, RunningSession, bool) {
	if rs, ok := sm.running[id]; ok {
		return id, rs, true
	}
	for pgid, rs := range sm.running {
		if rs.PokegentID == id || rs.SessionID == id {
			return pgid, rs, true
		}
	}
	return id, RunningSession{}, false
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func (sm *StateManager) explicitRunningTranscriptPathLocked(id string) string {
	if rs, ok := sm.running[id]; ok && rs.TranscriptPath != "" {
		if _, err := os.Stat(rs.TranscriptPath); err == nil {
			return rs.TranscriptPath
		}
	}
	if rs, ok := sm.running[id]; ok && rs.LastGoodTranscriptPath != "" {
		if _, err := os.Stat(rs.LastGoodTranscriptPath); err == nil {
			return rs.LastGoodTranscriptPath
		}
	}
	for _, rs := range sm.running {
		if rs.PokegentID == id || rs.SessionID == id {
			if rs.TranscriptPath != "" {
				if _, err := os.Stat(rs.TranscriptPath); err == nil {
					return rs.TranscriptPath
				}
			}
			if rs.LastGoodTranscriptPath != "" {
				if _, err := os.Stat(rs.LastGoodTranscriptPath); err == nil {
					return rs.LastGoodTranscriptPath
				}
			}
			return ""
		}
	}
	return ""
}

func (sm *StateManager) findTranscriptPathLocked(pokegentID string) string {
	// Check if the running session has an explicit transcript path (non-Claude
	// backends). The caller may pass either the stable pokegent_id or the
	// backend's current session_id, so resolve both forms before falling back to
	// filesystem discovery.
	if rs, ok := sm.running[pokegentID]; ok && rs.TranscriptPath != "" {
		if _, err := os.Stat(rs.TranscriptPath); err == nil {
			return rs.TranscriptPath
		}
	}
	for pgid, rs := range sm.running {
		if pgid == pokegentID || rs.PokegentID == pokegentID || rs.SessionID == pokegentID {
			if rs.TranscriptPath != "" {
				if _, err := os.Stat(rs.TranscriptPath); err == nil {
					return rs.TranscriptPath
				}
			}
			if rs.LastGoodTranscriptPath != "" {
				if _, err := os.Stat(rs.LastGoodTranscriptPath); err == nil {
					return rs.LastGoodTranscriptPath
				}
			}
			if pgid != pokegentID {
				pokegentID = pgid
			}
			break
		}
	}
	// Use the Claude session_id for file lookup (transcripts are named by session_id)
	sessionID := pokegentID
	if rs, ok := sm.running[pokegentID]; ok && rs.SessionID != "" {
		sessionID = rs.SessionID
	}
	return storelib.FindTranscriptPathWithDataDir(sessionID, sm.claudeProjectDir, sm.dataDir)
}

// InvalidateTranscriptPath evicts a cached transcript path.
func (sm *StateManager) InvalidateTranscriptPath(pokegentID string) {
	sm.mu.Lock()
	delete(sm.transcriptPaths, pokegentID)
	sm.mu.Unlock()
}

// GetProfiles returns all loaded profiles.
// GetNameOverrides returns a copy of the name overrides map.
func (sm *StateManager) GetNameOverrides() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	m := make(map[string]string, len(sm.nameOverrides))
	for k, v := range sm.nameOverrides {
		m[k] = v
	}
	return m
}

// GetSessionToPokegent returns a copy of the Claude session_id → pokegent_id reverse lookup map.
func (sm *StateManager) GetSessionToPokegent() map[string]string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	m := make(map[string]string, len(sm.sessionToPokegent))
	for k, v := range sm.sessionToPokegent {
		m[k] = v
	}
	return m
}

// GetProfile returns a single profile by name.
func (sm *StateManager) GetProfile(name string) *Profile {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if p, ok := sm.profiles[name]; ok {
		return &p
	}
	return nil
}

func (sm *StateManager) GetProfiles() []Profile {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]Profile, 0, len(sm.profiles))
	for _, p := range sm.profiles {
		result = append(result, p)
	}
	return result
}

// ReloadRunning reloads running session files and rebuilds agents.
func (sm *StateManager) ReloadRunning() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.loadRunning()
	sm.rebuildAgents()
}

// ReloadStatus reloads a single status file and rebuilds agents.
func (sm *StateManager) ReloadStatus(path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	base := strings.TrimSuffix(filepath.Base(path), ".json")
	sf, err := sm.store.Status.Get(base)
	if err != nil || sf == nil {
		// File was deleted or unreadable — remove from statuses.
		// base is the pokegent_id (filename). Direct lookup.
		if _, ok := sm.statuses[base]; ok {
			delete(sm.statuses, base)
		} else {
			// Fallback: base might be a session_id used by legacy hooks — scan reverse map
			if pgid, ok := sm.sessionToPokegent[base]; ok {
				delete(sm.statuses, pgid)
			}
		}
	} else {
		// Key by pokegent_id (filename base), not sf.SessionID (which is the ACP/Claude session_id)
		sm.statuses[base] = *sf
	}
	sm.rebuildAgents()
}

// UpdateFromEvent processes an incoming hook event.
func (sm *StateManager) UpdateFromEvent(evt HookEvent) *AgentState {
	if evt.SessionID == "" {
		return nil
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Resolve hook's session_id to pokegent_id using reverse lookup
	pgid := sm.sessionToPokegent[evt.SessionID]
	if pgid == "" {
		// Scan running sessions for matching Claude session_id
		for pg, rs := range sm.running {
			if rs.SessionID == evt.SessionID {
				pgid = pg
				break
			}
		}
	}
	if pgid == "" {
		pgid = evt.SessionID // temporary key for truly unknown sessions
	}

	// Update status from event (mirrors status-update.sh logic)
	sf, exists := sm.statuses[pgid]
	if !exists {
		sf = StatusFile{SessionID: evt.SessionID}
	}

	// Guard against race conditions: a slow PreToolUse/PostToolUse arriving after
	// Stop should not overwrite idle/error with "busy". Only UserPromptSubmit can
	// transition out of done/error/idle.
	if exists && sf.State != "busy" && sf.State != "needs_input" && sf.State != "" {
		switch evt.HookEventName {
		case "PreToolUse", "PostToolUse", "PostToolUseFailure":
			return nil
		}
	}

	// Normalize incoming "done" from hooks to "idle" — the hook's status-update.sh
	// still writes "done" but the server treats it as idle (Phase 2 collapse).
	if sf.State == "done" {
		sf.State = "idle"
	}

	sf.CWD = evt.CWD
	sf.Timestamp = time.Now().UTC().Format(time.RFC3339)

	switch evt.HookEventName {
	case "UserPromptSubmit":
		sf.State = "busy"
		sf.Detail = "processing prompt"
		sf.BusySince = time.Now().UTC().Format(time.RFC3339)
		sf.LastSummary = ""
		sf.LastTrace = ""
		sf.RecentActions = nil
		if evt.Prompt != "" {
			sf.UserPrompt = truncate(evt.Prompt, 200)
		}
	case "PreToolUse":
		sf.State = "busy"
		toolInput := ""
		if m, ok := evt.ToolInput.(map[string]any); ok {
			for _, key := range []string{"command", "file_path", "pattern", "query", "description"} {
				if v, ok := m[key]; ok {
					toolInput = truncate(toString(v), 80)
					break
				}
			}
		}
		sf.Detail = evt.ToolName + ": " + toolInput
		sf.RecentActions = appendAction(sf.RecentActions, evt.ToolName+": "+toolInput)
		sf.LastTrace = extractTraceFromTranscript(evt.TranscriptPath)
	case "PostToolUse":
		sf.State = "busy"
		sf.Detail = "completed " + evt.ToolName
		sf.LastTrace = extractTraceFromTranscript(evt.TranscriptPath)
	case "PostToolUseFailure":
		sf.State = "busy"
		sf.Detail = evt.ToolName + " failed"
		sf.RecentActions = appendAction(sf.RecentActions, evt.ToolName+" failed")
		sf.LastTrace = extractTraceFromTranscript(evt.TranscriptPath)
	case "StopFailure":
		sf.State = "error"
		sf.Detail = "API error — reprompt to retry"
		sf.RecentActions = nil
		sf.BusySince = ""
	case "Stop":
		sf.State = "idle"
		sf.Detail = "finished"
		sf.RecentActions = nil
		sf.BusySince = ""
		sf.LastSummary = truncate(evt.LastAssistantMessage, 2000)
	case "PermissionRequest":
		sf.State = "needs_input"
		sf.Detail = "needs permission for " + evt.ToolName
	case "Notification":
		if evt.NotificationType == "idle_prompt" {
			// idle_prompt only transitions busy → idle (never sets needs_input;
			// that's exclusively for PermissionRequest)
			if sf.State == "busy" {
				sf.State = "idle"
				sf.Detail = "finished"
				sf.BusySince = ""
				if evt.TranscriptPath != "" {
					trace := extractTraceFromTranscript(evt.TranscriptPath)
					if trace != "" {
						sf.LastSummary = trace
					}
				}
			}
		}
	case "SessionStart":
		sf.State = "idle"
		sf.Detail = "session started"
	case "SessionEnd":
		// Remove from statuses — agent disappears from dashboard
		delete(sm.statuses, pgid)
		// Cascade: remove completed ephemeral children so they don't linger
		sm.cleanupEphemeralsByParentLocked(pgid)
		sm.rebuildAgents()
		return nil
	default:
		return nil
	}

	sm.statuses[pgid] = sf

	// Update context from transcript if available
	if evt.TranscriptPath != "" {
		ctx := extractContextUsage(evt.TranscriptPath)
		if ctx.Tokens > 0 {
			sm.contexts[pgid] = ctx
		}
	}

	sm.rebuildAgents()

	// Append tool calls to activity feed for immediate display
	{
		ts := time.Now().Local().Format("15:04:05")
		feed := sm.activityFeeds[pgid]
		switch evt.HookEventName {
		case "UserPromptSubmit":
			feed = nil
			sm.feedClearedAt[pgid] = time.Now()
		case "PreToolUse":
			toolInput := ""
			if m, ok := evt.ToolInput.(map[string]any); ok {
				for _, key := range []string{"command", "file_path", "pattern", "query", "description"} {
					if v, ok := m[key]; ok {
						toolInput = truncate(toString(v), 80)
						break
					}
				}
			}
			feed = append(feed, ActivityItem{Time: ts, Type: "tool", Text: evt.ToolName + ": " + toolInput})
			if len(feed) > 20 {
				feed = feed[len(feed)-20:]
			}
		case "PostToolUseFailure":
			feed = append(feed, ActivityItem{Time: ts, Type: "tool", Text: evt.ToolName + " failed"})
			if len(feed) > 20 {
				feed = feed[len(feed)-20:]
			}
		}
		sm.activityFeeds[pgid] = feed
		if a, ok := sm.agents[pgid]; ok {
			a.ActivityFeed = feed
		}
	}

	if a, ok := sm.agents[pgid]; ok {
		cp := *a
		return &cp
	}
	return nil
}

// --- internal helpers ---

func (sm *StateManager) loadProfiles() error {
	profiles, err := sm.store.Profiles.List()
	if err != nil {
		return nil
	}
	sm.profiles = make(map[string]Profile, len(profiles))
	for _, p := range profiles {
		sm.profiles[p.Name] = p
	}
	return nil
}

func (sm *StateManager) loadRunning() error {
	sessions, err := sm.store.Running.List()
	if err != nil {
		return nil
	}
	sm.running = make(map[string]RunningSession, len(sessions))
	for _, rs := range sessions {
		pgid := rs.PokegentID
		if pgid == "" {
			pgid = rs.SessionID // last resort fallback
		}
		if pgid != "" {
			sm.running[pgid] = rs
		}
	}
	return nil
}

func (sm *StateManager) loadStatuses() error {
	statuses, err := sm.store.Status.List()
	if err != nil {
		return nil
	}
	sm.statuses = make(map[string]StatusFile, len(statuses))
	for _, sf := range statuses {
		if sf.SessionID == "" {
			continue
		}
		// Status files are named {pokegent_id}.json — use the filename as key.
		pgid := sf.FileKey
		if pgid == "" {
			pgid = sf.SessionID
		}
		sm.statuses[pgid] = sf
	}
	return nil
}

func (sm *StateManager) loadEphemeral() error {
	if sm.store.Ephemeral == nil {
		return nil
	}
	agents, err := sm.store.Ephemeral.List()
	if err != nil {
		return err
	}
	for i := range agents {
		sm.ephemeral[agents[i].AgentID] = &agents[i]
	}
	return nil
}

func (sm *StateManager) loadIdentities() error {
	if sm.store.Agents == nil {
		return nil
	}
	agents, err := sm.store.Agents.List()
	if err != nil {
		return err
	}
	for i := range agents {
		sm.identities[agents[i].PokegentID] = &agents[i]
	}
	return nil
}

func (sm *StateManager) loadNameOverrides() {
	sm.store.Metadata.LoadJSON("name-overrides.json", &sm.nameOverrides)
}

func (sm *StateManager) saveNameOverrides() {
	sm.store.Metadata.SaveJSON("name-overrides.json", sm.nameOverrides)
}

func (sm *StateManager) loadAgentOrder() {
	sm.store.Metadata.LoadJSON("agent-order.json", &sm.agentOrder)
}

func (sm *StateManager) saveAgentOrder() {
	sm.store.Metadata.SaveJSON("agent-order.json", sm.agentOrder)
}

// GetAgentOrder returns the current agent display order.
func (sm *StateManager) GetAgentOrder() []string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]string, len(sm.agentOrder))
	copy(result, sm.agentOrder)
	return result
}

// SetAgentOrder saves a new agent display order.
func (sm *StateManager) SetAgentOrder(order []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.agentOrder = order
	sm.saveAgentOrder()
}

// CreateEphemeral registers a new ephemeral subagent in memory and on disk.
func (sm *StateManager) CreateEphemeral(ea EphemeralAgent) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.store.Ephemeral == nil {
		return fmt.Errorf("ephemeral store not available")
	}
	if err := sm.store.Ephemeral.Create(ea); err != nil {
		return err
	}
	sm.ephemeral[ea.AgentID] = &ea
	return nil
}

// CompleteEphemeral marks an ephemeral subagent as completed in memory and on disk.
func (sm *StateManager) CompleteEphemeral(agentID, lastMessage, transcriptPath string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.store.Ephemeral == nil {
		return fmt.Errorf("ephemeral store not available")
	}
	if err := sm.store.Ephemeral.Complete(agentID, lastMessage, transcriptPath); err != nil {
		return err
	}
	// Update in-memory
	if ea, ok := sm.ephemeral[agentID]; ok {
		ea.State = "completed"
		ea.LastMessage = lastMessage
		ea.TranscriptPath = transcriptPath
		ea.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		if ea.CreatedAt != "" {
			if created, err := time.Parse(time.RFC3339, ea.CreatedAt); err == nil {
				ea.DurationSec = int(time.Since(created).Seconds())
			}
		}
	}
	return nil
}

// DeleteEphemeral removes an ephemeral subagent from memory and disk.
// Always removes from memory even if the disk file is already gone.
func (sm *StateManager) DeleteEphemeral(agentID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.store.Ephemeral != nil {
		// Best-effort disk removal — ignore "not found" errors
		if err := sm.store.Ephemeral.Delete(agentID); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	delete(sm.ephemeral, agentID)
	return nil
}

// cleanupEphemeralsByParentLocked removes completed ephemeral agents for a parent session.
// Caller must hold sm.mu.
func (sm *StateManager) cleanupEphemeralsByParentLocked(parentSessionID string) int {
	if sm.store.Ephemeral == nil {
		return 0
	}
	removed := 0
	for id, ea := range sm.ephemeral {
		if ea.ParentSessionID != parentSessionID {
			continue
		}
		if ea.State != "completed" {
			continue
		}
		if err := sm.store.Ephemeral.Delete(id); err == nil {
			delete(sm.ephemeral, id)
			removed++
		}
	}
	return removed
}

func (sm *StateManager) rebuildAgents() {
	now := time.Now()
	agents := make(map[string]*AgentState)

	// Reload identities from disk — new agents may have created identity files since startup
	if sm.store.Agents != nil {
		if loaded, err := sm.store.Agents.List(); err == nil {
			for i := range loaded {
				sm.identities[loaded[i].PokegentID] = &loaded[i]
			}
		}
	}

	// Build reverse lookup: Claude session_id → pokegent_id
	sm.sessionToPokegent = make(map[string]string, len(sm.running))
	for pgid, rs := range sm.running {
		if rs.SessionID != "" {
			sm.sessionToPokegent[rs.SessionID] = pgid
		}
	}

	// Migrate agentOrder entries from session_id to pokegent_id if needed
	for i, entry := range sm.agentOrder {
		if pgid, ok := sm.sessionToPokegent[entry]; ok {
			sm.agentOrder[i] = pgid
		}
	}

	// Start with running sessions as the base (keyed by pokegent_id)
	for pgid, rs := range sm.running {
		a := &AgentState{
			SessionID:      rs.SessionID,
			PokegentID:     pgid,
			ProfileName:    rs.Profile,
			Role:           rs.Role,
			Project:        rs.Project,
			TaskGroup:      rs.TaskGroup,
			DisplayName:    rs.DisplayName,
			PID:            rs.PID,
			TTY:            rs.TTY,
			ITermSessionID: rs.ITermSessionID,
			State:          "idle",
		}

		// Seed model/effort from running session (launch-time snapshot)
		a.Model = rs.Model
		a.Effort = rs.Effort
		a.Sprite = rs.Sprite

		// Enrich from role config (emoji, model, effort)
		if rs.Role != "" && sm.store != nil {
			if role, err := sm.store.Roles.Get(rs.Role); err == nil {
				a.RoleEmoji = role.Emoji
				if a.Emoji == "" {
					a.Emoji = role.Emoji
				}
				if role.Model != "" {
					a.Model = role.Model
				}
				if role.Effort != "" {
					a.Effort = role.Effort
				}
			}
		}

		// Enrich from project config (color, model/effort baseline)
		if rs.Project != "" && sm.store != nil {
			if proj, err := sm.store.Projects.Get(rs.Project); err == nil {
				a.ProjectColor = proj.Color
				a.Color = proj.Color
				if a.DisplayName == "" {
					a.DisplayName = proj.Title
				}
				// Project provides baseline — only fill if role didn't set
				if a.Model == "" && proj.Model != "" {
					a.Model = proj.Model
				}
				if a.Effort == "" && proj.Effort != "" {
					a.Effort = proj.Effort
				}
			}
		}

		// Enrich from legacy profile (fallback)
		if p, ok := sm.profiles[rs.Profile]; ok {
			if a.Emoji == "" {
				a.Emoji = p.Emoji
			}
			if a.Color == [3]int{} {
				a.Color = p.Color
			}
			if a.DisplayName == "" {
				a.DisplayName = p.Title
			}
		}

		// Apply persistent name override (keyed by pokegent_id)
		if override, ok := sm.nameOverrides[pgid]; ok {
			a.DisplayName = override
		}

		// Merge persistent identity (source of truth for display/config fields)
		if ident, ok := sm.identities[pgid]; ok {
			if ident.DisplayName != "" {
				a.DisplayName = ident.DisplayName
			}
			if ident.Sprite != "" {
				a.Sprite = ident.Sprite
			}
			if ident.Role != "" {
				a.Role = ident.Role
			}
			if ident.Project != "" {
				a.Project = ident.Project
			}
			if ident.TaskGroup != "" {
				a.TaskGroup = ident.TaskGroup
			}
			if ident.Model != "" {
				a.Model = ident.Model
			}
			if ident.Effort != "" {
				a.Effort = ident.Effort
			}
			if ident.CreatedAt != "" {
				a.CreatedAt = ident.CreatedAt
			}
			if ident.Interface != "" {
				a.Interface = ident.Interface
			}
		}
		// Running-file Interface field wins over identity (it's the live truth
		// for interface-migration scenarios).
		if rs.Interface != "" {
			a.Interface = rs.Interface
		}
		if rs.AgentBackend != "" {
			a.AgentBackend = rs.AgentBackend
			backendType := rs.AgentBackend
			backendModelLabel := ""
			if sm.backendStore != nil {
				if bc, ok := sm.backendStore.Get(rs.AgentBackend); ok {
					backendType = bc.Type
					backendModelLabel = bc.ResolvedModelLabel()
				}
			}
			nonClaude := !isClaudeBackend(rs.AgentBackend) && !isClaudeBackend(backendType)
			if nonClaude {
				modelLabel := a.Model
				if modelLabel == "" || isClaudeModel(modelLabel) {
					modelLabel = backendModelLabel
				}
				a.Model = displayModelForBackend(modelLabel, rs.AgentBackend, backendType)
			} else if a.Model == "" {
				a.Model = displayModelForBackend(backendModelLabel, rs.AgentBackend, backendType)
			}
		}

		// Check PID liveness
		a.IsAlive = isProcessAlive(rs.PID)

		// Get creation time from running file field, fall back to mtime
		if rs.CreatedAt != "" {
			a.CreatedAt = rs.CreatedAt
		} else {
			runningDir := filepath.Join(sm.dataDir, "running")
			pattern := filepath.Join(runningDir, "*-"+pgid+".json")
			if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
				if info, err := os.Stat(matches[0]); err == nil {
					a.CreatedAt = info.ModTime().UTC().Format(time.RFC3339)
				}
			}
		}

		agents[pgid] = a
	}

	// Merge status data (keyed by pokegent_id)
	for pgid, sf := range sm.statuses {
		a, exists := agents[pgid]
		if !exists {
			a = &AgentState{
				SessionID:  sf.SessionID,
				PokegentID: pgid,
			}
			// Try to match profile by CWD
			a.ProfileName, a.Emoji, a.Color = sm.matchProfileLocked(sf.CWD)
			agents[pgid] = a
		}
		a.State = sf.State
		a.Detail = sf.Detail
		a.CWD = sf.CWD
		a.LastSummary = sf.LastSummary
		a.LastTrace = sf.LastTrace
		a.RecentActions = sf.RecentActions
		a.UserPrompt = sf.UserPrompt
		a.LastUpdated = sf.Timestamp
		a.BusySince = sf.BusySince
		if sf.ContextTokens > 0 {
			a.ContextTokens = sf.ContextTokens
		}
		if sf.ContextWindow > 0 {
			a.ContextWindow = sf.ContextWindow
		}

		// Compute duration
		if t, err := time.Parse(time.RFC3339, sf.Timestamp); err == nil {
			a.DurationSec = int(now.Sub(t).Seconds())
		}
	}

	// Re-apply persisted context usage (only if non-zero — don't overwrite
	// correct values from the status file with stale zeros from transcript extraction).
	for pgid, ctx := range sm.contexts {
		if a, ok := agents[pgid]; ok {
			if ctx.Tokens > 0 {
				a.ContextTokens = ctx.Tokens
			}
			if ctx.Window > 0 {
				a.ContextWindow = ctx.Window
			}
		}
	}

	// Re-apply persisted activity feeds
	for pgid, feed := range sm.activityFeeds {
		if a, ok := agents[pgid]; ok {
			a.ActivityFeed = feed
		}
	}

	sm.agents = agents
}

// MatchProfile finds the best matching profile for a given cwd. Thread-safe.
func (sm *StateManager) MatchProfile(cwd string) (name, emoji string, color [3]int) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.matchProfileLocked(cwd)
}

func (sm *StateManager) matchProfileLocked(cwd string) (name, emoji string, color [3]int) {
	if cwd == "" {
		return "", "", [3]int{}
	}
	bestLen := 0
	for _, p := range sm.profiles {
		if strings.HasPrefix(cwd, p.CWD) && len(p.CWD) > bestLen {
			name = p.Name
			emoji = p.Emoji
			color = p.Color
			bestLen = len(p.CWD)
		}
	}
	return
}

func appendAction(actions []string, action string) []string {
	actions = append(actions, action)
	if len(actions) > 6 {
		actions = actions[len(actions)-6:]
	}
	return actions
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}

// ContextUsage holds token counts extracted from a transcript.
type ContextUsage struct {
	Tokens int
	Window int
}

// extractContextUsage reads the last assistant message's usage from the transcript.
// Auto-detects the JSONL format (Claude, Codex, etc.) and delegates to the
// appropriate parser implementation.
func extractContextUsage(path string) ContextUsage {
	if path == "" {
		return ContextUsage{}
	}
	parser := storelib.DetectParser(path)
	cu := parser.ExtractContextUsage(path)
	return ContextUsage{Tokens: cu.Tokens, Window: cu.Window}
}

// extractTraceFromTranscript reads the tail of a transcript JSONL and returns
// the last assistant text block (the live thinking/output trace).
// Auto-detects format (Claude, Codex, etc.).
func extractTraceFromTranscript(path string) string {
	if path == "" {
		return ""
	}
	parser := storelib.DetectParser(path)
	return parser.ExtractTrace(path)
}

// extractLastMessages reads the tail of a transcript JSONL and returns
// the last user message (user_prompt) and last assistant text (last_summary).
// Auto-detects format (Claude, Codex, etc.).
func extractLastMessages(path string) (userPrompt, lastSummary string) {
	if path == "" {
		return "", ""
	}
	parser := storelib.DetectParser(path)
	return parser.ExtractLastMessages(path)
}

// extractActivityFeed reads the transcript and builds a unified timeline of
// tool calls and text/thinking output from the last user turn.
// Auto-detects format (Claude, Codex, etc.).
func extractActivityFeed(path string) []ActivityItem {
	if path == "" {
		return nil
	}
	parser := storelib.DetectParser(path)
	storeFeed := parser.ExtractActivityFeed(path)
	// Convert store.ActivityItem to server.ActivityItem
	feed := make([]ActivityItem, len(storeFeed))
	for i, item := range storeFeed {
		feed[i] = ActivityItem{Time: item.Time, Type: item.Type, Text: item.Text}
	}
	return feed
}

// extractLastUserPrompt reads the transcript and returns the last user message.
// Auto-detects format (Claude, Codex, etc.).
func extractLastUserPrompt(path string) string {
	if path == "" {
		return ""
	}
	parser := storelib.DetectParser(path)
	return parser.ExtractLastUserPrompt(path)
}

// extractLastAssistantMessage reads the tail of a transcript and returns the
// last assistant text block, truncated to 200 chars from the start. Used to
// backfill LastSummary on cold start when hooks haven't fired yet.
// Auto-detects format (Claude, Codex, etc.).
func extractLastAssistantMessage(path string) string {
	if path == "" {
		return ""
	}
	// Use ExtractTrace which already does tail-200 extraction
	parser := storelib.DetectParser(path)
	trace := parser.ExtractTrace(path)
	// ExtractTrace returns last 200 chars from the end; we want first 200 for summary
	_, summary := parser.ExtractLastMessages(path)
	if summary != "" {
		r := []rune(summary)
		if len(r) > 200 {
			return string(r[:200])
		}
		return summary
	}
	return trace
}

// isTranscriptInterrupted checks if the last entry in a transcript JSONL is
// "[Request interrupted by user]" — written by Claude Code on Ctrl+C.
// No hook fires for interrupts, so the poller must detect this from the transcript.
// Auto-detects format (Claude, Codex, etc.).
func isTranscriptInterrupted(path string) bool {
	if path == "" {
		return false
	}
	parser := storelib.DetectParser(path)
	return parser.IsInterrupted(path)
}
