// Package services contains business-logic services that sit between
// HTTP handlers and the store layer.
package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pokegents/dashboard/server/store"

	_ "github.com/mattn/go-sqlite3"
)

// ProfileMatcher can resolve a CWD to a profile name.
type ProfileMatcher interface {
	MatchProfile(cwd string) (name string)
}

// ProfileMatcherFunc adapts a plain function to the ProfileMatcher interface.
type ProfileMatcherFunc func(cwd string) string

func (f ProfileMatcherFunc) MatchProfile(cwd string) string { return f(cwd) }

// SearchResult is returned by Search and RecentSessions.
type SearchResult struct {
	SessionID   string `json:"session_id"`
	ProjectDir  string `json:"project_dir"`
	CustomTitle string `json:"custom_title"`
	ProfileName string `json:"profile_name"`
	Role        string `json:"role,omitempty"`
	Project     string `json:"project,omitempty"`
	TaskGroup   string `json:"task_group,omitempty"`
	Sprite      string `json:"sprite,omitempty"`
	PokegentID  string `json:"pokegent_id,omitempty"`
	Snippet     string `json:"snippet"`
	CWD         string `json:"cwd"`
	GitBranch   string `json:"git_branch"`
}

// SearchResponse wraps search results with total count.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
}

// TranscriptSummary is a per-conversation entry under a pokegent.
type TranscriptSummary struct {
	SessionID    string  `json:"session_id"`
	StartedAt    string  `json:"started_at"`
	LastModified float64 `json:"last_modified"`
	CustomTitle  string  `json:"custom_title"`
	FirstUserMsg string  `json:"first_user_msg"`
	ProjectDir   string  `json:"project_dir"`
	GitBranch    string  `json:"git_branch"`
	CWD          string  `json:"cwd"`
	Snippet      string  `json:"snippet,omitempty"`
}

// PokegentSummary is one row in the PC box — an agent (pokegent_id) with its latest transcript.
type PokegentSummary struct {
	PokegentID        string              `json:"pokegent_id"`
	DisplayName       string              `json:"display_name"`
	Sprite            string              `json:"sprite"`
	Role              string              `json:"role,omitempty"`
	Project           string              `json:"project,omitempty"`
	TaskGroup         string              `json:"task_group,omitempty"`
	ProfileName       string              `json:"profile_name,omitempty"`
	CreatedAt         string              `json:"created_at,omitempty"`
	LastActiveAt      float64             `json:"last_active_at"`
	ConversationCount int                 `json:"conversation_count"`
	LatestSession     TranscriptSummary   `json:"latest_session"`
	Transcripts       []TranscriptSummary `json:"transcripts,omitempty"`
}

// SearchService manages the SQLite FTS5 search index over session transcripts.
type SearchService struct {
	mu               sync.Mutex
	db               *sql.DB
	claudeProjectDir string
	profiles         store.ProfileStore
	profileMatcher   ProfileMatcher
	done             chan struct{}

	// pokegentResolver maps a Claude session_id → pokegent_id. Supplied by the
	// caller (usually the state manager) so the indexer can attribute transcripts
	// without reaching into running files / identity store directly.
	pokegentResolver func(sessionID string) string
}

// SetPokegentResolver installs a session_id → pokegent_id resolver. The indexer
// uses it to populate session_transcripts.pokegent_id.
func (ss *SearchService) SetPokegentResolver(fn func(sessionID string) string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.pokegentResolver = fn
}

// NewSearchService creates a search service backed by SQLite FTS5.
func NewSearchService(dbPath, claudeProjectDir string, profiles store.ProfileStore, matcher ProfileMatcher) (*SearchService, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}

	ss := &SearchService{
		db:               db,
		claudeProjectDir: claudeProjectDir,
		profiles:         profiles,
		profileMatcher:   matcher,
		done:             make(chan struct{}),
	}

	if err := ss.createTables(); err != nil {
		db.Close()
		return nil, err
	}

	return ss, nil
}

func (ss *SearchService) createTables() error {
	_, err := ss.db.Exec(`
		CREATE TABLE IF NOT EXISTS session_meta (
			session_id TEXT PRIMARY KEY,
			project_dir TEXT,
			custom_title TEXT,
			first_user_message TEXT,
			last_modified REAL,
			profile_name TEXT,
			cwd TEXT,
			git_branch TEXT
		);

		CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
			session_id UNINDEXED,
			project_dir UNINDEXED,
			custom_title,
			user_messages,
			assistant_messages,
			tokenize='porter unicode61'
		);

		-- New: per-conversation transcript index. Replaces session_meta as the
		-- transcript registry. Attributes each Claude session_id to a pokegent_id.
		CREATE TABLE IF NOT EXISTS session_transcripts (
			session_id    TEXT PRIMARY KEY,
			pokegent_id   TEXT NOT NULL,
			started_at    TEXT,
			last_modified REAL,
			custom_title  TEXT,
			first_user_msg TEXT,
			project_dir   TEXT,
			git_branch    TEXT,
			cwd           TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_transcripts_pgid ON session_transcripts(pokegent_id);
		CREATE INDEX IF NOT EXISTS idx_transcripts_modified ON session_transcripts(last_modified DESC);

		-- New: pokegent-level metadata. Read-through cache of the identity store,
		-- primary lookup for PC box rows and sprite resolution.
		CREATE TABLE IF NOT EXISTS pokegents_meta (
			pokegent_id    TEXT PRIMARY KEY,
			display_name   TEXT,
			sprite         TEXT,
			role           TEXT,
			project        TEXT,
			task_group     TEXT,
			profile_name   TEXT,
			created_at     TEXT,
			last_active_at REAL DEFAULT 0
		);
	`)
	if err != nil {
		return err
	}
	// Migrate: add columns added after initial schema (ignore error if already exists)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN last_user_message TEXT`)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN last_assistant_message TEXT`)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN role TEXT`)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN project TEXT`)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN task_group TEXT`)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN sprite TEXT`)
	ss.db.Exec(`ALTER TABLE session_meta ADD COLUMN pokegent_id TEXT`)
	return nil
}

// BuildIndex scans all JSONL files and indexes new/modified ones.
func (ss *SearchService) BuildIndex() {
	// Intentionally does NOT hold ss.mu — walking 100s of JSONL files would
	// block all read handlers for seconds. Per-row DB writes are safe because
	// sqlite3 serializes at the connection layer.
	indexed := 0
	entries, err := os.ReadDir(ss.claudeProjectDir)
	if err != nil {
		log.Printf("search: cannot read projects dir: %v", err)
	} else {
		for _, dirEntry := range entries {
			if !dirEntry.IsDir() {
				continue
			}
			projDir := filepath.Join(ss.claudeProjectDir, dirEntry.Name())
			files, err := filepath.Glob(filepath.Join(projDir, "*.jsonl"))
			if err != nil {
				continue
			}
			for _, f := range files {
				if ss.indexFileIfNeeded(f, dirEntry.Name()) {
					indexed++
				}
			}
		}
	}
	for _, root := range codexSessionRoots() {
		backendName := "codex"
		parts := strings.Split(filepath.ToSlash(root), "/")
		for i, part := range parts {
			if part == "codex-homes" && i+1 < len(parts) {
				backendName = "codex:" + parts[i+1]
				break
			}
		}
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
				return nil
			}
			if ss.indexFileIfNeeded(path, backendName) {
				indexed++
			}
			return nil
		})
	}

	if indexed > 0 {
		log.Printf("search: indexed %d session files", indexed)
	}
}

func codexSessionRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	roots := []string{filepath.Join(home, ".codex", "sessions")}
	codexHomes, _ := filepath.Glob(filepath.Join(home, ".pokegents", "codex-homes", "*", "sessions"))
	roots = append(roots, codexHomes...)
	out := roots[:0]
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" || seen[root] {
			continue
		}
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			seen[root] = true
			out = append(out, root)
		}
	}
	return out
}

func (ss *SearchService) indexFileIfNeeded(path, projectDir string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	modTime := float64(info.ModTime().UnixMilli()) / 1000.0
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")

	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	var (
		customTitle        string
		firstUserMessage   string
		userMessages       strings.Builder
		assistantMsgs      strings.Builder
		cwd                string
		gitBranch          string
		startedAt          string
		embeddedPokegentID string
	)

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}

		entryType, _ := entry["type"].(string)
		payload, _ := entry["payload"].(map[string]any)

		if cwd == "" {
			if c, ok := entry["cwd"].(string); ok {
				cwd = c
			} else if payload != nil {
				if c, ok := payload["cwd"].(string); ok {
					cwd = c
				}
			}
		}
		if gitBranch == "" {
			if b, ok := entry["gitBranch"].(string); ok {
				gitBranch = b
			} else if payload != nil {
				if git, ok := payload["git"].(map[string]any); ok {
					if b, ok := git["branch"].(string); ok {
						gitBranch = b
					}
				}
			}
		}
		if startedAt == "" {
			if ts, ok := entry["timestamp"].(string); ok {
				startedAt = ts
			}
		}

		switch entryType {
		case "session_meta":
			if payload != nil {
				if id, ok := payload["id"].(string); ok && id != "" {
					sessionID = id
				}
			}
		case "pokegent-id":
			if p, ok := entry["pokegent_id"].(string); ok {
				embeddedPokegentID = p
			}
		case "custom-title":
			if t, ok := entry["customTitle"].(string); ok {
				customTitle = t
			}
		case "user":
			text := extractMessageText(entry)
			if text != "" {
				if firstUserMessage == "" {
					firstUserMessage = truncate(text, 200)
				}
				userMessages.WriteString(text)
				userMessages.WriteString("\n")
			}
		case "assistant":
			text := extractAssistantText(entry)
			if text != "" {
				assistantMsgs.WriteString(text)
				assistantMsgs.WriteString("\n")
			}
		case "response_item":
			if payload == nil {
				continue
			}
			role, _ := payload["role"].(string)
			text := extractCodexPayloadText(payload)
			if text == "" {
				continue
			}
			switch role {
			case "user":
				trimmed := strings.TrimSpace(text)
				if strings.HasPrefix(trimmed, "<") {
					continue
				}
				if firstUserMessage == "" {
					firstUserMessage = truncate(text, 200)
				}
				userMessages.WriteString(text)
				userMessages.WriteString("\n")
			case "assistant":
				assistantMsgs.WriteString(text)
				assistantMsgs.WriteString("\n")
			}
		case "event_msg":
			if payload == nil {
				continue
			}
			msgType, _ := payload["type"].(string)
			msg, _ := payload["message"].(string)
			if msg == "" {
				continue
			}
			switch msgType {
			case "user_message":
				if firstUserMessage == "" {
					firstUserMessage = truncate(msg, 200)
				}
				userMessages.WriteString(msg)
				userMessages.WriteString("\n")
			case "agent_message":
				assistantMsgs.WriteString(msg)
				assistantMsgs.WriteString("\n")
			}
		}
	}

	var existingMod, existingTranscriptMod float64
	metaOK := ss.db.QueryRow("SELECT last_modified FROM session_meta WHERE session_id = ?", sessionID).Scan(&existingMod) == nil
	transcriptOK := ss.db.QueryRow("SELECT last_modified FROM session_transcripts WHERE session_id = ?", sessionID).Scan(&existingTranscriptMod) == nil
	if metaOK && existingMod >= modTime && transcriptOK && existingTranscriptMod >= modTime {
		return false
	}

	profileName := ""
	if cwd != "" && ss.profileMatcher != nil {
		profileName = ss.profileMatcher.MatchProfile(cwd)
	}

	// Preserve role/project/task_group that were synced from running sessions
	var existingRole, existingProject, existingTaskGroup, existingProfile sql.NullString
	ss.db.QueryRow("SELECT role, project, task_group, profile_name FROM session_meta WHERE session_id = ?", sessionID).
		Scan(&existingRole, &existingProject, &existingTaskGroup, &existingProfile)

	ss.db.Exec(`DELETE FROM session_meta WHERE session_id = ?`, sessionID)
	ss.db.Exec(`DELETE FROM sessions_fts WHERE session_id = ?`, sessionID)

	// Use synced values if JSONL-derived profile is empty
	if profileName == "" && existingProfile.Valid {
		profileName = existingProfile.String
	}
	savedRole := existingRole.String
	savedProject := existingProject.String
	savedTaskGroup := existingTaskGroup.String

	ss.db.Exec(`INSERT INTO session_meta (session_id, project_dir, custom_title, first_user_message, last_modified, profile_name, cwd, git_branch, role, project, task_group)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, projectDir, customTitle, firstUserMessage, modTime, profileName, cwd, gitBranch, savedRole, savedProject, savedTaskGroup)

	ss.db.Exec(`INSERT INTO sessions_fts (session_id, project_dir, custom_title, user_messages, assistant_messages)
		VALUES (?, ?, ?, ?, ?)`,
		sessionID, projectDir, customTitle, userMessages.String(), assistantMsgs.String())

	// Attribute transcript to a pokegent_id (preferred: embedded marker;
	// fallback: resolver callback supplied by the state manager)
	pgid := embeddedPokegentID
	if pgid == "" && ss.pokegentResolver != nil {
		pgid = ss.pokegentResolver(sessionID)
	}
	if pgid != "" {
		ss.db.Exec(`
			INSERT INTO session_transcripts
				(session_id, pokegent_id, started_at, last_modified, custom_title, first_user_msg, project_dir, git_branch, cwd)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(session_id) DO UPDATE SET
				pokegent_id    = excluded.pokegent_id,
				started_at     = COALESCE(NULLIF(excluded.started_at, ''), session_transcripts.started_at),
				last_modified  = MAX(excluded.last_modified, session_transcripts.last_modified),
				custom_title   = COALESCE(NULLIF(excluded.custom_title, ''), session_transcripts.custom_title),
				first_user_msg = COALESCE(NULLIF(excluded.first_user_msg, ''), session_transcripts.first_user_msg),
				project_dir    = COALESCE(NULLIF(excluded.project_dir, ''), session_transcripts.project_dir),
				git_branch     = COALESCE(NULLIF(excluded.git_branch, ''), session_transcripts.git_branch),
				cwd            = COALESCE(NULLIF(excluded.cwd, ''), session_transcripts.cwd)
		`, sessionID, pgid, startedAt, modTime, customTitle, firstUserMessage, projectDir, gitBranch, cwd)

		ss.db.Exec(`
			UPDATE pokegents_meta
			SET last_active_at = (SELECT MAX(last_modified) FROM session_transcripts WHERE pokegent_id = ?)
			WHERE pokegent_id = ?
		`, pgid, pgid)
	}

	return true
}

// Search performs a full-text search and returns results.
func (ss *SearchService) Search(query string, limit, offset int) ([]SearchResult, int, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	ftsQuery := query
	if !strings.Contains(query, `"`) {
		words := strings.Fields(query)
		for i, w := range words {
			words[i] = w + "*"
		}
		ftsQuery = strings.Join(words, " ")
	}

	var total int
	err := ss.db.QueryRow(`SELECT COUNT(*) FROM sessions_fts WHERE sessions_fts MATCH ?`, ftsQuery).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("search count failed: %w", err)
	}

	rows, err := ss.db.Query(`
		SELECT
			f.session_id, f.project_dir,
			COALESCE(m.custom_title, ''),
			COALESCE(m.profile_name, ''),
			snippet(sessions_fts, 3, '<mark>', '</mark>', '...', 40) as snippet,
			COALESCE(m.cwd, ''),
			COALESCE(m.git_branch, ''),
			rank,
			COALESCE(m.role, ''),
			COALESCE(m.project, ''),
			COALESCE(m.task_group, ''),
			COALESCE(m.sprite, ''),
			COALESCE(m.pokegent_id, '')
		FROM sessions_fts f
		LEFT JOIN session_meta m ON f.session_id = m.session_id
		WHERE sessions_fts MATCH ?
		ORDER BY rank
		LIMIT ? OFFSET ?
	`, ftsQuery, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("search query failed: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var rank float64
		if err := rows.Scan(&r.SessionID, &r.ProjectDir, &r.CustomTitle, &r.ProfileName, &r.Snippet, &r.CWD, &r.GitBranch, &rank, &r.Role, &r.Project, &r.TaskGroup, &r.Sprite, &r.PokegentID); err != nil {
			continue
		}
		results = append(results, r)
	}

	return results, total, nil
}

// RecentSessions returns the most recently modified sessions.
func (ss *SearchService) RecentSessions(limit int) ([]SearchResult, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if limit <= 0 {
		limit = 20
	}

	rows, err := ss.db.Query(`
		SELECT session_id, project_dir, COALESCE(custom_title, ''),
			   COALESCE(profile_name, ''), COALESCE(first_user_message, ''),
			   COALESCE(cwd, ''), COALESCE(git_branch, ''),
			   COALESCE(role, ''), COALESCE(project, ''),
			   COALESCE(task_group, ''), COALESCE(sprite, ''),
			   COALESCE(pokegent_id, '')
		FROM session_meta
		ORDER BY last_modified DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.SessionID, &r.ProjectDir, &r.CustomTitle, &r.ProfileName, &r.Snippet, &r.CWD, &r.GitBranch, &r.Role, &r.Project, &r.TaskGroup, &r.Sprite, &r.PokegentID); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// SessionsByTaskGroup returns all sessions that belonged to a given task group.
func (ss *SearchService) SessionsByTaskGroup(taskGroup string) ([]SearchResult, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	rows, err := ss.db.Query(`
		SELECT session_id, project_dir, COALESCE(custom_title, ''),
			   COALESCE(profile_name, ''), COALESCE(first_user_message, ''),
			   COALESCE(cwd, ''), COALESCE(git_branch, ''),
			   COALESCE(role, ''), COALESCE(project, ''),
			   COALESCE(task_group, ''), COALESCE(sprite, ''),
			   COALESCE(pokegent_id, '')
		FROM session_meta
		WHERE task_group = ?
		ORDER BY last_modified DESC
	`, taskGroup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.SessionID, &r.ProjectDir, &r.CustomTitle, &r.ProfileName, &r.Snippet, &r.CWD, &r.GitBranch, &r.Role, &r.Project, &r.TaskGroup, &r.Sprite, &r.PokegentID); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// UpdateCustomTitle updates the display name in the search index.
func (ss *SearchService) UpdateCustomTitle(sessionID, title string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.db.Exec("UPDATE session_meta SET custom_title = ? WHERE session_id = ?", title, sessionID)
	ss.db.Exec("UPDATE sessions_fts SET custom_title = ? WHERE session_id = ?", title, sessionID)
}

// UpdateSessionMeta upserts role, project, task_group, and profile_name from
// a running session into the search index. This ensures metadata survives after
// the running file is deleted on SessionEnd.
func (ss *SearchService) UpdateSessionMeta(sessionID, profileName, role, project, taskGroup, sprite, pokegentID string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Ensure row exists (may not yet be indexed from JSONL)
	ss.db.Exec(`INSERT OR IGNORE INTO session_meta (session_id) VALUES (?)`, sessionID)

	// Only update fields that have values — don't overwrite existing data with blanks
	if profileName != "" {
		ss.db.Exec("UPDATE session_meta SET profile_name = ? WHERE session_id = ?", profileName, sessionID)
	}
	if role != "" {
		ss.db.Exec("UPDATE session_meta SET role = ? WHERE session_id = ?", role, sessionID)
	}
	if project != "" {
		ss.db.Exec("UPDATE session_meta SET project = ? WHERE session_id = ?", project, sessionID)
	}
	if taskGroup != "" {
		ss.db.Exec("UPDATE session_meta SET task_group = ? WHERE session_id = ?", taskGroup, sessionID)
	}
	if sprite != "" {
		ss.db.Exec("UPDATE session_meta SET sprite = ? WHERE session_id = ?", sprite, sessionID)
	}
	if pokegentID != "" {
		ss.db.Exec("UPDATE session_meta SET pokegent_id = ? WHERE session_id = ?", pokegentID, sessionID)
	}
}

// GetSessionMeta looks up stored metadata for a session (by session_id or pokegent_id).
func (ss *SearchService) GetSessionMeta(sessionID string) (profileName, role, project, taskGroup, sprite string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// Try by session_id first, then by pokegent_id
	err := ss.db.QueryRow(
		"SELECT COALESCE(profile_name,''), COALESCE(role,''), COALESCE(project,''), COALESCE(task_group,''), COALESCE(sprite,'') FROM session_meta WHERE session_id = ?",
		sessionID,
	).Scan(&profileName, &role, &project, &taskGroup, &sprite)
	if err != nil {
		ss.db.QueryRow(
			"SELECT COALESCE(profile_name,''), COALESCE(role,''), COALESCE(project,''), COALESCE(task_group,''), COALESCE(sprite,'') FROM session_meta WHERE pokegent_id = ?",
			sessionID,
		).Scan(&profileName, &role, &project, &taskGroup, &sprite)
	}
	return
}

// GetProfileName looks up the profile name for a session.
func (ss *SearchService) GetProfileName(sessionID string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var pn string
	ss.db.QueryRow("SELECT profile_name FROM session_meta WHERE session_id = ?", sessionID).Scan(&pn)
	return pn
}

// ── Pokegent-centric index (new data model) ─────────────────

// IdentitySnapshot is a minimal view of an agent identity, supplied by the
// caller so this package doesn't reach into the identity store directly.
type IdentitySnapshot struct {
	PokegentID  string
	DisplayName string
	Sprite      string
	Role        string
	Project     string
	TaskGroup   string
	ProfileName string
	CreatedAt   string
}

// IdentityResolver maps a pokegent_id to its identity data.
type IdentityResolver func(pokegentID string) (IdentitySnapshot, bool)

// UpsertPokegentsMeta replaces the pokegents_meta table with the supplied
// identity snapshots. Safe to call repeatedly — last_active_at is preserved.
func (ss *SearchService) UpsertPokegentsMeta(identities []IdentitySnapshot) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	tx, err := ss.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	for _, id := range identities {
		if id.PokegentID == "" {
			continue
		}
		_, _ = tx.Exec(`
			INSERT INTO pokegents_meta
				(pokegent_id, display_name, sprite, role, project, task_group, profile_name, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(pokegent_id) DO UPDATE SET
				display_name = excluded.display_name,
				sprite       = excluded.sprite,
				role         = excluded.role,
				project      = excluded.project,
				task_group   = excluded.task_group,
				profile_name = excluded.profile_name,
				created_at   = COALESCE(excluded.created_at, pokegents_meta.created_at)
		`, id.PokegentID, id.DisplayName, id.Sprite, id.Role, id.Project, id.TaskGroup, id.ProfileName, id.CreatedAt)
	}
	// Recompute last_active_at from transcripts
	_, _ = tx.Exec(`
		UPDATE pokegents_meta
		SET last_active_at = COALESCE((
			SELECT MAX(last_modified) FROM session_transcripts
			WHERE session_transcripts.pokegent_id = pokegents_meta.pokegent_id
		), 0)
	`)
	tx.Commit()
}

// UpsertTranscript records that a transcript belongs to a pokegent.
func (ss *SearchService) UpsertTranscript(t TranscriptSummary, pokegentID string) {
	if t.SessionID == "" || pokegentID == "" {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	_, _ = ss.db.Exec(`
		INSERT INTO session_transcripts
			(session_id, pokegent_id, started_at, last_modified, custom_title, first_user_msg, project_dir, git_branch, cwd)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			pokegent_id    = excluded.pokegent_id,
			started_at     = COALESCE(NULLIF(excluded.started_at, ''), session_transcripts.started_at),
			last_modified  = MAX(excluded.last_modified, session_transcripts.last_modified),
			custom_title   = COALESCE(NULLIF(excluded.custom_title, ''), session_transcripts.custom_title),
			first_user_msg = COALESCE(NULLIF(excluded.first_user_msg, ''), session_transcripts.first_user_msg),
			project_dir    = COALESCE(NULLIF(excluded.project_dir, ''), session_transcripts.project_dir),
			git_branch     = COALESCE(NULLIF(excluded.git_branch, ''), session_transcripts.git_branch),
			cwd            = COALESCE(NULLIF(excluded.cwd, ''), session_transcripts.cwd)
	`, t.SessionID, pokegentID, t.StartedAt, t.LastModified, t.CustomTitle, t.FirstUserMsg, t.ProjectDir, t.GitBranch, t.CWD)

	_, _ = ss.db.Exec(`
		UPDATE pokegents_meta
		SET last_active_at = (SELECT MAX(last_modified) FROM session_transcripts WHERE pokegent_id = ?)
		WHERE pokegent_id = ?
	`, pokegentID, pokegentID)
}

// GetPokegentIDForSession looks up the pokegent that owns a Claude session_id.
func (ss *SearchService) GetPokegentIDForSession(sessionID string) string {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var pgid string
	ss.db.QueryRow(`SELECT pokegent_id FROM session_transcripts WHERE session_id = ?`, sessionID).Scan(&pgid)
	return pgid
}

// ListPokegents returns all pokegents with their latest transcript, sorted by recency.
func (ss *SearchService) ListPokegents(limit int) ([]PokegentSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// Single query: pokegents_meta LEFT JOIN a subquery that picks the most-recent
	// transcript per pokegent_id, plus a count of transcripts.
	rows, err := ss.db.Query(`
		WITH latest AS (
			SELECT t.* FROM session_transcripts t
			INNER JOIN (
				SELECT pokegent_id, MAX(last_modified) AS mx
				FROM session_transcripts
				GROUP BY pokegent_id
			) agg
			ON t.pokegent_id = agg.pokegent_id AND t.last_modified = agg.mx
		)
		SELECT
			m.pokegent_id,
			COALESCE(m.display_name, ''),
			COALESCE(m.sprite, ''),
			COALESCE(m.role, ''),
			COALESCE(m.project, ''),
			COALESCE(m.task_group, ''),
			COALESCE(m.profile_name, ''),
			COALESCE(m.created_at, ''),
			COALESCE(m.last_active_at, 0),
			COALESCE((SELECT COUNT(*) FROM session_transcripts WHERE pokegent_id = m.pokegent_id), 0),
			COALESCE(latest.session_id, ''),
			COALESCE(latest.started_at, ''),
			COALESCE(latest.last_modified, 0),
			COALESCE(latest.custom_title, ''),
			COALESCE(latest.first_user_msg, ''),
			COALESCE(latest.project_dir, ''),
			COALESCE(latest.git_branch, ''),
			COALESCE(latest.cwd, '')
		FROM pokegents_meta m
		LEFT JOIN latest ON latest.pokegent_id = m.pokegent_id
		ORDER BY m.last_active_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PokegentSummary
	for rows.Next() {
		var p PokegentSummary
		var t TranscriptSummary
		if err := rows.Scan(
			&p.PokegentID, &p.DisplayName, &p.Sprite, &p.Role, &p.Project, &p.TaskGroup,
			&p.ProfileName, &p.CreatedAt, &p.LastActiveAt, &p.ConversationCount,
			&t.SessionID, &t.StartedAt, &t.LastModified, &t.CustomTitle, &t.FirstUserMsg,
			&t.ProjectDir, &t.GitBranch, &t.CWD,
		); err != nil {
			continue
		}
		if t.SessionID != "" {
			p.LatestSession = t
		}
		out = append(out, p)
	}
	return out, nil
}

// latestTranscriptLocked returns the most recent transcript for a pokegent.
// Caller must hold ss.mu.
func (ss *SearchService) latestTranscriptLocked(pgid string) (TranscriptSummary, bool) {
	var t TranscriptSummary
	err := ss.db.QueryRow(`
		SELECT session_id, COALESCE(started_at,''), COALESCE(last_modified,0),
		       COALESCE(custom_title,''), COALESCE(first_user_msg,''),
		       COALESCE(project_dir,''), COALESCE(git_branch,''), COALESCE(cwd,'')
		FROM session_transcripts
		WHERE pokegent_id = ?
		ORDER BY last_modified DESC
		LIMIT 1
	`, pgid).Scan(&t.SessionID, &t.StartedAt, &t.LastModified, &t.CustomTitle, &t.FirstUserMsg, &t.ProjectDir, &t.GitBranch, &t.CWD)
	if err != nil {
		return TranscriptSummary{}, false
	}
	return t, true
}

// TranscriptsForPokegent returns every transcript belonging to a pokegent.
func (ss *SearchService) TranscriptsForPokegent(pgid string) ([]TranscriptSummary, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	rows, err := ss.db.Query(`
		SELECT session_id, COALESCE(started_at,''), COALESCE(last_modified,0),
		       COALESCE(custom_title,''), COALESCE(first_user_msg,''),
		       COALESCE(project_dir,''), COALESCE(git_branch,''), COALESCE(cwd,'')
		FROM session_transcripts
		WHERE pokegent_id = ?
		ORDER BY last_modified DESC
	`, pgid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptSummary
	for rows.Next() {
		var t TranscriptSummary
		if err := rows.Scan(&t.SessionID, &t.StartedAt, &t.LastModified, &t.CustomTitle, &t.FirstUserMsg, &t.ProjectDir, &t.GitBranch, &t.CWD); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// GetPokegentSummary returns a single pokegent's summary with its transcripts populated.
func (ss *SearchService) GetPokegentSummary(pgid string) (*PokegentSummary, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.getPokegentSummaryLocked(pgid)
}

// getPokegentSummaryLocked is the internal implementation — caller must hold ss.mu.
func (ss *SearchService) getPokegentSummaryLocked(pgid string) (*PokegentSummary, error) {
	var p PokegentSummary
	err := ss.db.QueryRow(`
		SELECT pokegent_id, COALESCE(display_name,''), COALESCE(sprite,''),
		       COALESCE(role,''), COALESCE(project,''), COALESCE(task_group,''),
		       COALESCE(profile_name,''), COALESCE(created_at,''), COALESCE(last_active_at,0)
		FROM pokegents_meta WHERE pokegent_id = ?
	`, pgid).Scan(&p.PokegentID, &p.DisplayName, &p.Sprite, &p.Role, &p.Project, &p.TaskGroup,
		&p.ProfileName, &p.CreatedAt, &p.LastActiveAt)
	if err != nil {
		return nil, err
	}
	transcripts := ss.transcriptsForPokegentLocked(pgid)
	p.Transcripts = transcripts
	p.ConversationCount = len(transcripts)
	if len(transcripts) > 0 {
		p.LatestSession = transcripts[0]
	}
	return &p, nil
}

// transcriptsForPokegentLocked is the unlocked internal — caller must hold ss.mu.
func (ss *SearchService) transcriptsForPokegentLocked(pgid string) []TranscriptSummary {
	rows, err := ss.db.Query(`
		SELECT session_id, COALESCE(started_at,''), COALESCE(last_modified,0),
		       COALESCE(custom_title,''), COALESCE(first_user_msg,''),
		       COALESCE(project_dir,''), COALESCE(git_branch,''), COALESCE(cwd,'')
		FROM session_transcripts
		WHERE pokegent_id = ?
		ORDER BY last_modified DESC
	`, pgid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []TranscriptSummary
	for rows.Next() {
		var t TranscriptSummary
		if err := rows.Scan(&t.SessionID, &t.StartedAt, &t.LastModified, &t.CustomTitle, &t.FirstUserMsg, &t.ProjectDir, &t.GitBranch, &t.CWD); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

// SearchPokegents runs a keyword query across transcripts, groups matches by
// pokegent_id, and returns summaries with matching transcripts populated.
func (ss *SearchService) SearchPokegents(query string, limit int) ([]PokegentSummary, int, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	ftsQuery := query
	if !strings.Contains(query, `"`) {
		words := strings.Fields(query)
		for i, w := range words {
			words[i] = w + "*"
		}
		ftsQuery = strings.Join(words, " ")
	}

	rows, err := ss.db.Query(`
		SELECT
			f.session_id,
			snippet(sessions_fts, 3, '<mark>', '</mark>', '...', 40) as snippet,
			t.pokegent_id
		FROM sessions_fts f
		INNER JOIN session_transcripts t ON f.session_id = t.session_id
		WHERE sessions_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, ftsQuery, limit*10)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	type hit struct{ sessionID, snippet string }
	byPgid := map[string][]hit{}
	order := []string{}
	for rows.Next() {
		var sid, snippet, pgid string
		if err := rows.Scan(&sid, &snippet, &pgid); err != nil {
			continue
		}
		if pgid == "" {
			continue
		}
		if _, seen := byPgid[pgid]; !seen {
			order = append(order, pgid)
		}
		byPgid[pgid] = append(byPgid[pgid], hit{sid, snippet})
	}

	out := make([]PokegentSummary, 0, len(order))
	for _, pgid := range order {
		if len(out) >= limit {
			break
		}
		summary, err := ss.getPokegentSummaryLocked(pgid)
		if err != nil {
			continue
		}
		hits := byPgid[pgid]
		// Attach snippets only to matching transcripts, and cap transcripts to matches
		hitMap := map[string]string{}
		for _, h := range hits {
			hitMap[h.sessionID] = h.snippet
		}
		matched := make([]TranscriptSummary, 0, len(hits))
		for _, t := range summary.Transcripts {
			if s, ok := hitMap[t.SessionID]; ok {
				t.Snippet = s
				matched = append(matched, t)
			}
		}
		summary.Transcripts = matched
		if len(matched) > 0 {
			summary.LatestSession.Snippet = matched[0].Snippet
		}
		out = append(out, *summary)
	}
	return out, len(byPgid), nil
}

// MigrateFromSessionMeta backfills session_transcripts + pokegents_meta from
// existing data. Idempotent — skips if session_transcripts already has rows.
// sessionIDMap is the pokegent_id → claude_session_id map used only here as a
// last-resort attribution source.
func (ss *SearchService) MigrateFromSessionMeta(
	identities []IdentitySnapshot,
	sessionIDMap map[string]string,
) {
	ss.mu.Lock()
	var existingCount int
	ss.db.QueryRow("SELECT COUNT(*) FROM session_transcripts").Scan(&existingCount)
	ss.mu.Unlock()

	// Always upsert identity metadata (cheap, identity store is source of truth)
	ss.UpsertPokegentsMeta(identities)

	if existingCount > 0 {
		return
	}
	log.Printf("search: migrating legacy session_meta → session_transcripts + pokegents_meta")

	// Build lookup: claude_sid → pokegent_id from the session-id map.
	// sessionIDMap is pokegent_id → claude_sid.
	claudeToPgid := make(map[string]string, len(sessionIDMap))
	for pokegentID, claudeSID := range sessionIDMap {
		// Last-write wins is fine here — sprites within a cluster were equalized
		// by the one-time identity patch.
		claudeToPgid[claudeSID] = pokegentID
	}

	ss.mu.Lock()
	defer ss.mu.Unlock()

	rows, err := ss.db.Query(`
		SELECT session_id,
		       COALESCE(pokegent_id, ''),
		       COALESCE(project_dir, ''),
		       COALESCE(custom_title, ''),
		       COALESCE(first_user_message, ''),
		       COALESCE(last_modified, 0),
		       COALESCE(cwd, ''),
		       COALESCE(git_branch, '')
		FROM session_meta
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	tx, err := ss.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()

	migrated := 0
	orphaned := 0
	for rows.Next() {
		var (
			sid, pgid, projDir, title, firstMsg, cwd, branch string
			lastMod                                          float64
		)
		if err := rows.Scan(&sid, &pgid, &projDir, &title, &firstMsg, &lastMod, &cwd, &branch); err != nil {
			continue
		}
		if pgid == "" {
			pgid = claudeToPgid[sid]
		}
		if pgid == "" {
			orphaned++
			continue
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO session_transcripts
				(session_id, pokegent_id, last_modified, custom_title, first_user_msg, project_dir, git_branch, cwd)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, sid, pgid, lastMod, title, firstMsg, projDir, branch, cwd)
		if err == nil {
			migrated++
		}
	}
	_, _ = tx.Exec(`
		UPDATE pokegents_meta
		SET last_active_at = COALESCE((
			SELECT MAX(last_modified) FROM session_transcripts
			WHERE session_transcripts.pokegent_id = pokegents_meta.pokegent_id
		), 0)
	`)
	tx.Commit()
	log.Printf("search: migrated %d transcripts (%d orphaned — no pokegent_id attribution)", migrated, orphaned)
}

// ── End pokegent-centric index ──────────────────────────────

// StartBackgroundIndexer re-indexes every interval. Calls onReady after the
// first index build completes (used to sync running session metadata).
func (ss *SearchService) StartBackgroundIndexer(interval time.Duration, onReady ...func()) {
	go func() {
		ss.BuildIndex()
		for _, fn := range onReady {
			fn()
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ss.done:
				return
			case <-ticker.C:
				ss.BuildIndex()
			}
		}
	}()
}

// Close shuts down the search service.
func (ss *SearchService) Close() {
	close(ss.done)
	if ss.db != nil {
		ss.db.Close()
	}
}

// --- text extraction helpers ---

func extractMessageText(entry map[string]any) string {
	msg, ok := entry["message"].(map[string]any)
	if !ok {
		return ""
	}
	content := msg["content"]
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, block := range c {
			if m, ok := block.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func extractAssistantText(entry map[string]any) string {
	msg, ok := entry["message"].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	var texts []string
	for _, block := range content {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if blockType, _ := m["type"].(string); blockType == "text" {
			if t, ok := m["text"].(string); ok {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func extractCodexPayloadText(payload map[string]any) string {
	content := payload["content"]
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, block := range c {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				texts = append(texts, t)
				continue
			}
			if t, ok := m["input_text"].(string); ok {
				texts = append(texts, t)
				continue
			}
			if t, ok := m["output_text"].(string); ok {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	}
	if t, ok := payload["text"].(string); ok {
		return t
	}
	return ""
}

func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}
