package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"pokegents/dashboard/server/services"
	"pokegents/dashboard/server/store"
)

// Server is the main dashboard HTTP server.
type Server struct {
	state        *StateManager
	eventBus     *EventBus
	notifier     *Notifier
	storeWatcher *store.StoreWatcher
	msgSvc       *services.MessagingService
	searchSvc    *services.SearchService
	terminal     TerminalIntegration
	fileStore    *store.Store
	dataDir      string
	chatMgr      *ChatManager
	runtimes     runtimeRegistry
	backendStore *store.BackendStore
	paths        PathService
	mux          *http.ServeMux
	httpServer   *http.Server
	port         int
	bindHost     string
	webDir       string
	usageLog     *UsageLogger

	pendingResumeTaskGroups map[string]string // old session_id → task group
	pendingResumeSpriteMu   sync.Mutex        // guards pendingResumeTaskGroups
}

// Config holds server configuration.
type Config struct {
	Port             int
	BindHost         string
	DataDir          string
	ClaudeProjectDir string
	SearchDBPath     string
	WebDir           string
}

// DefaultConfig returns config with sensible defaults, reading port from the
// Pokegents data dir config.
func DefaultConfig() Config {
	paths := NewPathService("")

	// Read port from config file
	port := 7834
	if data, err := os.ReadFile(paths.ConfigPath()); err == nil {
		var cfg struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(data, &cfg) == nil && cfg.Port > 0 {
			port = cfg.Port
		}
	}

	return Config{
		Port:             port,
		BindHost:         "127.0.0.1",
		DataDir:          paths.DataDir,
		ClaudeProjectDir: paths.ClaudeProjectsDir(),
		SearchDBPath:     filepath.Join(paths.DataDir, "search.db"),
		WebDir:           "", // set at runtime
	}
}

func NewServer(cfg Config) (*Server, error) {
	// One-time fixup: chat status files written before the runtime-parity
	// refactor stored pokegent_id in the `session_id` field, which made
	// state.go's status lookup miss for chat agents (causing last_summary
	// to flicker as the JSONL backfill briefly populated it then got
	// clobbered by rebuildAgents). Idempotent.
	fixupStatusFiles(cfg.DataDir)

	fileStore := store.NewFileStore(cfg.DataDir)
	state := NewStateManagerWithStore(fileStore, cfg.DataDir, cfg.ClaudeProjectDir)
	eventBus := NewEventBus()
	notifier := NewNotifier(cfg.WebDir, cfg.DataDir)
	terminal := NewTerminal()

	// Services. Wake callback is wired below once the runtime registry is
	// built — using a setter avoids reordering the rest of bootstrap and
	// lets the closure capture `s` so it always sees the latest registry.
	msgSvc := services.NewMessagingService(
		fileStore.Messages,
		nil,
		func(id string) *services.AgentInfo {
			a := state.GetAgent(id)
			if a == nil {
				return nil
			}
			return &services.AgentInfo{
				State: a.State, IsAlive: a.IsAlive,
				LastUpdated: a.LastUpdated, TTY: a.TTY,
				ITermSessionID: a.ITermSessionID,
			}
		},
		terminal.IsSessionFocused,
	)

	// Search service
	searchSvc, searchErr := services.NewSearchService(cfg.SearchDBPath, cfg.ClaudeProjectDir, fileStore.Profiles,
		services.ProfileMatcherFunc(func(cwd string) string {
			name, _, _ := state.MatchProfile(cwd)
			return name
		}),
	)
	if searchErr != nil {
		log.Printf("search service unavailable: %v", searchErr)
	}

	sw := store.NewStoreWatcher(cfg.DataDir)
	backendStore := store.NewBackendStore(cfg.DataDir)
	state.SetBackendStore(backendStore)

	s := &Server{
		state:                   state,
		eventBus:                eventBus,
		notifier:                notifier,
		storeWatcher:            sw,
		msgSvc:                  msgSvc,
		searchSvc:               searchSvc,
		terminal:                terminal,
		fileStore:               fileStore,
		dataDir:                 cfg.DataDir,
		backendStore:            backendStore,
		paths:                   NewPathService(cfg.DataDir),
		mux:                     http.NewServeMux(),
		port:                    cfg.Port,
		bindHost:                cfg.BindHost,
		webDir:                  cfg.WebDir,
		usageLog:                NewUsageLogger(cfg.DataDir),
		pendingResumeTaskGroups: make(map[string]string),
	}

	// Phase 3: chat-backed pokegent supervisor.
	s.chatMgr = NewChatManager(cfg.DataDir, func() {
		eventBus.Publish("state_update", state.GetAgents())
	}, eventBus, s.usageLog)
	s.chatMgr.notifyFn = func(pgid, agentState, name, summary string) {
		agent := s.state.GetAgent(pgid)
		if agent == nil {
			return
		}
		agentName := name
		if agentName == "" {
			agentName = agent.DisplayName
		}
		if agentName == "" {
			agentName = agent.ProfileName
		}
		evt := HookEvent{
			SessionID: agent.SessionID,
		}
		if agentState == "done" || agentState == "idle" {
			evt.HookEventName = "Stop"
			evt.LastAssistantMessage = summary
		}
		s.notifier.MaybeNotify(evt, agent)
	}

	// Runtime registry — every backend implements the same interface; HTTP
	// handlers dispatch through this map keyed by `agent.interface`.
	s.runtimes = runtimeRegistry{
		"iterm2": NewITerm2Runtime(state, terminal),
		"chat":   NewChatRuntime(s.chatMgr),
	}

	// Wire the messaging-service wake callback now that the registry exists.
	// Dispatches per-agent based on `agent.Interface` — iterm2 types the
	// trigger phrase into the TTY, chat sends an ACP prompt. This was the
	// missing piece that broke nudges for chat agents.
	msgSvc.SetWake(func(pgid string) error {
		a := state.GetAgent(pgid)
		if a == nil {
			return nil
		}
		rt, err := s.runtimes.For(a.Interface)
		if err != nil {
			return err
		}
		return rt.CheckMessages(context.Background(), pgid)
	})

	s.routes()
	return s, nil
}

func (s *Server) routes() {
	// API routes
	s.mux.HandleFunc("GET /api/sessions", s.handleGetSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/focus", s.handleFocusSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/rename", s.handleRenameSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/sprite", s.handleSetSprite)
	s.mux.HandleFunc("POST /api/sessions/{id}/role", s.handleSetRole)
	s.mux.HandleFunc("POST /api/sessions/{id}/project", s.handleSetProject)
	s.mux.HandleFunc("POST /api/sessions/{id}/task-group", s.handleSetTaskGroup)
	s.mux.HandleFunc("POST /api/sessions/{id}/prompt", s.handleSendPrompt)
	s.mux.HandleFunc("POST /api/sessions/{id}/cancel", s.handleCancelSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/runtime-config", s.handleSetRuntimeConfig)
	s.mux.HandleFunc("POST /api/sessions/{id}/check-messages", s.handleCheckMessages)
	s.mux.HandleFunc("POST /api/sessions/{id}/clone", s.handleCloneSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/shutdown", s.handleShutdownSession)
	s.mux.HandleFunc("POST /api/sessions/{id}/acknowledge", s.handleAcknowledge)
	s.mux.HandleFunc("POST /api/sessions/{id}/debug/force-idle", s.handleDebugForceIdle)
	s.mux.HandleFunc("POST /api/sessions/{id}/debug/respawn", s.handleDebugRespawn)
	s.mux.HandleFunc("POST /api/sessions/{id}/restart-backend", s.handleRestartBackend)
	s.mux.HandleFunc("POST /api/server/restart", s.handleServerRestart)
	s.mux.HandleFunc("GET /api/runtimes", s.handleListRuntimes)
	s.mux.HandleFunc("GET /api/backends", s.handleListBackends)
	s.mux.HandleFunc("POST /api/backends", s.handleUpsertBackend)
	s.mux.HandleFunc("PUT /api/backends/{id}", s.handleUpsertBackend)
	s.mux.HandleFunc("DELETE /api/backends/{id}", s.handleDeleteBackend)
	s.mux.HandleFunc("POST /api/backends/{id}/default", s.handleSetDefaultBackend)
	s.mux.HandleFunc("POST /api/sessions/{id}/switch-backend", s.handleSwitchBackend)
	s.mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	s.mux.HandleFunc("POST /api/setup/apply", s.handleSetupApply)
	s.mux.HandleFunc("POST /api/setup/repair-hooks", s.handleSetupRepairHooks)
	s.mux.HandleFunc("POST /api/setup/repair-mcp", s.handleSetupRepairMCP)
	s.mux.HandleFunc("POST /api/setup/install-launch-agent", s.handleSetupInstallLaunchAgent)
	s.mux.HandleFunc("POST /api/setup/open-at-login", s.handleSetupOpenAtLogin)
	s.mux.HandleFunc("POST /api/setup/preferences", s.handleSetupPreferences)
	s.mux.HandleFunc("POST /api/setup/onboarding/complete", s.handleSetupOnboardingComplete)
	s.mux.HandleFunc("POST /api/setup/defaults/roles", s.handleSetupDefaultRoles)
	s.mux.HandleFunc("POST /api/setup/defaults/project", s.handleSetupDefaultProject)
	s.mux.HandleFunc("POST /api/setup/open-config", s.handleSetupOpenConfig)
	s.mux.HandleFunc("POST /api/setup/open-auth", s.handleSetupOpenAuth)
	s.mux.HandleFunc("POST /api/open-external", s.handleOpenExternal)
	s.mux.HandleFunc("GET /api/dev/diagnostics", s.handleDevDiagnostics)
	s.mux.HandleFunc("GET /api/dev/logs", s.handleDevLogs)
	s.mux.HandleFunc("GET /api/dev/acp/connections", s.handleDevACPConnections)
	s.mux.HandleFunc("POST /api/dev/acp/{id}/close", s.handleDevACPClose)
	s.mux.HandleFunc("POST /api/dev/acp/{id}/force-idle", s.handleDevACPForceIdle)
	s.mux.HandleFunc("POST /api/task-groups/{name}/release", s.handleReleaseTaskGroup)
	s.mux.HandleFunc("GET /api/task-groups/{name}/sessions", s.handleGetTaskGroupSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}/transcript", s.handleGetTranscript)
	s.mux.HandleFunc("GET /api/sessions/{id}/preview", s.handleSessionPreview)
	s.mux.HandleFunc("POST /api/sessions/{id}/image", s.handleUploadImage)
	s.mux.HandleFunc("GET /api/profiles", s.handleGetProfiles)
	s.mux.HandleFunc("GET /api/projects", s.handleGetProjects)
	s.mux.HandleFunc("GET /api/roles", s.handleGetRoles)
	s.mux.HandleFunc("POST /api/profiles/{name}/launch", s.handleLaunchProfile)
	s.mux.HandleFunc("POST /api/launch", s.handleLaunch)
	// Phase 2: unified launch endpoint. Single entry point regardless of interface.
	s.mux.HandleFunc("POST /api/pokegents/launch", s.handleUnifiedLaunch)

	// Chat-only endpoints — WebSocket relay is the primary interface now.
	// SSE streams and permission endpoint removed in Phase 3 (browser owns state).
	s.mux.HandleFunc("GET /api/chat/{id}/ws", s.handleChatWS)
	// Phase 1: prompt queue — inspect pending queued prompts.

	// Phase 4: interface migration — swap an agent's runtime backend without
	// changing identity (pokegent_id, session_id, mailbox all preserved).
	s.mux.HandleFunc("POST /api/sessions/{id}/migrate", s.handleMigrateInterface)
	s.mux.HandleFunc("GET /api/agent-order", s.handleGetAgentOrder)
	s.mux.HandleFunc("PUT /api/agent-order", s.handleSetAgentOrder)
	s.mux.HandleFunc("GET /api/events", s.eventBus.ServeSSE)
	s.mux.HandleFunc("POST /api/events", s.handlePostEvent)
	// Compat: pokegent.sh's resume path reads sprite + pokegent_id by session_id.
	// Thin shim over the new pokegent-centric data.
	s.mux.HandleFunc("GET /api/sessions/{id}/meta", s.handleGetSessionMeta)

	// Pokegent-centric PC box
	s.mux.HandleFunc("GET /api/pokegents/pc-box", s.handleListPokegents)
	s.mux.HandleFunc("GET /api/pokegents/search", s.handleSearchPokegents)
	s.mux.HandleFunc("GET /api/pokegents/{id}", s.handleGetPokegent)
	s.mux.HandleFunc("POST /api/pokegents/{id}/revive", s.handleRevivePokegent)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/messages", s.handleSendMessage)
	s.mux.HandleFunc("POST /api/messages/send", s.handleSendMessageResolved)
	s.mux.HandleFunc("GET /api/messages", s.handleGetMessages)
	s.mux.HandleFunc("GET /api/messages/connections", s.handleGetConnections)
	s.mux.HandleFunc("GET /api/messages/pending/{id}", s.handleGetPending)
	s.mux.HandleFunc("POST /api/messages/deliver/{id}", s.handleDeliverPending)
	s.mux.HandleFunc("POST /api/messages/consume/{id}", s.handleConsumePending)
	s.mux.HandleFunc("GET /api/activity", s.handleGetActivity)
	s.mux.HandleFunc("GET /api/grid-layout", s.handleGetGridLayout)
	s.mux.HandleFunc("PUT /api/grid-layout", s.handleSetGridLayout)
	s.mux.HandleFunc("GET /api/town-mask", s.handleGetTownMask)
	s.mux.HandleFunc("PUT /api/town-mask", s.handleSetTownMask)
	s.mux.HandleFunc("GET /api/grid-profiles", s.handleListGridProfiles)
	s.mux.HandleFunc("GET /api/grid-profiles/{name}", s.handleGetGridProfile)
	s.mux.HandleFunc("PUT /api/grid-profiles/{name}", s.handleSetGridProfile)
	s.mux.HandleFunc("DELETE /api/grid-profiles/{name}", s.handleDeleteGridProfile)
	s.mux.HandleFunc("POST /api/ephemeral", s.handleCreateEphemeral)
	s.mux.HandleFunc("PUT /api/ephemeral/{id}/complete", s.handleCompleteEphemeral)
	s.mux.HandleFunc("DELETE /api/ephemeral/{id}", s.handleDeleteEphemeral)

	// Serve frontend static files
	if s.webDir != "" {
		fs := http.FileServer(http.Dir(s.webDir))
		s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			path := filepath.Join(s.webDir, r.URL.Path)
			isAsset := strings.HasPrefix(r.URL.Path, "/assets/")
			servingIndex := false

			// SPA fallback: serve index.html for unknown paths
			if _, err := os.Stat(path); os.IsNotExist(err) && r.URL.Path != "/" {
				servingIndex = true
			}
			if r.URL.Path == "/" || r.URL.Path == "/index.html" {
				servingIndex = true
			}

			if servingIndex {
				// Never cache index.html — ensures new JS/CSS hashes are picked up
				w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
				http.ServeFile(w, r, filepath.Join(s.webDir, "index.html"))
				return
			}
			if isAsset {
				// Short cache for dev — allows quick iteration without hard refresh
				w.Header().Set("Cache-Control", "public, max-age=10")
			}
			fs.ServeHTTP(w, r)
		})
	}
}

// Start initializes state, starts watcher and search, and listens on the port.
func (s *Server) Start() error {
	// Load initial state
	if err := s.state.LoadAll(); err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}
	log.Printf("loaded %d profiles, %d agents", len(s.state.GetProfiles()), len(s.state.GetAgents()))

	// Start file watcher — single broadcast point for all file changes
	if err := s.storeWatcher.Start(); err != nil {
		log.Printf("watcher failed to start: %v", err)
	}
	go s.watcherLoop()

	// Start search indexer — sync running session metadata after first index build
	if s.searchSvc != nil {
		// Wire pokegent resolver: indexer attributes JSONL → pokegent_id via
		// session_transcripts lookup or the reverse lookup map fallback.
		s.searchSvc.SetPokegentResolver(func(sessionID string) string {
			if pgid := s.searchSvc.GetPokegentIDForSession(sessionID); pgid != "" {
				return pgid
			}
			// Fallback: consult live state (agent currently running with this sid)
			if a := s.state.GetAgent(sessionID); a != nil && a.PokegentID != "" {
				return a.PokegentID
			}
			// Last resort: reverse lookup from Claude session_id → pokegent_id
			if pgid, ok := s.state.GetSessionToPokegent()[sessionID]; ok {
				return pgid
			}
			return ""
		})
		// One-time migration from legacy session_meta → new tables
		s.migratePokegentsIndex()
		s.searchSvc.StartBackgroundIndexer(5*time.Minute, s.syncSessionMetaToSearch)
	}

	// Start transcript poller for live trace updates
	s.startTracePoller()

	// Re-attach orphaned chat agents (dashboard crash recovery). Async so
	// it doesn't block the HTTP server from coming up — chat agents
	// briefly show "connecting" until their fresh ACP backends finish
	// session/load (typically 2-5s per agent).
	go s.reattachChatSessions()

	bindHost := s.bindHost
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bindHost, s.port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.corsMiddleware(s.mux),
	}
	log.Printf("dashboard server listening on http://%s", addr)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// startTracePoller polls transcript files every 2 seconds for busy agents
// to get live thinking/output traces (hooks only fire on tool events, not mid-generation).
func (s *Server) startTracePoller() {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		type pollCacheEntry struct {
			size    int64
			modTime time.Time
			batch   store.BatchResult
		}
		pollCache := make(map[string]pollCacheEntry)
		for range ticker.C {
			// Clean up stale running files, reconcile mismatched session IDs,
			// and transition done→idle after 10 minutes
			if s.state.CleanStale() || s.state.ReconcileRunningFiles() || s.state.TransitionDoneToIdle() {
				s.eventBus.Publish("state_update", s.state.GetAgents())
			}

			agents := s.state.GetAgents()
			changed := false
			for _, a := range agents {
				needsPrompt := a.UserPrompt == ""
				needsSummary := a.State != "busy" && a.LastSummary == "" && a.LastTrace == ""
				needsContext := a.ContextWindow == 0 || (a.State == "busy" && a.ContextTokens == 0)
				needsBusyTrace := a.State == "busy"
				if !needsPrompt && !needsSummary && !needsContext && !needsBusyTrace {
					continue
				}

				transcriptPath := s.state.FindTranscriptPath(a.PokegentID)
				if transcriptPath == "" {
					continue
				}
				info, err := os.Stat(transcriptPath)
				if err != nil {
					delete(pollCache, transcriptPath)
					continue
				}
				var batch store.BatchResult
				if cached, ok := pollCache[transcriptPath]; ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
					if needsBusyTrace {
						// Busy traces only change when the transcript changes.
						continue
					}
					batch = cached.batch
				} else {
					// Single-pass batch extraction (1 file open, 1 bounded tail read, all fields).
					batch = store.BatchExtract(transcriptPath)
					pollCache[transcriptPath] = pollCacheEntry{size: info.Size(), modTime: info.ModTime(), batch: batch}
				}

				// Backfill missing user prompt
				if a.UserPrompt == "" && batch.LastUserPrompt != "" {
					s.state.UpdateUserPrompt(a.PokegentID, batch.LastUserPrompt)
					changed = true
				}
				// Backfill missing last summary
				if a.State != "busy" && a.LastSummary == "" && a.LastTrace == "" && batch.LastSummary != "" {
					s.state.UpdateSummary(a.PokegentID, batch.LastSummary)
					changed = true
				}
				// Update context usage — detect compaction (tokens decrease).
				// Only act on non-zero transcript values; transcript extraction
				// returns 0 for backends whose JSONL doesn't carry usage data
				// (the live ACP usage_update path handles those correctly).
				ctx := batch.ContextUsage
				if ctx.Tokens > 0 && (ctx.Tokens != a.ContextTokens || ctx.Window != a.ContextWindow) {
					if a.ContextTokens > 0 && ctx.Tokens < a.ContextTokens && a.State == "idle" {
						s.state.UpdateSummary(a.PokegentID, "Compacted")
					}
					s.state.UpdateContext(a.PokegentID, ctx.Tokens, ctx.Window)
					changed = true
				}
				// Update window from transcript even when tokens are 0
				if ctx.Tokens == 0 && ctx.Window > 0 && a.ContextWindow != ctx.Window {
					s.state.UpdateContext(a.PokegentID, a.ContextTokens, ctx.Window)
					changed = true
				}
				// Detect Ctrl+C interrupt
				if a.State == "busy" && batch.IsInterrupted {
					s.state.TransitionState(a.PokegentID, "idle", "interrupted")
					changed = true
					continue
				}
				// Update trace and activity feed for busy agents
				if a.State == "busy" && len(a.RecentActions) > 0 {
					if batch.Trace != "" && batch.Trace != a.LastTrace {
						s.state.UpdateTrace(a.PokegentID, batch.Trace)
						changed = true
					}
					if len(batch.ActivityFeed) > 0 {
						feed := make([]ActivityItem, len(batch.ActivityFeed))
						for i, item := range batch.ActivityFeed {
							feed[i] = ActivityItem{Time: item.Time, Type: item.Type, Text: item.Text}
						}
						s.state.UpdateActivityFeed(a.PokegentID, feed)
						changed = true
					}
				}
			}
			if changed {
				s.eventBus.Publish("state_update", s.state.GetAgents())
			}
		}
	}()
}

// watcherLoop consumes FileEvents from the store watcher and broadcasts
// state updates via SSE. This is the SINGLE broadcast point for file changes.
func (s *Server) watcherLoop() {
	ch, cleanup := s.storeWatcher.Subscribe()
	defer cleanup()

	for evt := range ch {
		switch {
		case evt.SessionID == "*":
			// Running directory changed — full reload
			s.state.ReloadRunning()
			s.syncSessionMetaToSearch()
			s.applyPendingResumeTaskGroups()
		case strings.HasPrefix(evt.Path, "status") || strings.HasSuffix(evt.Path, ".json"):
			// Status file changed
			s.state.ReloadStatus(evt.Path)
		}
		s.eventBus.Publish("state_update", s.state.GetAgents())
	}
}

// syncSessionMetaToSearch pushes role/project/task_group/profile from running
// sessions into the search index so metadata survives after SessionEnd cleanup.
func (s *Server) syncSessionMetaToSearch() {
	if s.searchSvc == nil {
		return
	}
	for _, a := range s.state.GetAgents() {
		if a.Ephemeral {
			continue
		}
		s.searchSvc.UpdateSessionMeta(a.SessionID, a.ProfileName, a.Role, a.Project, a.TaskGroup, a.Sprite, a.PokegentID)
		// Also bind session_id → pokegent_id in the new transcripts table so the
		// PC box / resolver can attribute without waiting for the 5-min indexer
		if a.PokegentID != "" && a.SessionID != "" {
			s.searchSvc.UpsertTranscript(services.TranscriptSummary{
				SessionID: a.SessionID,
			}, a.PokegentID)
		}
	}
	// Keep pokegents_meta fresh too (sprite/name edits in the identity store)
	s.upsertIdentitiesToIndex()
}

// migratePokegentsIndex runs the one-time migration from legacy session_meta to
// the new session_transcripts + pokegents_meta tables. Also performs a continuous
// upsert of identity-store data so live sprite/name changes reach the PC box.
func (s *Server) migratePokegentsIndex() {
	if s.searchSvc == nil {
		return
	}
	idents := s.state.GetIdentities()
	snapshots := make([]services.IdentitySnapshot, 0, len(idents))
	for _, id := range idents {
		if id == nil {
			continue
		}
		snapshots = append(snapshots, services.IdentitySnapshot{
			PokegentID:  id.PokegentID,
			DisplayName: id.DisplayName,
			Sprite:      id.Sprite,
			Role:        id.Role,
			Project:     id.Project,
			TaskGroup:   id.TaskGroup,
			ProfileName: id.Profile,
			CreatedAt:   id.CreatedAt,
		})
	}
	// MigrateFromSessionMeta expects pokegent_id → claude_sid format.
	// Invert our sessionToPokegent (claude_sid → pokegent_id) to get pokegent_id → claude_sid.
	sessionToPg := s.state.GetSessionToPokegent()
	pgToSession := make(map[string]string, len(sessionToPg))
	for claudeSID, pgid := range sessionToPg {
		pgToSession[pgid] = claudeSID
	}
	s.searchSvc.MigrateFromSessionMeta(snapshots, pgToSession)
}

// applyPendingResumeTaskGroups re-assigns task groups to sessions resumed from Session Box.
func (s *Server) applyPendingResumeTaskGroups() {
	s.pendingResumeSpriteMu.Lock()
	if len(s.pendingResumeTaskGroups) == 0 {
		s.pendingResumeSpriteMu.Unlock()
		return
	}
	pending := make(map[string]string, len(s.pendingResumeTaskGroups))
	for k, v := range s.pendingResumeTaskGroups {
		pending[k] = v
	}
	s.pendingResumeSpriteMu.Unlock()

	agents := s.state.GetAgents()
	for oldSID, taskGroup := range pending {
		for _, a := range agents {
			if a.SessionID == oldSID || a.PokegentID == oldSID {
				s.state.SetAgentTaskGroup(a.PokegentID, taskGroup)
				s.pendingResumeSpriteMu.Lock()
				delete(s.pendingResumeTaskGroups, oldSID)
				s.pendingResumeSpriteMu.Unlock()
				break
			}
		}
	}
}

// Stop shuts down all background workers and cleanly terminates every
// active chat session. Order matters here:
//  1. httpServer.Shutdown — drain in-flight HTTP requests first so they
//     complete normally rather than 500ing on a closed chat manager.
//  2. chatMgr.CloseAll — kill ACP subprocesses and wait for cmd.Wait
//     goroutines. Without this, ACP processes would survive as PPID=1
//     orphans that the next dashboard couldn't re-attach to via stdio.
//  3. storeWatcher.Stop — only after chat goroutines stop publishing
//     state_updates that consumers may have already disconnected from.
//  4. searchSvc.Close — last because indexer reads from running state.
func (s *Server) Stop() {
	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(ctx)
	}
	if s.chatMgr != nil {
		log.Printf("shutdown: closing all chat sessions")
		s.chatMgr.CloseAll(5 * time.Second)
	}
	s.storeWatcher.Stop()
	if s.searchSvc != nil {
		s.searchSvc.Close()
	}
}

// --- middleware ---

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !isAllowedLocalOrigin(origin) {
				if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
					http.Error(w, "forbidden origin", http.StatusForbidden)
					return
				}
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAllowedLocalOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// --- handlers ---

func (s *Server) handleGetSessions(w http.ResponseWriter, r *http.Request) {
	agents := s.state.GetAgents()
	writeJSON(w, agents)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent := s.state.GetAgent(id)
	if agent == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, agent)
}

func (s *Server) handleCreateEphemeral(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AgentID         string `json:"agent_id"`
		AgentType       string `json:"agent_type"`
		ParentSessionID string `json:"parent_session_id"`
		Description     string `json:"description"`
		Prompt          string `json:"prompt"`
		CWD             string `json:"cwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.AgentID == "" {
		http.Error(w, "agent_id required", http.StatusBadRequest)
		return
	}

	ea := EphemeralAgent{
		AgentID:         req.AgentID,
		AgentType:       req.AgentType,
		ParentSessionID: req.ParentSessionID,
		Description:     req.Description,
		Prompt:          req.Prompt,
		State:           "running",
		CWD:             req.CWD,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.state.CreateEphemeral(ea); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.eventBus.Publish("ephemeral_start", map[string]any{
		"agent_id":   ea.AgentID,
		"agent_type": ea.AgentType,
		"parent":     ea.ParentSessionID,
	})
	writeJSON(w, map[string]string{"status": "ok", "agent_id": ea.AgentID})
}

func (s *Server) handleCompleteEphemeral(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	var req struct {
		LastMessage    string `json:"last_message"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.state.CompleteEphemeral(agentID, req.LastMessage, req.TranscriptPath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "ephemeral agent not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	s.eventBus.Publish("ephemeral_stop", map[string]any{
		"agent_id": agentID,
	})
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteEphemeral(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if err := s.state.DeleteEphemeral(agentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleGetSessionMeta is a compat shim for pokegent.sh's resume sprite lookup.
// Resolves a Claude session_id to its owning pokegent via session_transcripts,
// then returns that pokegent's identity fields.
func (s *Server) handleGetSessionMeta(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	// Live agent first
	if agent := s.state.GetAgent(sessionID); agent != nil {
		writeJSON(w, map[string]string{
			"sprite":       agent.Sprite,
			"role":         agent.Role,
			"project":      agent.Project,
			"task_group":   agent.TaskGroup,
			"profile_name": agent.ProfileName,
			"pokegent_id":  agent.PokegentID,
		})
		return
	}
	// Dead agent: session_transcripts → pokegents_meta
	if s.searchSvc != nil {
		if pgid := s.searchSvc.GetPokegentIDForSession(sessionID); pgid != "" {
			if summary, err := s.searchSvc.GetPokegentSummary(pgid); err == nil && summary != nil {
				writeJSON(w, map[string]string{
					"sprite":       summary.Sprite,
					"role":         summary.Role,
					"project":      summary.Project,
					"task_group":   summary.TaskGroup,
					"profile_name": summary.ProfileName,
					"pokegent_id":  summary.PokegentID,
				})
				return
			}
		}
	}
	writeJSON(w, map[string]string{})
}

func (s *Server) handleGetProfiles(w http.ResponseWriter, r *http.Request) {
	profiles := s.state.GetProfiles()
	writeJSON(w, profiles)
}

func (s *Server) handleGetProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.fileStore.Projects.List()
	if err != nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, projects)
}

func (s *Server) handleGetRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.fileStore.Roles.List()
	if err != nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, roles)
}

func (s *Server) handleLaunchProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	profile := s.state.GetProfile(name)
	if profile == nil {
		http.Error(w, "unknown profile", http.StatusNotFound)
		return
	}
	if err := s.terminal.LaunchProfile(LaunchOptions{Profile: name, ITermProfile: profile.ITermProfile}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// handleLaunch accepts any profile string including role@project syntax.
// Unlike handleLaunchProfile, it does not validate against known profiles —
// the shell handles resolution.
func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Profile   string `json:"profile"`
		TaskGroup string `json:"task_group,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Profile == "" {
		http.Error(w, "missing profile", http.StatusBadRequest)
		return
	}
	// Try to look up iTerm profile for tab coloring
	itermProfile := ""
	if p := s.state.GetProfile(body.Profile); p != nil {
		itermProfile = p.ITermProfile
	}
	if err := s.terminal.LaunchProfile(LaunchOptions{
		Profile:      body.Profile,
		ITermProfile: itermProfile,
		TaskGroup:    body.TaskGroup,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleGetAgentOrder(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.state.GetAgentOrder())
}

func (s *Server) handleSetAgentOrder(w http.ResponseWriter, r *http.Request) {
	var order []string
	if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	s.state.SetAgentOrder(order)
	// Broadcast reordered state
	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]bool{"ok": true})
}

// ── Grid Layout Persistence ────────────────────────────────

func (s *Server) gridLayoutPath() string {
	return filepath.Join(s.state.dataDir, "grid-layout.json")
}

func (s *Server) gridProfilesDir() string {
	return filepath.Join(s.state.dataDir, "grid-profiles")
}

func (s *Server) handleGetGridLayout(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.gridLayoutPath())
	if err != nil {
		writeJSON(w, map[string]any{"settings": nil, "layouts": map[string]any{}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) handleSetGridLayout(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(s.gridLayoutPath(), data, 0644); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// Town walkable-mask persistence — stores the user's hand-tuned collision grid
// at ~/.pokegents/town-mask.json. The frontend's debug-mode click-to-toggle
// PUTs here. The file is human-readable so the source `TOWN_MASK` constant in
// TownView.tsx can be updated by hand once the user is happy with the layout.
func (s *Server) townMaskPath() string {
	return filepath.Join(s.state.dataDir, "town-mask.json")
}

func (s *Server) handleGetTownMask(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.townMaskPath())
	if err != nil {
		// New installs should use the checked-in town config rather than an
		// all-walkable generated mask. User saves still override this file at
		// ~/.pokegents/town-mask.json.
		defaultPath := filepath.Join(resolvePokegentsRoot(), "dashboard", "defaults", "town-mask.json")
		data, err = os.ReadFile(defaultPath)
		if err != nil {
			// 204 = "no saved/default mask, frontend should use its hardcoded default"
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) handleSetTownMask(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(s.townMaskPath(), data, 0644); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleListGridProfiles(w http.ResponseWriter, r *http.Request) {
	dir := s.gridProfilesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, map[string]any{"profiles": []string{}})
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	writeJSON(w, map[string]any{"profiles": names})
}

func (s *Server) handleGetGridProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	path := filepath.Join(s.gridProfilesDir(), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (s *Server) handleSetGridProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dir := s.gridProfilesDir()
	os.MkdirAll(dir, 0755)
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), data, 0644); err != nil {
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteGridProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	os.Remove(filepath.Join(s.gridProfilesDir(), name+".json"))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handlePostEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var evt HookEvent
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	agent := s.state.UpdateFromEvent(evt)

	// Broadcast full state to SSE clients (single source of truth)
	s.eventBus.Publish("state_update", s.state.GetAgents())

	// Maybe send macOS notification
	s.notifier.MaybeNotify(evt, agent)

	// When an agent transitions to done/idle, nudge if pending messages exist
	if agent != nil && (agent.State == "idle" || agent.State == "done") {
		s.msgSvc.NudgeIfPending(s.resolveToPokegentID(evt.SessionID))
	}

	writeJSON(w, map[string]bool{"ok": true})
}

// ── Pokegent-centric PC box handlers ────────────────────────

func (s *Server) handleListPokegents(w http.ResponseWriter, r *http.Request) {
	if s.searchSvc == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}
	// Oversample so alive-agent filtering doesn't undercut the visible count.
	fetchLimit := limit + 50
	list, err := s.searchSvc.ListPokegents(fetchLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	alive := make(map[string]bool)
	for _, a := range s.state.GetAgents() {
		if a.PokegentID != "" {
			alive[a.PokegentID] = true
		}
	}
	filtered := make([]services.PokegentSummary, 0, len(list))
	for _, p := range list {
		if alive[p.PokegentID] {
			continue
		}
		filtered = append(filtered, p)
		if len(filtered) >= limit {
			break
		}
	}
	writeJSON(w, map[string]any{"pokegents": filtered})
}

func (s *Server) handleSearchPokegents(w http.ResponseWriter, r *http.Request) {
	if s.searchSvc == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, total, err := s.searchSvc.SearchPokegents(q, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"pokegents": list, "total": total})
}

func (s *Server) handleGetPokegent(w http.ResponseWriter, r *http.Request) {
	if s.searchSvc == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	pgid := r.PathValue("id")
	summary, err := s.searchSvc.GetPokegentSummary(pgid)
	if err != nil || summary == nil {
		http.Error(w, "pokegent not found", http.StatusNotFound)
		return
	}
	writeJSON(w, summary)
}

// handleRevivePokegent spawns a fresh Claude session under the given pokegent_id,
// resuming the most recent transcript.
func (s *Server) handleRevivePokegent(w http.ResponseWriter, r *http.Request) {
	if s.searchSvc == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	pgid := r.PathValue("id")
	summary, err := s.searchSvc.GetPokegentSummary(pgid)
	if err != nil || summary == nil {
		http.Error(w, "pokegent not found", http.StatusNotFound)
		return
	}
	if summary.LatestSession.SessionID == "" {
		http.Error(w, "no transcripts to resume", http.StatusBadRequest)
		return
	}

	profileName := summary.ProfileName
	role := summary.Role
	project := summary.Project
	pokegentTarget := profileName
	if role != "" && project != "" {
		pokegentTarget = role + "@" + project
	} else if project != "" {
		pokegentTarget = "@" + project
	}
	if pokegentTarget == "" {
		http.Error(w, "pokegent has no profile/project — cannot determine launch target", http.StatusBadRequest)
		return
	}

	// Stash task group for re-assignment after the resumed session registers
	if summary.TaskGroup != "" {
		s.pendingResumeSpriteMu.Lock()
		s.pendingResumeTaskGroups[summary.LatestSession.SessionID] = summary.TaskGroup
		s.pendingResumeSpriteMu.Unlock()
	}

	// Dispatch by the agent's stored interface. Chat-mode revives spawn a
	// fresh ACP backend resuming the same Claude session_id; iterm2-mode
	// revives open a new iTerm2 tab via pokegent.sh's --resume flow.
	// Without this branch every revive hard-coded to iterm2 — even for
	// agents whose identity says interface=chat — forcing the user to
	// migrate manually after every revive.
	ident, _ := s.fileStore.Agents.Get(pgid)
	wantChat := ident != nil && ident.Interface == "chat"

	if wantChat {
		// Pre-write running file so the dashboard sees the agent immediately.
		// chatMgr.Launch's patchRunningFileChat updates fields in-place; if
		// the file's missing it's a no-op and the agent stays invisible.
		// Pull Model/Effort from identity when present so they survive
		// revive (relaunchChatSession then defaults to role/project config
		// if these are still empty).
		rsModel, rsEffort := "", ""
		rsBackend := ""
		if ident != nil {
			rsModel = ident.Model
			rsEffort = ident.Effort
			rsBackend = ident.AgentBackend
		}
		if rsBackend == "" {
			rsBackend = inferChatBackendFromTranscript(summary.LatestSession.SessionID)
		}
		transcriptPath := store.FindTranscriptPath(summary.LatestSession.SessionID, s.state.claudeProjectDir)
		rs := store.RunningSession{
			Profile:                summary.ProfileName,
			PokegentID:             pgid,
			SessionID:              summary.LatestSession.SessionID,
			DisplayName:            summary.DisplayName,
			Sprite:                 summary.Sprite,
			Role:                   summary.Role,
			Project:                summary.Project,
			TaskGroup:              summary.TaskGroup,
			Model:                  rsModel,
			Effort:                 rsEffort,
			Interface:              "chat",
			AgentBackend:           rsBackend,
			TranscriptPath:         transcriptPath,
			LastGoodSessionID:      summary.LatestSession.SessionID,
			LastGoodTranscriptPath: transcriptPath,
		}
		if _, err := writePlaceholderRunningFile(filepath.Join(s.dataDir, "running"), rs); err != nil {
			http.Error(w, "pre-write running file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.relaunchChatSession(rs); err != nil {
			// Roll back the placeholder file on launch failure.
			path := filepath.Join(s.dataDir, "running",
				fmt.Sprintf("%s-%s.json", rs.Profile, rs.PokegentID))
			_ = os.Remove(path)
			http.Error(w, fmt.Sprintf("chat revive failed: %v", err), http.StatusInternalServerError)
			return
		}
		s.eventBus.Publish("state_update", s.state.GetAgents())
		writeJSON(w, map[string]bool{"ok": true})
		return
	}

	compact := r.URL.Query().Get("compact")
	if err := s.terminal.ResumePokegent(pokegentTarget, summary.LatestSession.SessionID, pgid, compact); err != nil {
		http.Error(w, fmt.Sprintf("failed to open terminal: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// upsertIdentitiesToIndex refreshes pokegents_meta from the in-memory identity store.
// Called before PC box reads so sprite/name changes are visible immediately.
func (s *Server) upsertIdentitiesToIndex() {
	if s.searchSvc == nil {
		return
	}
	idents := s.state.GetIdentities()
	snapshots := make([]services.IdentitySnapshot, 0, len(idents))
	for _, id := range idents {
		if id == nil || id.PokegentID == "" {
			continue
		}
		snapshots = append(snapshots, services.IdentitySnapshot{
			PokegentID:  id.PokegentID,
			DisplayName: id.DisplayName,
			Sprite:      id.Sprite,
			Role:        id.Role,
			Project:     id.Project,
			TaskGroup:   id.TaskGroup,
			ProfileName: id.Profile,
			CreatedAt:   id.CreatedAt,
		})
	}
	s.searchSvc.UpsertPokegentsMeta(snapshots)
}

// ── End pokegent-centric handlers ───────────────────────────

func (s *Server) handleFocusSession(w http.ResponseWriter, r *http.Request) {
	agent, rt, done := s.resolveAgentAndRuntime(w, r)
	if done {
		return
	}
	if !rt.Capabilities().CanFocus {
		// Chat agents have no terminal-tab to focus; the dashboard's
		// frontend opens the side ChatPanel itself via a CustomEvent.
		// Return 200 so the frontend's optimistic click handler doesn't
		// surface an error to the user.
		writeJSON(w, map[string]bool{"ok": true, "focusable": false})
		return
	}
	if err := rt.Focus(r.Context(), runtimeAgentID(agent)); err != nil {
		http.Error(w, fmt.Sprintf("failed to focus: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func runtimeAgentID(agent *AgentState) string {
	if agent == nil {
		return ""
	}
	if agent.PokegentID != "" {
		return agent.PokegentID
	}
	return agent.SessionID
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}

	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}

	agent := s.state.GetAgent(pgid)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	// Update the running file
	s.state.RenameAgent(pgid, body.Name)

	// Update iTerm2 tab title — fire and forget
	if agent.ITermSessionID != "" || agent.TTY != "" {
		go s.terminal.SetTabName(agent.ITermSessionID, agent.TTY, body.Name)
	}

	// Persist name to the JSONL transcript so it shows correctly in the
	// "Previous sessions" resume page even after the session ends
	go s.persistCustomTitle(pgid, body.Name)

	// Broadcast updated state
	s.eventBus.Publish("state_update", s.state.GetAgents())

	writeJSON(w, map[string]bool{"ok": true})
}

// persistCustomTitle appends a custom-title entry to the session's JSONL
// transcript and updates the search index.
func (s *Server) persistCustomTitle(pokegentID, name string) {
	path := s.state.FindTranscriptPath(pokegentID)
	if path == "" {
		return
	}

	// Derive the Claude session_id for the search index (transcript filename)
	transcriptSID := pokegentID
	if agent := s.state.GetAgent(pokegentID); agent != nil && agent.SessionID != "" {
		transcriptSID = agent.SessionID
	}

	entry := map[string]string{"type": "custom-title", "customTitle": name}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	f.Write(append(data, '\n'))
	f.Close()

	// Update search index with the transcript's session ID
	if s.searchSvc != nil {
		s.searchSvc.UpdateCustomTitle(transcriptSID, name)
	}
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From    string `json:"from"`
		To      string `json:"to"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	fromPGID := s.resolveToPokegentID(body.From)
	toPGID := s.resolveToPokegentID(body.To)

	// Resolve display names
	fromName, toName := s.resolveDisplayName(body.From, fromPGID), s.resolveDisplayName(body.To, toPGID)

	msg, needsNudge, err := s.msgSvc.Send(fromPGID, fromName, toPGID, toName, body.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.eventBus.Publish("new_message", msg)
	if conns, err := s.msgSvc.GetConnections(); err == nil {
		s.eventBus.Publish("connections_update", conns)
	}
	if needsNudge {
		s.msgSvc.QueueNudge(toPGID)
	}

	writeJSON(w, msg)
}

// handleSendMessageResolved combines agent resolution + message send in one round-trip.
// Accepts from_hint/to_hint (8-char prefixes or full IDs) and resolves server-side.
func (s *Server) handleSendMessageResolved(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FromHint string `json:"from_hint"`
		ToHint   string `json:"to_hint"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" || body.ToHint == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	fromPGID := s.resolveToPokegentID(body.FromHint)
	toPGID := s.resolveToPokegentID(body.ToHint)

	// Verify the recipient actually exists as a known agent
	toAgent := s.state.GetAgent(toPGID)
	if toAgent == nil {
		http.Error(w, "no agent found matching \""+body.ToHint+"\"", http.StatusNotFound)
		return
	}

	fromName := s.resolveDisplayName(body.FromHint, fromPGID)
	toName := s.resolveDisplayName(body.ToHint, toPGID)

	msg, needsNudge, err := s.msgSvc.Send(fromPGID, fromName, toPGID, toName, body.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.eventBus.Publish("new_message", msg)
	if conns, err := s.msgSvc.GetConnections(); err == nil {
		s.eventBus.Publish("connections_update", conns)
	}
	if needsNudge {
		s.msgSvc.QueueNudge(toPGID)
	}

	writeJSON(w, map[string]any{
		"message":   msg,
		"to_name":   toName,
		"from_name": fromName,
		"to_id":     toPGID,
		"from_id":   fromPGID,
	})
}

// resolveDisplayName returns the display name for a session ID, trying multiple IDs.
func (s *Server) resolveDisplayName(ids ...string) string {
	for _, id := range ids {
		if a := s.state.GetAgent(id); a != nil {
			if a.DisplayName != "" {
				return a.DisplayName
			}
			return a.ProfileName
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	// Collect activity from all project logs
	activityDir := filepath.Join(s.state.dataDir, "activity")
	entries, err := os.ReadDir(activityDir)
	if err != nil {
		writeJSON(w, []any{})
		return
	}

	type ActivityEntry struct {
		Timestamp string `json:"timestamp"`
		SessionID string `json:"session_id"`
		AgentName string `json:"agent_name"`
		Files     string `json:"files"`
		Summary   string `json:"summary"`
		Raw       string `json:"raw"`
	}

	var all []ActivityEntry
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(activityDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				continue
			}
			// Parse: [TIMESTAMP] [SESSION_ID] [AGENT_NAME] FILES — SUMMARY
			entry := ActivityEntry{Raw: line}
			rest := line
			if strings.HasPrefix(rest, "[") {
				if idx := strings.Index(rest, "] "); idx > 0 {
					entry.Timestamp = rest[1:idx]
					rest = rest[idx+2:]
				}
			}
			if strings.HasPrefix(rest, "[") {
				if idx := strings.Index(rest, "] "); idx > 0 {
					entry.SessionID = rest[1:idx]
					rest = rest[idx+2:]
				}
			}
			if strings.HasPrefix(rest, "[") {
				if idx := strings.Index(rest, "] "); idx > 0 {
					entry.AgentName = rest[1:idx]
					rest = rest[idx+2:]
				}
			}
			if dashIdx := strings.Index(rest, " — "); dashIdx >= 0 {
				entry.Files = strings.TrimSpace(rest[:dashIdx])
				entry.Summary = rest[dashIdx+len(" — "):]
			} else {
				entry.Files = strings.TrimSpace(rest)
			}
			// Skip entries with no actual file paths
			if entry.Files == "" || strings.HasPrefix(entry.Files, "—") {
				continue
			}
			all = append(all, entry)
		}
	}

	// Return last N entries
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	writeJSON(w, all)
}

func (s *Server) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	msgs, _ := s.msgSvc.GetHistory()
	writeJSON(w, msgs)
}

func (s *Server) handleGetConnections(w http.ResponseWriter, r *http.Request) {
	conns, _ := s.msgSvc.GetConnections()
	writeJSON(w, conns)
}

func (s *Server) handleGetPending(w http.ResponseWriter, r *http.Request) {
	pgID := s.resolveToPokegentID(r.PathValue("id"))
	msgs, _ := s.msgSvc.GetPending(pgID)
	writeJSON(w, msgs)
}

func (s *Server) handleDeliverPending(w http.ResponseWriter, r *http.Request) {
	pgID := s.resolveToPokegentID(r.PathValue("id"))
	msgs, _ := s.msgSvc.Deliver(pgID)
	writeJSON(w, msgs)
}

func (s *Server) handleConsumePending(w http.ResponseWriter, r *http.Request) {
	pgID := s.resolveToPokegentID(r.PathValue("id"))
	msgs, _ := s.msgSvc.Consume(pgID)
	writeJSON(w, msgs)
}

// resolveSessionID maps any agent ID (pokegent_id, session_id, or prefix)
// to the pokegent_id used as the primary map key.
func (s *Server) resolveSessionID(id string) string {
	// Single pass: check agent IDs per agent
	for _, a := range s.state.GetAgents() {
		for _, candidate := range []string{a.PokegentID, a.SessionID} {
			if candidate != "" && (candidate == id || strings.HasPrefix(candidate, id)) {
				return a.PokegentID
			}
		}
	}
	// Fallback: scan running files (covers stale in-memory state)
	runningDir := filepath.Join(s.state.dataDir, "running")
	entries, err := os.ReadDir(runningDir)
	if err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(runningDir, e.Name()))
			if err != nil {
				continue
			}
			var rf struct {
				SessionID  string `json:"session_id"`
				PokegentID string `json:"pokegent_id"`
			}
			if json.Unmarshal(data, &rf) == nil {
				for _, candidate := range []string{rf.PokegentID, rf.SessionID} {
					if candidate != "" && (candidate == id || strings.HasPrefix(candidate, id)) {
						// Return pokegent_id (primary key)
						pgid := rf.PokegentID
						if pgid == "" {
							pgid = rf.SessionID
						}
						return pgid
					}
				}
			}
		}
	}
	// Last resort: check mailbox directories
	msgDir := filepath.Join(s.state.dataDir, "messages")
	msgEntries, err := os.ReadDir(msgDir)
	if err == nil {
		for _, e := range msgEntries {
			if e.IsDir() && strings.HasPrefix(e.Name(), id) {
				return e.Name()
			}
		}
	}
	return id
}

// resolveToPokegentID maps any agent ID hint (8-char prefix or full UUID) to
// the agent's stable pokegent_id for mailbox routing. pokegent_id is
// backend-agnostic — it survives interface migration and is the only
// identifier the messaging layer should ever use for routing.
//
// Falls back to session_id if no pokegent_id exists. Returns the input
// unchanged if no agent matches (caller decides what to do).
func (s *Server) resolveToPokegentID(id string) string {
	for _, a := range s.state.GetAgents() {
		for _, candidate := range []string{a.PokegentID, a.SessionID} {
			if candidate != "" && (candidate == id || strings.HasPrefix(candidate, id)) {
				if a.PokegentID != "" {
					return a.PokegentID
				}
				return a.SessionID
			}
		}
	}
	return id
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"agents": len(s.state.GetAgents()),
	})
}

// resolveAgentAndRuntime is the boilerplate every runtime-dispatched handler
// shares: hint → pokegent_id → AgentState → Runtime. Returns (nil, nil, true)
// when an HTTP error has already been written.
func (s *Server) resolveAgentAndRuntime(w http.ResponseWriter, r *http.Request) (*AgentState, Runtime, bool) {
	hint := r.PathValue("id")
	pgid := s.resolveToPokegentID(hint)
	agent := s.state.GetAgent(pgid)
	if agent == nil {
		http.Error(w, "agent not found: "+hint, http.StatusNotFound)
		return nil, nil, true
	}
	rt, err := s.runtimes.For(agent.Interface)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, nil, true
	}
	return agent, rt, false
}

func (s *Server) handleSendPrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Prompt string `json:"prompt"`
		Nonce  string `json:"nonce,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Prompt == "" {
		http.Error(w, "missing prompt", http.StatusBadRequest)
		return
	}
	agent, rt, done := s.resolveAgentAndRuntime(w, r)
	if done {
		return
	}
	if err := rt.SendPrompt(r.Context(), agent.PokegentID, body.Prompt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.state.BeginPrompt(agent.PokegentID, body.Prompt)
	if s.eventBus != nil {
		s.eventBus.Publish("state_update", s.state.GetAgents())
	}
	writeJSON(w, map[string]any{"ok": true, "nonce": body.Nonce})
}

func (s *Server) handleCheckMessages(w http.ResponseWriter, r *http.Request) {
	agent, rt, done := s.resolveAgentAndRuntime(w, r)
	if done {
		return
	}
	if err := rt.CheckMessages(r.Context(), agent.PokegentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleCancelSession(w http.ResponseWriter, r *http.Request) {
	agent, rt, done := s.resolveAgentAndRuntime(w, r)
	if done {
		return
	}
	if !rt.Capabilities().CanCancel {
		http.Error(w, "runtime does not support cancel", http.StatusBadRequest)
		return
	}

	if err := rt.Cancel(r.Context(), agent.PokegentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleShutdownSession(w http.ResponseWriter, r *http.Request) {
	agent, rt, done := s.resolveAgentAndRuntime(w, r)
	if done {
		return
	}
	if err := rt.Close(r.Context(), agent.PokegentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Clean up running + status files so the agent doesn't reappear on restart.
	matches, _ := filepath.Glob(filepath.Join(s.dataDir, "running", "*-"+agent.PokegentID+".json"))
	for _, p := range matches {
		_ = os.Remove(p)
	}
	_ = os.Remove(filepath.Join(s.dataDir, "status", agent.PokegentID+".json"))
	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAcknowledge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}
	agent := s.state.GetAgent(pgid)
	if agent == nil || agent.State != "done" {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	s.state.TransitionState(pgid, "idle", "")
	sess := s.chatMgr.Get(pgid)
	if sess != nil {
		sess.smMu.Lock()
		if sess.smState == "done" {
			sess.smState = "idle"
		}
		sess.smMu.Unlock()
		sess.stateMu.Lock()
		sess.writeStatusFileLocked()
		sess.stateMu.Unlock()
		sess.publishAgentStatePatchWith("idle", time.Time{})
	}
	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleServerRestart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	log.Printf("server restart requested via API")
	go func() {
		exe, err := os.Executable()
		if err != nil {
			log.Printf("restart: cannot find executable: %v", err)
			return
		}
		if err := rebuildServerBinary(exe); err != nil {
			log.Printf("restart: rebuild failed: %v", err)
			return
		}
		time.Sleep(500 * time.Millisecond)
		args := os.Args
		env := os.Environ()
		log.Printf("restart: execing %s %v", exe, args)
		if err := syscall.Exec(exe, args, env); err != nil {
			log.Printf("restart: exec failed: %v", err)
		}
	}()
}

func rebuildServerBinary(exe string) error {
	root := resolvePokegentsRoot()
	dashboardDir := filepath.Join(root, "dashboard")
	if root == "" || !fileExists(filepath.Join(dashboardDir, "go.mod")) {
		dashboardDir = filepath.Dir(exe)
	}
	if !fileExists(filepath.Join(dashboardDir, "go.mod")) {
		return fmt.Errorf("cannot locate dashboard source directory for rebuild")
	}
	tmp := filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".new")
	_ = os.Remove(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", tmp, ".")
	cmd.Dir = dashboardDir
	cmd.Env = append(os.Environ(), "CGO_CFLAGS=-DSQLITE_ENABLE_FTS5")
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		_ = os.Remove(tmp)
		return fmt.Errorf("go build timed out")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("go build failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace binary: %w", err)
	}
	log.Printf("restart: rebuilt server binary at %s", exe)
	return nil
}

func (s *Server) handleDebugForceIdle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}
	s.state.TransitionState(pgid, "idle", "")
	sess := s.chatMgr.Get(pgid)
	if sess != nil {
		sess.ForceIdle()
	}
	log.Printf("debug[%s]: forced idle", shortChat(pgid))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleDebugRespawn(w http.ResponseWriter, r *http.Request) {
	s.restartChatBackend(w, r, "debug-respawn")
}

func (s *Server) handleRestartBackend(w http.ResponseWriter, r *http.Request) {
	s.restartChatBackend(w, r, "restart-backend")
}

func (s *Server) restartChatBackend(w http.ResponseWriter, r *http.Request, logPrefix string) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}
	runningGlob := filepath.Join(s.dataDir, "running", "*-"+pgid+".json")
	matches, _ := filepath.Glob(runningGlob)
	if len(matches) == 0 {
		http.Error(w, "no running file for chat backend", http.StatusNotFound)
		return
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rs store.RunningSession
	if err := json.Unmarshal(raw, &rs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rs.Interface != "chat" {
		http.Error(w, "agent is not chat-backed", http.StatusBadRequest)
		return
	}
	s.state.TransitionState(pgid, "reconfiguring", "restarting backend")
	s.eventBus.Publish("state_update", s.state.GetAgents())
	go func() {
		s.chatMgr.Close(pgid)
		time.Sleep(500 * time.Millisecond)
		if err := s.relaunchChatSession(rs); err != nil {
			log.Printf("%s[%s]: failed: %v", logPrefix, shortChat(pgid), err)
			s.state.TransitionState(pgid, "error", "backend restart failed: "+err.Error())
			s.eventBus.Publish("state_update", s.state.GetAgents())
		} else {
			log.Printf("%s[%s]: respawned", logPrefix, shortChat(pgid))
			s.eventBus.Publish("state_update", s.state.GetAgents())
		}
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleListRuntimes(w http.ResponseWriter, _ *http.Request) {
	out := make(map[string]RuntimeCapabilities, len(s.runtimes))
	for name, rt := range s.runtimes {
		out[name] = rt.Capabilities()
	}
	writeJSON(w, out)
}

func (s *Server) handleReleaseTaskGroup(w http.ResponseWriter, r *http.Request) {
	groupName := r.PathValue("name")
	if groupName == "" {
		http.Error(w, "missing group name", http.StatusBadRequest)
		return
	}

	agents := s.state.GetAgentsByTaskGroup(groupName)
	if len(agents) == 0 {
		http.Error(w, "no agents in group", http.StatusNotFound)
		return
	}

	var released []string
	for _, agent := range agents {
		if agent.Ephemeral {
			// Dismiss completed ephemerals immediately
			if agent.State == "idle" {
				s.state.DeleteEphemeral(agent.SessionID)
			}
			continue
		}
		if agent.TTY == "" {
			continue
		}
		itermSID := agent.ITermSessionID
		tty := agent.TTY
		go func() {
			s.terminal.WriteText(itermSID, tty, "/exit")
			time.Sleep(2 * time.Second)
			s.terminal.CloseSession(itermSID, tty)
		}()
		released = append(released, agent.SessionID)
	}

	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]any{"ok": true, "released": released, "count": len(released)})
}

func (s *Server) handleGetTaskGroupSessions(w http.ResponseWriter, r *http.Request) {
	groupName := r.PathValue("name")
	if groupName == "" {
		http.Error(w, "missing group name", http.StatusBadRequest)
		return
	}
	if s.searchSvc == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	results, err := s.searchSvc.SessionsByTaskGroup(groupName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Mark which sessions are currently active
	activeAgents := s.state.GetAgents()
	activeIDs := make(map[string]bool, len(activeAgents))
	for _, a := range activeAgents {
		activeIDs[a.SessionID] = true
		activeIDs[a.PokegentID] = true
	}

	type sessionInfo struct {
		services.SearchResult
		Active bool `json:"active"`
	}
	out := make([]sessionInfo, len(results))
	for i, r := range results {
		out[i] = sessionInfo{SearchResult: r, Active: activeIDs[r.SessionID]}
	}
	writeJSON(w, out)
}

func (s *Server) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	sessionID := s.resolveSessionID(r.PathValue("id"))

	// Read image data (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "missing image field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	// Store uploads in Pokegents-owned data, not a provider-specific cache.
	cacheDir := s.pathService().ImageUploadDir(sessionID)
	os.MkdirAll(cacheDir, 0755)

	// Find next available number
	num := 1
	for {
		if _, err := os.Stat(filepath.Join(cacheDir, fmt.Sprintf("%d.png", num))); os.IsNotExist(err) {
			break
		}
		num++
	}

	imgPath := filepath.Join(cacheDir, fmt.Sprintf("%d.png", num))
	if err := os.WriteFile(imgPath, data, 0644); err != nil {
		http.Error(w, "write error", http.StatusInternalServerError)
		return
	}

	// Return the file path — the agent can read it directly via the Read tool.
	writeJSON(w, map[string]any{"image_num": num, "path": imgPath, "ref": fmt.Sprintf("[Image: %s]", imgPath)})
}

func (s *Server) handleGetTranscript(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sessionID := s.resolveSessionID(id)

	tail := 100
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}
	afterUUID := r.URL.Query().Get("after")

	path := s.state.FindTranscriptPath(sessionID)
	reader := store.NewTranscriptReader(s.state.claudeProjectDir)
	if page, ok := s.mergedMigrationTranscriptPage(reader, sessionID, path, tail, afterUUID); ok {
		writeJSON(w, page)
		return
	}
	if path == "" {
		writeJSON(w, store.TranscriptPage{})
		return
	}
	page := reader.ParseTranscript(path, tail, afterUUID)
	writeJSON(w, page)
}

func (s *Server) mergedMigrationTranscriptPage(reader *store.TranscriptReader, pgid, currentPath string, tail int, afterUUID string) (store.TranscriptPage, bool) {
	if s.fileStore == nil || s.fileStore.Running == nil || pgid == "" {
		return store.TranscriptPage{}, false
	}
	rs, err := s.fileStore.Running.GetByPokegentID(pgid)
	if err != nil || rs == nil || rs.SourceTranscriptPath == "" {
		return store.TranscriptPage{}, false
	}
	sourcePath := rs.SourceTranscriptPath
	if rs.TranscriptPath != "" {
		currentPath = rs.TranscriptPath
	} else if currentPath == "" {
		currentPath = rs.TranscriptPath
	}
	if currentPath != "" && filepath.Clean(currentPath) == filepath.Clean(sourcePath) {
		return store.TranscriptPage{}, false
	}

	sourcePage := reader.ParseTranscript(sourcePath, tail, "")
	var entries []store.TranscriptEntry
	entries = append(entries, sourcePage.Entries...)

	currentHasMore := false
	if currentPath != "" {
		currentPage := reader.ParseTranscript(currentPath, tail, "")
		currentHasMore = currentPage.HasMore
		entries = append(entries, currentPage.Entries...)
	}
	if len(entries) == 0 {
		return store.TranscriptPage{}, false
	}

	if afterUUID != "" {
		found := false
		for i, e := range entries {
			if e.UUID == afterUUID {
				entries = entries[i+1:]
				found = true
				break
			}
		}
		if !found {
			entries = nil
		}
	}

	hasMore := sourcePage.HasMore || currentHasMore
	if tail > 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
		hasMore = true
	}
	return store.TranscriptPage{Entries: entries, HasMore: hasMore}, true
}

func (s *Server) handleSessionPreview(w http.ResponseWriter, r *http.Request) {
	sessionID := s.resolveSessionID(r.PathValue("id"))
	path := s.state.FindTranscriptPath(sessionID)
	if path == "" {
		writeJSON(w, map[string]string{"user_prompt": "", "last_summary": ""})
		return
	}
	userPrompt, lastSummary := extractLastMessages(path)
	writeJSON(w, map[string]string{
		"user_prompt":  userPrompt,
		"last_summary": lastSummary,
	})
}

func (s *Server) handleCloneSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}
	agent := s.state.GetAgent(pgid)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	profileName := agent.ProfileName
	if profileName == "" {
		http.Error(w, "cannot determine profile", http.StatusBadRequest)
		return
	}

	// Chat agents clone entirely through the dashboard — no iTerm involved.
	iface := agent.Interface
	if iface == "" && s.chatMgr.Get(agent.PokegentID) != nil {
		iface = "chat"
	}
	if iface == "" {
		iface = "iterm2"
	}

	if iface == "chat" {
		// Resolve backend, model, effort, system prompt from source agent
		isNonClaude := providerFromBackendKey(agent.AgentBackend) != "claude"
		role := agent.Role
		project := agent.Project
		systemPrompt := s.composeSystemPrompt(LaunchRequest{Role: role, Project: project})
		backendKey := agent.AgentBackend
		backendType := backendKey
		var backendEnv map[string]string
		if backendKey != "" {
			if bc, ok := s.backendStore.Get(backendKey); ok {
				backendEnv = bc.Env
				backendType = bc.Type
			}
		}
		// agent.Model is the display string (e.g. "Codex: GPT 5.5") — strip
		// the provider prefix to get the raw model ID for the backend.
		rawModel := stripModelDisplayPrefix(agent.Model)
		model, effort := s.resolveModelEffortForBackend(rawModel, agent.Effort, role, project, backendKey)
		cwd := agent.CWD
		if cwd == "" {
			home, _ := os.UserHomeDir()
			cwd = home
		}
		contextAgent := *agent
		contextAgent.CWD = cwd

		// Clone context: for all backends, tell the agent it's a clone.
		// For non-Claude, also include recent messages since we can't fork the session.
		cloneCtx := s.buildCloneContext(&contextAgent, isNonClaude)
		if cloneCtx != "" {
			systemPrompt += "\n\n" + cloneCtx
		}
		initialPromptContext := ""
		if isNonClaude {
			initialPromptContext = systemPrompt
		}

		// For Claude: copy JSONL to fork the conversation.
		// For Codex: skip (start fresh — Codex doesn't support session forking).
		var cloneSessionID string
		if !isNonClaude {
			srcJSONL, err := findJSONLForSession(agent.SessionID)
			if err != nil {
				http.Error(w, "cannot find source transcript: "+err.Error(), http.StatusBadRequest)
				return
			}
			csid, err := newPokegentID()
			if err != nil {
				http.Error(w, "failed to mint clone session id: "+err.Error(), http.StatusInternalServerError)
				return
			}
			cloneSessionID = csid
			dstJSONL := filepath.Join(filepath.Dir(srcJSONL), cloneSessionID+".jsonl")
			if err := copyFile(srcJSONL, dstJSONL); err != nil {
				http.Error(w, "failed to copy transcript: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}

		pgid, err := newPokegentID()
		if err != nil {
			http.Error(w, "failed to mint pokegent_id: "+err.Error(), http.StatusInternalServerError)
			return
		}

		cloneName := agent.DisplayName
		if cloneName != "" {
			cloneName += " (clone)"
		} else {
			cloneName = "clone"
		}

		rs := store.RunningSession{
			Profile:      profileName,
			PokegentID:   pgid,
			DisplayName:  cloneName,
			TaskGroup:    agent.TaskGroup,
			Sprite:       agent.Sprite,
			Model:        model,
			Effort:       effort,
			AgentBackend: backendKey,
			Interface:    "chat",
		}
		runningPath, err := writePlaceholderRunningFile(filepath.Join(s.dataDir, "running"), rs)
		if err != nil {
			http.Error(w, "failed to pre-write running file: "+err.Error(), http.StatusInternalServerError)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		if _, err := s.chatMgr.Launch(ctx, ChatLaunchOptions{
			PokegentID:           pgid,
			Profile:              profileName,
			Cwd:                  cwd,
			SystemPromptAppend:   systemPrompt,
			InitialPromptContext: initialPromptContext,
			Model:                model,
			Effort:               effort,
			ResumeSessionID:      cloneSessionID,
			AgentBackend:         backendType,
			BackendConfigKey:     backendKey,
			BackendEnv:           backendEnv,
		}); err != nil {
			_ = os.Remove(runningPath)
			http.Error(w, "chat clone failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		sprite := agent.Sprite
		if sprite == "" {
			sprite = pickDefaultSprite(pgid)
		}
		ident := store.AgentIdentity{
			PokegentID:   pgid,
			DisplayName:  cloneName,
			Sprite:       sprite,
			Role:         role,
			Project:      project,
			Profile:      profileName,
			TaskGroup:    agent.TaskGroup,
			Model:        model,
			Effort:       effort,
			Interface:    "chat",
			AgentBackend: backendKey,
			CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		if err := s.fileStore.Agents.Save(ident); err != nil {
			log.Printf("chat clone: persist identity %s failed: %v", pgid[:8], err)
		}

		s.eventBus.Publish("state_update", s.state.GetAgents())
		writeJSON(w, map[string]any{"ok": true, "pokegent_id": pgid})
		return
	}

	// iterm2: Open a new iTerm2 tab and launch a forked clone via pokegents
	if err := s.terminal.CloneSession(profileName, agent.SessionID[:8]); err != nil {
		http.Error(w, fmt.Sprintf("failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// buildCloneContext extracts the last few conversation messages from an agent's
// transcript and formats them as context for a cloned session. Used for backends
// that don't support session forking (Codex).
func (s *Server) buildCloneContext(agent *AgentState, includeMessages bool) string {
	name := agent.DisplayName
	if name == "" {
		name = agent.PokegentID
	}
	header := fmt.Sprintf(`## Clone Context

You are a newly spawned clone of agent %q.
You are managed by the same pokegents dashboard as the source agent.
Your current working directory is %s.
When asked who you are or whether you are a clone, answer that you are the cloned agent and use this context. Do not say you lack clone context.`, name, agent.CWD)
	if !includeMessages {
		return header
	}

	path := s.state.FindTranscriptPath(agent.PokegentID)
	if path == "" {
		return header
	}
	snapshot, err := s.buildConversationSnapshotFromTranscript(agent.PokegentID, agent.AgentBackend, agent.SessionID, path, agent.CWD)
	if err != nil {
		return header
	}
	if len(snapshot.RecentTurns) == 0 && snapshot.Summary == "" {
		return header
	}
	targetProvider := providerFromBackendKey(agent.AgentBackend)
	ctx := renderSnapshotContext(snapshot, TransitionPurposeClone, targetProvider)
	if ctx.SystemPromptAppend == "" {
		return header
	}
	return header + "\n\n" + ctx.SystemPromptAppend
}

// buildMigrationContext carries recent conversation context across ACP backend
// switches. This is intentionally backend-agnostic: the destination backend
// receives it as instructions/context because it cannot load another backend's
// native transcript format directly.
func (s *Server) buildMigrationContext(agentName, cwd, transcriptPath string) string {
	if agentName == "" {
		agentName = "this agent"
	}
	header := fmt.Sprintf(`## Migrated Conversation Context

You are the same pokegents agent %q after a backend migration.
Your current working directory is %s.
The previous backend transcript is at %s.
Treat the prior conversation below as your own history. Do not say you lack prior history solely because the backend changed.`, agentName, cwd, transcriptPath)
	if transcriptPath == "" {
		return header
	}
	reader := store.NewTranscriptReader(s.state.claudeProjectDir)
	page := reader.ParseTranscript(transcriptPath, 60, "")
	if len(page.Entries) == 0 {
		return header
	}

	lines := []string{header, "Recent prior conversation:"}
	for _, e := range page.Entries {
		switch e.Type {
		case "user":
			text := strings.TrimSpace(e.Content)
			if text == "" {
				continue
			}
			if len(text) > 700 {
				text = text[:700] + "..."
			}
			lines = append(lines, "User: "+text)
		case "assistant":
			var parts []string
			for _, b := range e.Blocks {
				if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
					parts = append(parts, strings.TrimSpace(b.Text))
				}
			}
			if len(parts) == 0 {
				continue
			}
			text := strings.Join(parts, "\n")
			if len(text) > 900 {
				text = text[:900] + "..."
			}
			lines = append(lines, "Assistant: "+text)
		}
	}

	result := strings.Join(lines, "\n\n")
	const maxMigrationContext = 16000
	if len(result) > maxMigrationContext {
		result = header + "\n\nRecent prior conversation (truncated to latest context):\n\n" + result[len(result)-maxMigrationContext:]
	}
	return result
}

func transcriptHasMigrationContext(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if len(data) > 256*1024 {
		data = data[:256*1024]
	}
	return strings.Contains(string(data), "Migrated Conversation Context")
}

func (s *Server) handleSetSprite(w http.ResponseWriter, r *http.Request) {
	sessionID := s.resolveSessionID(r.PathValue("id"))
	var body struct {
		Sprite string `json:"sprite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Sprite == "" {
		http.Error(w, "missing sprite", http.StatusBadRequest)
		return
	}

	if err := s.state.SetAgentSprite(sessionID, body.Sprite); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.eventBus.Publish("state_update", s.state.GetAgents())

	// Update iTerm2 Dynamic Profile icon so the tab icon matches immediately
	s.updateITermSprite(sessionID, body.Sprite)

	writeJSON(w, map[string]bool{"ok": true})
}

// updateITermSprite updates the iTerm2 Dynamic Profile icon for a session.
// iTerm2 watches ~/Library/Application Support/iTerm2/DynamicProfiles/ and
// picks up changes automatically.
func (s *Server) updateITermSprite(sessionID, sprite string) {
	home, _ := os.UserHomeDir()
	dynProfileDir := filepath.Join(home, "Library", "Application Support", "iTerm2", "DynamicProfiles")

	// Dynamic Profile is named by pokegent_id (stable). Fall back to session_id.
	agent := s.state.GetAgent(sessionID)
	dynProfile := ""
	for _, candidate := range []string{
		func() string {
			if agent != nil && agent.PokegentID != "" {
				return agent.PokegentID
			}
			return ""
		}(),
		sessionID,
	} {
		if candidate == "" {
			continue
		}
		p := filepath.Join(dynProfileDir, "pokegents-session-"+candidate+".json")
		if _, err := os.Stat(p); err == nil {
			dynProfile = p
			break
		}
	}
	if dynProfile == "" {
		log.Printf("updateITermSprite: no dynamic profile for %s", sessionID)
		return
	}

	// Read existing profile to preserve Name, Guid, parent
	data, err := os.ReadFile(dynProfile)
	if err != nil {
		return
	}
	var profile map[string]any
	if json.Unmarshal(data, &profile) != nil {
		return
	}

	profiles, ok := profile["Profiles"].([]any)
	if !ok || len(profiles) == 0 {
		return
	}
	p, ok := profiles[0].(map[string]any)
	if !ok {
		return
	}

	// Build absolute path to sprite PNG
	spritePath := filepath.Join(s.webDir, "sprites", sprite+".png")
	if absPath, err := filepath.Abs(spritePath); err == nil {
		spritePath = absPath
	}

	p["Icon"] = 2 // custom icon
	p["Custom Icon Path"] = spritePath
	profiles[0] = p
	profile["Profiles"] = profiles

	updated, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(dynProfile, updated, 0644)
}

func (s *Server) handleSetRole(w http.ResponseWriter, r *http.Request) {
	sessionID := s.resolveSessionID(r.PathValue("id"))
	var body struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Role != "" {
		if _, err := s.fileStore.Roles.Get(body.Role); err != nil {
			http.Error(w, "unknown role: "+body.Role, http.StatusBadRequest)
			return
		}
	}
	s.state.SetAgentRole(sessionID, body.Role)
	s.eventBus.Publish("state_update", s.state.GetAgents())
	status := s.relaunchIfIdle(sessionID)
	writeJSON(w, map[string]string{"status": status, "role": body.Role})
}

func (s *Server) handleSetProject(w http.ResponseWriter, r *http.Request) {
	sessionID := s.resolveSessionID(r.PathValue("id"))
	var body struct {
		Project string `json:"project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Project != "" {
		if _, err := s.fileStore.Projects.Get(body.Project); err != nil {
			http.Error(w, "unknown project: "+body.Project, http.StatusBadRequest)
			return
		}
	}
	s.state.SetAgentProject(sessionID, body.Project)
	s.eventBus.Publish("state_update", s.state.GetAgents())
	status := s.relaunchIfIdle(sessionID)
	writeJSON(w, map[string]string{"status": status, "project": body.Project})
}

func (s *Server) handleSetTaskGroup(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	var body struct {
		TaskGroup string `json:"task_group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	sessionID = s.resolveSessionID(sessionID)
	if err := s.state.SetAgentTaskGroup(sessionID, body.TaskGroup); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.eventBus.Publish("state_update", s.state.GetAgents())
	writeJSON(w, map[string]string{"status": "updated", "task_group": body.TaskGroup})
}

// relaunchIfIdle stops and relaunches an agent with its current role@project.
// Returns "relaunching" (immediate), "queued" (busy), or "updated" (no relaunch needed).
func (s *Server) relaunchIfIdle(sessionID string) string {
	agent := s.state.GetAgent(sessionID)
	if agent == nil || (agent.Role == "" && agent.Project == "") {
		return "updated"
	}

	// If project is empty, use the agent's legacy profile name as project fallback
	// (e.g. assigning role "pm" to a legacy "personal" agent → pm@personal)
	project := agent.Project
	if project == "" {
		project = agent.ProfileName
	}
	target := composeTarget(agent.Role, project, agent.ProfileName)
	// Pass --pokegent-id to preserve identity (sprite, grid position, task group, mailbox)
	// across role/project changes
	pokegentID := agent.PokegentID
	cmd := fmt.Sprintf("pokegent %s -r %s", target, sessionID)
	if pokegentID != "" {
		cmd += fmt.Sprintf(" --pokegent-id %s", pokegentID)
	}

	if agent.State == "idle" {
		if agent.ITermSessionID != "" || agent.TTY != "" {
			go func() {
				s.terminal.WriteText(agent.ITermSessionID, agent.TTY, "/exit")
				time.Sleep(2 * time.Second)
				s.terminal.WriteText(agent.ITermSessionID, agent.TTY, cmd)
			}()
		}
		return "relaunching"
	}

	// Busy — queue for later
	s.state.SetPendingRelaunch(sessionID, cmd)
	return "queued"
}

func composeTarget(role, project, fallback string) string {
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

// handleListBackends returns all configured ACP backends from backends.json.
func (s *Server) handleListBackends(w http.ResponseWriter, _ *http.Request) {
	backends := s.backendStore.List()
	writeJSON(w, sortedBackendResponses(backends, false))
}

// handleSwitchBackend kills the current ACP subprocess and relaunches with a
// different backend, preserving pokegent_id, identity, sprite, etc.
func (s *Server) handleSwitchBackend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}

	var body struct {
		Backend string `json:"backend"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Backend == "" {
		http.Error(w, "missing backend", http.StatusBadRequest)
		return
	}

	// Validate that the backend exists.
	bc, ok := s.backendStore.Get(body.Backend)
	if !ok {
		http.Error(w, "unknown backend: "+body.Backend, http.StatusBadRequest)
		return
	}
	_ = bc
	canonicalBackend := s.backendStore.CanonicalID(body.Backend)

	// Find the running file for this agent.
	runningGlob := filepath.Join(s.dataDir, "running", "*-"+pgid+".json")
	matches, _ := filepath.Glob(runningGlob)
	if len(matches) == 0 {
		http.Error(w, "no running file for agent", http.StatusNotFound)
		return
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rs store.RunningSession
	if err := json.Unmarshal(raw, &rs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rs.Interface != "chat" {
		http.Error(w, "switch-backend only supported for chat agents", http.StatusBadRequest)
		return
	}

	oldBackend := rs.AgentBackend
	oldProvider := providerFromBackendType(oldBackend, s.backendTypeForKey(oldBackend))
	newProvider := providerFromBackendType(body.Backend, s.backendTypeForKey(body.Backend))
	if oldProvider != "" && newProvider != "" && oldProvider != newProvider {
		sourcePath := rs.TranscriptPath
		if sourcePath == "" && rs.LastGoodTranscriptPath != "" {
			if _, err := os.Stat(rs.LastGoodTranscriptPath); err == nil {
				sourcePath = rs.LastGoodTranscriptPath
			}
		}
		if sourcePath == "" {
			sourcePath = s.state.FindTranscriptPath(pgid)
		}
		if sourcePath == "" && rs.SessionID != "" {
			if p, err := findJSONLForSession(rs.SessionID); err == nil {
				sourcePath = p
			}
		}
		if sourcePath != "" {
			if snapshot, err := s.buildConversationSnapshotFromTranscript(pgid, oldBackend, rs.SessionID, sourcePath, rs.CWD); err == nil {
				if err := s.writeConversationSnapshot(snapshot); err != nil {
					log.Printf("switch-backend[%s]: write snapshot failed: %v", shortChat(pgid), err)
				}
			} else {
				log.Printf("switch-backend[%s]: build snapshot failed: %v", shortChat(pgid), err)
			}
			rs.SourceTranscriptPath = sourcePath
			if rs.CWD == "" {
				if cwd, err := extractCwdFromJSONL(sourcePath); err == nil {
					rs.CWD = cwd
				}
			}
			// Provider-native session IDs/transcripts are not loadable across
			// providers. Start a fresh destination session and inject the
			// portable snapshot as handoff context instead.
			rs.SessionID = ""
			rs.TranscriptPath = ""
		}
	}

	// Update the running file with the new backend.
	rs.AgentBackend = canonicalBackend
	rs.Model = resolveModelAlias(bc.ResolvedModel())
	rs.Effort = strings.TrimSpace(bc.ResolvedEffort())
	out, _ := json.MarshalIndent(rs, "", "  ")
	_ = os.WriteFile(matches[0], out, 0o644)
	s.state.TransitionState(pgid, "reconfiguring", "switching backend")
	_ = s.fileStore.Agents.Update(pgid, func(id *store.AgentIdentity) {
		id.AgentBackend = canonicalBackend
	})

	// Close and relaunch in background (same pattern as debug/respawn).
	go func() {
		s.chatMgr.Close(pgid)
		time.Sleep(500 * time.Millisecond)
		if err := s.relaunchChatSession(rs); err != nil {
			log.Printf("switch-backend[%s]: relaunch failed: %v", shortChat(pgid), err)
		} else {
			log.Printf("switch-backend[%s]: relaunched with backend %q", shortChat(pgid), canonicalBackend)
		}
		s.eventBus.Publish("state_update", s.state.GetAgents())
	}()

	writeJSON(w, map[string]any{"ok": true, "backend": canonicalBackend})
}

func inferChatBackendFromTranscript(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	path := store.FindTranscriptPath(sessionID, "")
	if path == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		if part == "codex-homes" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	if strings.Contains(filepath.ToSlash(path), "/.codex/sessions/") {
		return "codex"
	}
	return ""
}

func isNonClaudeTranscriptPath(path string) bool {
	if path == "" {
		return false
	}
	return !strings.Contains(path, string(filepath.Separator)+".claude"+string(filepath.Separator)+"projects"+string(filepath.Separator))
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
