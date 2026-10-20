package server

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pokegents/dashboard/server/store"
)

// reattachChatSessions runs once at dashboard startup to recover chat-mode
// agents whose previous chat backend was orphaned (dashboard crash, ungraceful
// kill, or the user's `kill <dashboard>` flow before the SIGTERM handler
// existed). Without this, chat agents whose `m.sessions` entry was wiped by
// the restart sit forever showing "connecting" because the SSE endpoint
// returns 404.
//
// Algorithm — for each running file with interface=chat:
//  1. Find any orphan ACP subprocesses for the agent's session_id
//     (search by `claude-agent-sdk … --resume <session_id>`) and SIGKILL
//     them along with their `claude-agent-acp` and `npm exec` ancestors.
//  2. Poll until the orphans are actually gone (bounded by
//     reattachOrphanKillTimeout). A fixed sleep is brittle under load.
//  3. Spawn a fresh chat backend via chatMgr.Launch with
//     ResumeSessionID set to the agent's existing Claude session_id.
//     Same JSONL transcript continues; only the ACP wrapper is new.
//
// Runs in a goroutine so a slow `npx` or session/load doesn't block the
// HTTP server from coming up.
func (s *Server) reattachChatSessions() {
	runningDir := filepath.Join(s.dataDir, "running")
	matches, err := filepath.Glob(filepath.Join(runningDir, "*.json"))
	if err != nil {
		return
	}
	type todo struct {
		rs       store.RunningSession
		jsonPath string
	}
	var pending []todo
	for _, f := range matches {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rs store.RunningSession
		if err := json.Unmarshal(raw, &rs); err != nil {
			continue
		}
		if rs.Interface != "chat" {
			continue
		}
		if rs.PokegentID == "" || rs.SessionID == "" {
			continue
		}
		// Already in our session map → nothing to reattach (was migrated
		// during a launch flow that survived this restart).
		if s.chatMgr.Get(rs.PokegentID) != nil {
			continue
		}
		pending = append(pending, todo{rs: rs, jsonPath: f})
	}
	if len(pending) == 0 {
		return
	}
	log.Printf("chat-reattach: %d chat agent(s) need reattachment", len(pending))
	// Touch running files so CleanStale's 30s grace period protects them
	// while we kill orphans and respawn ACP processes.
	now := time.Now()
	for _, t := range pending {
		os.Chtimes(t.jsonPath, now, now)
	}
	for _, t := range pending {
		killOrphanChatBackends(t.rs.SessionID)
	}
	// Wait for the orphans to actually exit (file descriptors released,
	// JSONL locks dropped, etc.) before spawning new ACP processes that
	// would otherwise collide on `session/load`. Bounded so we don't hang
	// startup on a stuck process — if any survive past the timeout, the
	// per-agent relaunch will surface its own error.
	for _, t := range pending {
		waitForOrphansGone(t.rs.SessionID, reattachOrphanKillTimeout)
	}
	for _, t := range pending {
		// If the status file says "busy" but the ACP subprocess is dead
		// (it's being respawned), overwrite state to idle so the agent
		// doesn't appear permanently stuck in busy state during recovery.
		sf, err := s.state.store.Status.Get(t.rs.PokegentID)
		if err == nil && sf != nil && sf.State == "busy" {
			sf.State = "idle"
			sf.Detail = "recovered after restart"
			sf.BusySince = ""
			s.state.store.Status.Upsert(*sf)
			log.Printf("chat-reattach[%s]: status was busy-but-dead, reset to idle",
				shortChat(t.rs.PokegentID))
		}

		// Re-touch before each spawn — the 30s grace period may have
		// expired if earlier spawns took a while.
		os.Chtimes(t.jsonPath, time.Now(), time.Now())
		if err := s.relaunchChatSession(t.rs); err != nil {
			log.Printf("chat-reattach[%s]: relaunch failed: %v", shortChat(t.rs.PokegentID), err)
			continue
		}
		// Touch again after spawn so CleanStale sees a fresh mtime
		os.Chtimes(t.jsonPath, time.Now(), time.Now())
		log.Printf("chat-reattach[%s]: re-spawned chat backend (resumed session %s)",
			shortChat(t.rs.PokegentID), shortChat(t.rs.SessionID))
	}
	s.eventBus.Publish("state_update", s.state.GetAgents())
}

const (
	// reattachOrphanKillTimeout caps how long we'll wait for kill -9'd
	// chat-backend processes to finish exiting before spawning fresh ones.
	reattachOrphanKillTimeout = 5 * time.Second
)

// orphanProcessRegex matches the *exact* claude-agent-sdk CLI flag
// invocation we expect, not just any process with the session_id in its
// argv. Pre-fix `pgrep -f "resume <id>"` matched too aggressively (a `vim`
// editing the JSONL, a `grep <id>` the user just ran, etc.); this is
// scoped to processes whose argv contains both `claude-agent-sdk` AND
// `--resume <session_id>`.
const orphanProcessRegex = `claude-agent-sdk.*--resume `

// killOrphanChatBackends finds and SIGKILLs `claude-agent-sdk` processes
// resuming the given session_id, plus their parent `claude-agent-acp` and
// `npm exec` wrappers (PPID chain). Catches the orphaned process tree left
// by an ungraceful dashboard exit. Re-validates each process's cmdline
// immediately before killing to defeat PID recycling.
func killOrphanChatBackends(claudeSessionID string) {
	if claudeSessionID == "" {
		return
	}
	leafPids := pgrepLeafChatPids(claudeSessionID)
	if len(leafPids) == 0 {
		return
	}
	// Collect the entire process tree (leaf + ancestors). Use a set so we
	// don't kill the same wrapper twice when multiple leaves share a parent.
	toKill := make(map[int]struct{})
	for _, pid := range leafPids {
		toKill[pid] = struct{}{}
		for p := pid; p > 1; {
			ppid := psPPID(p)
			if ppid <= 1 {
				break
			}
			// Only chase parents that look chat-related; don't walk into
			// launchd or the user's shell.
			if !isChatProcess(ppid) {
				break
			}
			toKill[ppid] = struct{}{}
			p = ppid
		}
	}
	for pid := range toKill {
		// Re-validate immediately before killing — PIDs can recycle between
		// the pgrep above and the kill below.
		if !isChatProcess(pid) {
			continue
		}
		_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
	}
}

// pgrepLeafChatPids returns the PIDs of `claude-agent-sdk … --resume <sid>`
// processes (the actual Claude CLI workers, not the wrappers above them).
// We match a tighter pattern than `resume <sid>` to avoid catching a `vim`
// editing the transcript or a `grep <sid>` the user just ran.
func pgrepLeafChatPids(claudeSessionID string) []int {
	out, err := exec.Command("pgrep", "-f", orphanProcessRegex+claudeSessionID).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, s := range strings.Fields(strings.TrimSpace(string(out))) {
		p, err := strconv.Atoi(s)
		if err != nil || p <= 1 {
			continue
		}
		pids = append(pids, p)
	}
	return pids
}

// isChatProcess returns true if the given PID's command line looks like
// part of the chat-backend tree (claude-agent-sdk, claude-agent-acp, or
// `npm exec` invoking the latter). Used as a guard against PID recycling
// and against accidentally killing unrelated tools.
func isChatProcess(pid int) bool {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	cmd := string(out)
	if strings.Contains(cmd, "claude-agent-sdk") {
		return true
	}
	if strings.Contains(cmd, "claude-agent-acp") {
		return true
	}
	if strings.Contains(cmd, "npm exec") && strings.Contains(cmd, "claude-agent-acp") {
		return true
	}
	return false
}

// psPPID returns the parent PID for a given PID, or 0 on lookup failure.
func psPPID(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid
}

// waitForOrphansGone polls until no `claude-agent-sdk … --resume <sid>`
// processes remain, or the timeout elapses. A fixed sleep was brittle —
// under load `npm exec` + the SDK + claude take more than 500ms to fully
// exit after SIGKILL, and `session/load` on the new backend would race
// against the still-open JSONL.
func waitForOrphansGone(claudeSessionID string, timeout time.Duration) {
	if claudeSessionID == "" {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(pgrepLeafChatPids(claudeSessionID)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// resolveModelEffort follows the same precedence pokegent.sh uses for
// iterm2 launches: running-file (launch-time snapshot) wins, then role
// config, then project config. Either field may end up empty if no
// config in the chain provides one. UI display falls back to an explicit
// provider-scoped unknown label instead of pretending a concrete model is known.
const defaultClaudeModelLabel = "Claude: unknown model"
const defaultCodexModelLabel = "Codex: unknown model"

func (s *Server) resolveModelEffort(rsModel, rsEffort, role, project string) (model, effort string) {
	return s.resolveModelEffortForBackend(rsModel, rsEffort, role, project, "claude")
}

func isClaudeBackend(backend string) bool {
	return backend == "" || backend == "claude" || backend == "claude-acp"
}

func isClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func displayModelForBackend(model, backendKey, backendType string) string {
	model = strings.TrimSpace(resolveModelAlias(model))
	if isClaudeBackend(backendKey) || isClaudeBackend(backendType) {
		if model != "" {
			return "Claude: " + friendlyModelName(model)
		}
		return defaultClaudeModelLabel
	}
	if strings.Contains(strings.ToLower(backendKey+" "+backendType), "codex") {
		if model != "" {
			return "Codex: " + friendlyModelName(model)
		}
		return defaultCodexModelLabel
	}
	if model != "" {
		return strings.TrimSpace(backendKey) + ": " + friendlyModelName(model)
	}
	return strings.TrimSpace(backendKey)
}

// stripModelDisplayPrefix removes "Claude: " or "Codex: " prefixes and
// reverses friendlyModelName's "GPT 5.5" → "gpt-5.5" transformation,
// recovering the raw model ID from a display string.
func stripModelDisplayPrefix(display string) string {
	m := strings.TrimSpace(display)
	if m == "" {
		return ""
	}
	if idx := strings.Index(m, ": "); idx >= 0 {
		prefix := strings.ToLower(m[:idx])
		if prefix == "claude" || prefix == "codex" {
			m = strings.TrimSpace(m[idx+2:])
		}
	}
	lower := strings.ToLower(m)
	if strings.HasPrefix(lower, "gpt ") {
		m = "gpt-" + strings.TrimSpace(m[4:])
	}
	return strings.ToLower(m)
}

func friendlyModelName(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return "unknown model"
	}
	lower := strings.ToLower(m)
	if strings.HasPrefix(lower, "claude: ") || strings.HasPrefix(lower, "codex: ") {
		if idx := strings.Index(m, ":"); idx >= 0 {
			m = strings.TrimSpace(m[idx+1:])
			lower = strings.ToLower(m)
		}
	}
	if strings.HasPrefix(lower, "codex [") && strings.HasSuffix(m, "]") {
		m = strings.TrimSpace(m[len("Codex [") : len(m)-1])
		lower = strings.ToLower(m)
	}
	if strings.HasPrefix(lower, "gpt-") {
		return "GPT " + strings.TrimSpace(m[len("gpt-"):])
	}
	if strings.HasPrefix(lower, "gpt ") {
		return "GPT " + strings.TrimSpace(m[len("gpt "):])
	}
	if strings.HasPrefix(lower, "openai") {
		return m
	}
	switch lower {
	case "haiku":
		return "Haiku"
	case "sonnet":
		return "Sonnet"
	case "opus":
		return "Opus"
	}
	if strings.Contains(lower, "haiku") {
		return "Haiku"
	}
	if strings.Contains(lower, "sonnet") {
		return "Sonnet"
	}
	if strings.Contains(lower, "opus") {
		if strings.Contains(lower, "1m") || strings.Contains(lower, "[1m]") {
			return "Opus 1M"
		}
		return "Opus"
	}
	if strings.HasPrefix(lower, "gpt") {
		return "GPT" + strings.TrimSpace(m[len("gpt"):])
	}
	return m
}

func (s *Server) resolveModelEffortForBackend(rsModel, rsEffort, role, project, backendKey string) (model, effort string) {
	model = resolveModelAlias(rsModel)
	effort = rsEffort
	backendType := backendKey
	backendModel := ""
	backendEffort := ""
	if s.backendStore != nil && backendKey != "" {
		if bc, ok := s.backendStore.Get(backendKey); ok {
			backendType = bc.Type
			backendModel = resolveModelAlias(bc.ResolvedModel())
			backendEffort = strings.TrimSpace(bc.ResolvedEffort())
		}
	}
	nonClaude := !isClaudeBackend(backendKey) && !isClaudeBackend(backendType)
	if nonClaude && isClaudeModel(model) {
		model = ""
	}
	if s.fileStore != nil && role != "" {
		if r, err := s.fileStore.Roles.Get(role); err == nil && r != nil {
			if model == "" && r.Model != "" {
				roleModel := resolveModelAlias(r.Model)
				if !nonClaude || !isClaudeModel(roleModel) {
					model = roleModel
				}
			}
			if effort == "" && r.Effort != "" {
				effort = r.Effort
			}
		}
	}
	if s.fileStore != nil && project != "" {
		if p, err := s.fileStore.Projects.Get(project); err == nil && p != nil {
			if model == "" && p.Model != "" {
				projectModel := resolveModelAlias(p.Model)
				if !nonClaude || !isClaudeModel(projectModel) {
					model = projectModel
				}
			}
			if effort == "" && p.Effort != "" {
				effort = p.Effort
			}
		}
	}
	if model == "" && backendModel != "" {
		if !nonClaude || !isClaudeModel(backendModel) {
			model = backendModel
		}
	}
	if effort == "" && backendEffort != "" {
		effort = backendEffort
	}
	return
}

// relaunchChatSession spawns a fresh chat backend for an existing chat-mode
// agent, resuming its Claude session. Same machinery migrateToChat uses on
// the iterm2→chat path, minus the iTerm-tab teardown (there's no iTerm tab
// to close — we're recovering a chat agent).
func (s *Server) relaunchChatSession(rs store.RunningSession) error {
	var cwd string
	backendKey := rs.AgentBackend
	canonicalBackendKey := backendKey
	agentBackend := backendKey
	var backendEnv map[string]string
	if backendKey != "" {
		if bc, ok := s.backendStore.Get(backendKey); ok {
			backendEnv = bc.Env
			agentBackend = bc.Type
		}
		canonicalBackendKey = s.backendStore.CanonicalID(backendKey)
	}
	// Defer per-model env merge until after model resolution (below).
	targetProvider := providerFromBackendType(backendKey, agentBackend)
	isNonClaude := targetProvider != "claude"
	if isNonClaude {
		// Non-Claude backends (Codex, OpenCode) manage their own session
		// storage. The ACP adapter resolves the session ID internally via
		// session/load. We just need the cwd from the running file or identity.
		cwd = rs.CWD
	} else {
		if rs.SessionID != "" {
			jsonlPath, err := findJSONLForSession(rs.SessionID)
			if err != nil {
				return err
			}
			var cwdErr error
			cwd, cwdErr = extractCwdFromJSONL(jsonlPath)
			if cwdErr != nil {
				return cwdErr
			}
		} else {
			// Fresh Claude session from a cross-provider handoff.
			cwd = rs.CWD
		}
	}
	ident, _ := s.fileStore.Agents.Get(rs.PokegentID)
	role, project := "", ""
	if ident != nil {
		role = ident.Role
		project = ident.Project
	}
	systemPrompt := s.composeSystemPrompt(LaunchRequest{Role: role, Project: project})
	var handoffContext ProviderContext
	migrationSourcePath := rs.SourceTranscriptPath
	if migrationSourcePath == "" && rs.PokegentID != "" && isNonClaude {
		// Compatibility for agents that were already switched before
		// source_transcript_path existed. Claude transcripts are named by the
		// stable pokegent id, so this recovers the pre-switch history.
		if p := store.FindTranscriptPath(rs.PokegentID, s.state.claudeProjectDir); p != "" && !isNonClaudeTranscriptPath(p) {
			migrationSourcePath = p
		}
	}
	if migrationSourcePath != "" && migrationSourcePath != rs.TranscriptPath {
		snapshot, err := s.readConversationSnapshot(rs.PokegentID)
		if err != nil || snapshot.SourceTranscriptPath != migrationSourcePath {
			snapshot, err = s.buildConversationSnapshotFromTranscript(rs.PokegentID, rs.AgentBackend, rs.SessionID, migrationSourcePath, cwd)
			if err == nil {
				_ = s.writeConversationSnapshot(snapshot)
			}
		}
		if err == nil && snapshot.SourceProvider != targetProvider {
			handoffContext = renderSnapshotContext(snapshot, TransitionPurposeMigration, targetProvider)
			systemPrompt += "\n\n" + handoffContext.SystemPromptAppend
			if cwd == "" && handoffContext.CWD != "" {
				cwd = handoffContext.CWD
			}
		}
	}

	// Resolve model/effort from running-file → role config → project
	// config (mirrors state.go's rebuildAgents enrichment) so the chat
	// backend gets the same values pokegent.sh resolves for iterm2.
	model, effort := s.resolveModelEffortForBackend(rs.Model, rs.Effort, role, project, rs.AgentBackend)

	// Merge per-model env overrides now that the model is resolved.
	if backendKey != "" {
		if bc, ok := s.backendStore.Get(backendKey); ok {
			backendEnv = bc.ResolvedEnvForModel(model)
		}
	}

	// Persist resolved model back to running file so the dashboard UI
	// shows the correct context window (otherwise null → SDK default 200k).
	if model != rs.Model || effort != rs.Effort || canonicalBackendKey != rs.AgentBackend {
		rs.Model = model
		rs.Effort = effort
		rs.AgentBackend = canonicalBackendKey
		runningGlob := filepath.Join(s.dataDir, "running", "*-"+rs.PokegentID+".json")
		if rfMatches, _ := filepath.Glob(runningGlob); len(rfMatches) > 0 {
			if out, err := json.MarshalIndent(rs, "", "  "); err == nil {
				_ = os.WriteFile(rfMatches[0], out, 0o644)
			}
		}
	}

	resumeID := rs.SessionID
	existingTranscriptPath := rs.TranscriptPath
	if existingTranscriptPath == "" && rs.LastGoodTranscriptPath != "" {
		if _, err := os.Stat(rs.LastGoodTranscriptPath); err == nil {
			existingTranscriptPath = rs.LastGoodTranscriptPath
			if rs.LastGoodSessionID != "" {
				resumeID = rs.LastGoodSessionID
			}
		}
	}
	if isNonClaude && existingTranscriptPath != "" && resumeID != "" && store.FindTranscriptPath(resumeID, "") == "" {
		if pathSessionID := sessionIDFromTranscriptPath(existingTranscriptPath); pathSessionID != "" {
			log.Printf("chat-reattach[%s]: session %s has no transcript; using verified transcript session %s",
				shortChat(rs.PokegentID), shortChat(resumeID), shortChat(pathSessionID))
			resumeID = pathSessionID
		}
	}
	if isNonClaude && rs.TranscriptPath != "" && !isNonClaudeTranscriptPath(rs.TranscriptPath) {
		// A backend switch from Claude to Codex preserves the pokegent_id but
		// must not ask Codex to load Claude's session_id. Start fresh; Launch
		// will patch the running file with Codex's real session id.
		resumeID = ""
	}
	if !isNonClaude && rs.SessionID == "" {
		resumeID = ""
	}
	if migrationSourcePath != "" && rs.TranscriptPath != "" && !transcriptHasMigrationContext(rs.TranscriptPath) {
		// The destination backend already has a session, but it was created
		// before we injected migration context. Start one clean context-aware
		// session instead of resuming the "I have no history" thread forever.
		resumeID = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, err := s.chatMgr.Launch(ctx, ChatLaunchOptions{
		PokegentID:             rs.PokegentID,
		Profile:                rs.Profile,
		Cwd:                    cwd,
		SystemPromptAppend:     systemPrompt,
		InitialPromptContext:   handoffContext.InitialPromptContext,
		Model:                  model,
		Effort:                 effort,
		ResumeSessionID:        resumeID,
		ExistingTranscriptPath: existingTranscriptPath,
		AgentBackend:           agentBackend,
		BackendConfigKey:       canonicalBackendKey,
		BackendEnv:             backendEnv,
	})
	return err
}
