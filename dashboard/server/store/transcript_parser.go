package store

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TranscriptParser converts a backend-specific JSONL file into the common
// TranscriptEntry format. Implementations exist for Claude and Codex.
type TranscriptParser interface {
	// Parse reads a transcript file and returns conversation entries.
	// tail: number of entries from the end (0 = all). afterUUID: only return entries after this UUID.
	Parse(path string, tail int, afterUUID string) TranscriptPage

	// ExtractCwd returns the working directory recorded in the transcript.
	ExtractCwd(path string) (string, error)

	// ExtractLastMessages returns the last user prompt and last assistant summary.
	ExtractLastMessages(path string) (userPrompt, lastSummary string)

	// ExtractContextUsage returns token count and context window from the transcript.
	ExtractContextUsage(path string) ContextUsage

	// ExtractTrace returns the last assistant text block (live thinking/output trace).
	ExtractTrace(path string) string

	// ExtractLastUserPrompt returns just the last user message.
	ExtractLastUserPrompt(path string) string

	// ExtractActivityFeed returns an activity timeline from the last turn.
	ExtractActivityFeed(path string) []ActivityItem

	// IsInterrupted checks if the session was interrupted by the user.
	IsInterrupted(path string) bool

	// BatchExtract reads the transcript once and returns all extractable fields.
	// Used by the trace poller to avoid re-opening the file 6 times per tick.
	BatchExtract(path string) BatchResult
}

// BatchResult holds all fields extracted in a single pass over the transcript tail.
type BatchResult struct {
	LastUserPrompt string
	LastSummary    string
	Trace          string
	ContextUsage   ContextUsage
	ActivityFeed   []ActivityItem
	IsInterrupted  bool
}

// ActivityItem represents a single item in an activity feed timeline.
type ActivityItem struct {
	Time string `json:"time"`
	Type string `json:"type"` // "tool", "text", "thinking"
	Text string `json:"text"`
}

// DetectParser reads the first line of a JSONL file and returns the appropriate
// parser implementation. Returns a ClaudeTranscriptParser as fallback.
func DetectParser(path string) TranscriptParser {
	f, err := os.Open(path)
	if err != nil {
		return &ClaudeTranscriptParser{}
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1*1024*1024)
	if !sc.Scan() {
		return &ClaudeTranscriptParser{}
	}
	line := sc.Text()

	var entry struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &entry) != nil {
		return &ClaudeTranscriptParser{}
	}

	switch entry.Type {
	case "session_meta":
		return &CodexTranscriptParser{}
	default:
		// Claude transcripts typically start with "init", "summary", "system", "user", or "assistant"
		return &ClaudeTranscriptParser{}
	}
}

// FindTranscriptPath searches multiple backend storage locations for a session's
// transcript JSONL file. It checks Claude first, then Codex.
func FindTranscriptPath(sessionID string, claudeProjectDir string) string {
	return FindTranscriptPathWithDataDir(sessionID, claudeProjectDir, "")
}

// FindTranscriptPathWithDataDir is FindTranscriptPath with an explicit
// Pokegents data dir for managed provider homes.
func FindTranscriptPathWithDataDir(sessionID string, claudeProjectDir string, dataDir string) string {
	// Try Claude: ~/.claude/projects/*/{sessionID}.jsonl
	if path := findClaudeJSONL(sessionID, claudeProjectDir); path != "" {
		return path
	}
	// Try Codex: ~/.codex/sessions/ and ~/.pokegents/codex-homes/*/sessions/
	if path := findCodexJSONL(sessionID, dataDir); path != "" {
		return path
	}
	return ""
}

// findClaudeJSONL searches Claude's project directories for a session transcript.
func findClaudeJSONL(sessionID, claudeProjectDir string) string {
	if claudeProjectDir == "" {
		return ""
	}
	entries, err := os.ReadDir(claudeProjectDir)
	if err != nil {
		return ""
	}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		path := filepath.Join(claudeProjectDir, d.Name(), sessionID+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// findCodexJSONL searches Codex's session directories for a transcript matching
// the session ID. Codex stores files as: ~/.codex/sessions/YYYY/MM/DD/{name}-{session_id}.jsonl
// The ACP session ID may differ from Codex's internal ID, so we also check
// the session_meta inside each recent file.
func findCodexJSONL(sessionID string, dataDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	suffix := sessionID + ".jsonl"

	searchDirs := []string{filepath.Join(home, ".codex", "sessions")}
	if dataDir == "" {
		dataDir = os.Getenv("POKEGENTS_DATA")
	}
	if dataDir == "" {
		dataDir = filepath.Join(home, ".pokegents")
	}
	codexHomes, _ := filepath.Glob(filepath.Join(dataDir, "codex-homes", "*", "sessions"))
	searchDirs = append(searchDirs, codexHomes...)
	ccsessionHomes, _ := filepath.Glob(filepath.Join(home, ".ccsession", "codex-homes", "*", "sessions"))
	searchDirs = append(searchDirs, ccsessionHomes...)

	var recentFiles []string
	for _, sessionsDir := range searchDirs {
		if _, err := os.Stat(sessionsDir); err != nil {
			continue
		}
		filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
				return nil
			}
			if strings.HasSuffix(info.Name(), suffix) {
				recentFiles = append(recentFiles, path)
				return filepath.SkipAll
			}
			if time.Since(info.ModTime()) < 7*24*time.Hour {
				recentFiles = append(recentFiles, path)
			}
			return nil
		})
	}

	for _, c := range recentFiles {
		if strings.HasSuffix(filepath.Base(c), suffix) {
			return c
		}
	}

	for _, c := range recentFiles {
		f, err := os.Open(c)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
		checked := 0
		for scanner.Scan() && checked < 5 {
			checked++
			var entry struct {
				Type    string `json:"type"`
				Payload struct {
					ID string `json:"id"`
				} `json:"payload"`
			}
			if json.Unmarshal(scanner.Bytes(), &entry) == nil && entry.Payload.ID == sessionID {
				f.Close()
				return c
			}
		}
		f.Close()
	}
	return ""
}
