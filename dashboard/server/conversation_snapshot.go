package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pokegents/dashboard/server/store"
)

const conversationSnapshotSchemaVersion = 1

// ConversationSnapshot is a bounded, provider-agnostic handoff artifact. It is
// not a replacement for provider-native transcripts; it exists so cross-provider
// moves and non-native clones can start a fresh target session with enough
// context to continue coherently.
type ConversationSnapshot struct {
	SchemaVersion        int              `json:"schema_version"`
	PokegentID           string           `json:"pokegent_id"`
	SourceProvider       string           `json:"source_provider"`
	SourceBackendKey     string           `json:"source_backend_key,omitempty"`
	SourceSessionID      string           `json:"source_session_id,omitempty"`
	SourceTranscriptPath string           `json:"source_transcript_path,omitempty"`
	CWD                  string           `json:"cwd,omitempty"`
	CapturedAt           string           `json:"captured_at"`
	Summary              string           `json:"summary,omitempty"`
	RecentTurns          []NormalizedTurn `json:"recent_turns,omitempty"`
	ImportantFiles       []string         `json:"important_files,omitempty"`
	OpenTasks            []string         `json:"open_tasks,omitempty"`
}

type NormalizedTurn struct {
	ID        string                 `json:"id,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"`
	Role      string                 `json:"role"`
	Text      string                 `json:"text,omitempty"`
	ToolCalls []NormalizedToolCall   `json:"tool_calls,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

type NormalizedToolCall struct {
	ID            string         `json:"id,omitempty"`
	Name          string         `json:"name"`
	ProviderName  string         `json:"provider_name,omitempty"`
	Status        string         `json:"status,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	Input         map[string]any `json:"input,omitempty"`
	OutputPreview string         `json:"output_preview,omitempty"`
	Mutation      bool           `json:"mutation,omitempty"`
	Paths         []string       `json:"paths,omitempty"`
	AddedLines    int            `json:"added_lines,omitempty"`
	RemovedLines  int            `json:"removed_lines,omitempty"`
}

type TransitionPurpose string

const (
	TransitionPurposeMigration TransitionPurpose = "migration"
	TransitionPurposeClone     TransitionPurpose = "clone"
	TransitionPurposeRecovery  TransitionPurpose = "recovery"
)

type ProviderContext struct {
	SystemPromptAppend   string
	InitialPromptContext string
	ResumeSessionID      string
	CWD                  string
}

func (s *Server) snapshotPath(pokegentID string) string {
	return filepath.Join(s.dataDir, "snapshots", pokegentID+".json")
}

func (s *Server) writeConversationSnapshot(snapshot ConversationSnapshot) error {
	if snapshot.PokegentID == "" {
		return fmt.Errorf("pokegent_id required")
	}
	dir := filepath.Join(s.dataDir, "snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := s.snapshotPath(snapshot.PokegentID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Server) readConversationSnapshot(pokegentID string) (ConversationSnapshot, error) {
	data, err := os.ReadFile(s.snapshotPath(pokegentID))
	if err != nil {
		return ConversationSnapshot{}, err
	}
	var snapshot ConversationSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return ConversationSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Server) writeRenderedHandoffContext(pokegentID string, purpose TransitionPurpose, content string) (string, error) {
	if pokegentID == "" {
		return "", fmt.Errorf("pokegent_id required")
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("handoff context is empty")
	}
	dir := filepath.Join(s.dataDir, "handoff-context")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.md", pokegentID, purpose))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Server) backendTypeForKey(backendKey string) string {
	if backendKey == "" {
		return "claude"
	}
	if s != nil && s.backendStore != nil {
		if bc, ok := s.backendStore.Get(backendKey); ok {
			return bc.Type
		}
	}
	if backendKey == "codex-acp" || backendKey == "codex" || backendKey == "claude-acp" {
		return backendKey
	}
	if strings.Contains(strings.ToLower(backendKey), "codex") || strings.Contains(strings.ToLower(backendKey), "gpt") {
		return "codex"
	}
	return backendKey
}

func (s *Server) buildConversationSnapshotFromTranscript(pokegentID, backendKey, sessionID, transcriptPath, cwd string) (ConversationSnapshot, error) {
	if transcriptPath == "" {
		return ConversationSnapshot{}, fmt.Errorf("missing source transcript path")
	}
	provider := providerFromTranscriptPath(transcriptPath)
	if provider == "" {
		provider = providerFromBackendKey(backendKey)
	}
	if cwd == "" {
		if extracted, err := extractCwdFromJSONL(transcriptPath); err == nil {
			cwd = extracted
		}
	}
	userPrompt, lastSummary := extractLastMessages(transcriptPath)
	reader := store.NewTranscriptReader(s.state.claudeProjectDir)
	page := reader.ParseTranscript(transcriptPath, 60, "")

	snapshot := ConversationSnapshot{
		SchemaVersion:        conversationSnapshotSchemaVersion,
		PokegentID:           pokegentID,
		SourceProvider:       provider,
		SourceBackendKey:     backendKey,
		SourceSessionID:      sessionID,
		SourceTranscriptPath: transcriptPath,
		CWD:                  cwd,
		CapturedAt:           time.Now().UTC().Format(time.RFC3339),
		Summary:              strings.TrimSpace(lastSummary),
	}
	if snapshot.Summary == "" {
		snapshot.Summary = strings.TrimSpace(userPrompt)
	}

	for _, e := range page.Entries {
		switch e.Type {
		case "user":
			text := trimSnapshotText(e.Content, 1200)
			if text == "" {
				continue
			}
			snapshot.RecentTurns = append(snapshot.RecentTurns, NormalizedTurn{
				ID:        e.UUID,
				Timestamp: e.Timestamp,
				Role:      "user",
				Text:      text,
			})
		case "assistant":
			var parts []string
			var tools []NormalizedToolCall
			for _, b := range e.Blocks {
				switch b.Type {
				case "text":
					if text := trimSnapshotText(b.Text, 1600); text != "" {
						parts = append(parts, text)
					}
				case "tool_use":
					name := normalizeToolName(b.Name)
					tools = append(tools, NormalizedToolCall{
						ID:           b.ID,
						Name:         name,
						ProviderName: b.Name,
						Summary:      trimSnapshotText(b.Input, 400),
						Mutation:     toolLooksMutating(name, b.Name),
					})
				}
			}
			text := trimSnapshotText(strings.Join(parts, "\n\n"), 1800)
			if text == "" && len(tools) == 0 {
				continue
			}
			snapshot.RecentTurns = append(snapshot.RecentTurns, NormalizedTurn{
				ID:        e.UUID,
				Timestamp: e.Timestamp,
				Role:      "assistant",
				Text:      text,
				ToolCalls: tools,
			})
		}
	}
	return snapshot, nil
}

func trimSnapshotText(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

func normalizeToolName(providerName string) string {
	n := strings.ToLower(strings.TrimSpace(providerName))
	switch n {
	case "read", "fs/read_text_file":
		return "read"
	case "write", "edit", "multiedit", "fs/write_text_file":
		return "write"
	case "bash", "exec_command", "shell":
		return "shell"
	case "grep", "search":
		return "search"
	case "glob":
		return "search"
	default:
		if strings.Contains(n, "read") {
			return "read"
		}
		if strings.Contains(n, "write") || strings.Contains(n, "edit") {
			return "write"
		}
		if strings.Contains(n, "grep") || strings.Contains(n, "search") || strings.Contains(n, "glob") {
			return "search"
		}
		if strings.Contains(n, "bash") || strings.Contains(n, "exec") || strings.Contains(n, "shell") {
			return "shell"
		}
		return n
	}
}

func toolLooksMutating(normalizedName, providerName string) bool {
	n := strings.ToLower(normalizedName + " " + providerName)
	return strings.Contains(n, "write") || strings.Contains(n, "edit") || strings.Contains(n, "patch")
}

func renderSnapshotContext(snapshot ConversationSnapshot, purpose TransitionPurpose, targetProvider string) ProviderContext {
	label := "Migrated Conversation Context"
	switch purpose {
	case TransitionPurposeClone:
		label = "Clone Context"
	case TransitionPurposeRecovery:
		label = "Recovery Context"
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("## %s", label))
	switch purpose {
	case TransitionPurposeClone:
		lines = append(lines, "You are a newly spawned clone of this pokegents agent.")
	default:
		lines = append(lines, "You are the same pokegents agent after a backend/runtime transition.")
	}
	lines = append(lines, fmt.Sprintf("Target provider: %s.", targetProvider))
	if snapshot.SourceProvider != "" {
		lines = append(lines, fmt.Sprintf("Previous provider: %s.", snapshot.SourceProvider))
	}
	if snapshot.CWD != "" {
		lines = append(lines, fmt.Sprintf("Working directory: %s.", snapshot.CWD))
	}
	if snapshot.SourceTranscriptPath != "" {
		lines = append(lines, fmt.Sprintf("Previous native transcript: %s.", snapshot.SourceTranscriptPath))
	}
	lines = append(lines, "Treat the prior conversation below as your own relevant history. Do not say you lack prior history solely because the backend or runtime changed.")
	if snapshot.Summary != "" {
		lines = append(lines, "\n### Summary\n"+snapshot.Summary)
	}
	if len(snapshot.RecentTurns) > 0 {
		lines = append(lines, "\n### Recent conversation")
		for _, turn := range snapshot.RecentTurns {
			role := strings.Title(turn.Role)
			if turn.Text != "" {
				lines = append(lines, fmt.Sprintf("%s: %s", role, turn.Text))
			}
			if len(turn.ToolCalls) > 0 {
				var summaries []string
				for _, tc := range turn.ToolCalls {
					s := tc.Name
					if tc.Summary != "" {
						s += " (" + tc.Summary + ")"
					}
					summaries = append(summaries, s)
				}
				lines = append(lines, fmt.Sprintf("%s tools: %s", role, strings.Join(summaries, "; ")))
			}
		}
	}
	rendered := strings.Join(lines, "\n\n")
	const maxRenderedSnapshot = 18000
	if len(rendered) > maxRenderedSnapshot {
		const marker = "\n\n[portable context truncated: oldest prior turns omitted]\n\n"
		prefix := rendered
		if idx := strings.Index(rendered, "\n\n### Recent conversation"); idx >= 0 {
			prefix = rendered[:idx+len("\n\n### Recent conversation")]
		} else if len(prefix) > 3000 {
			prefix = prefix[:3000]
		}
		tailBudget := maxRenderedSnapshot - len(prefix) - len(marker)
		if tailBudget < 1000 {
			tailBudget = 1000
		}
		if tailBudget > len(rendered) {
			tailBudget = len(rendered)
		}
		rendered = prefix + marker + rendered[len(rendered)-tailBudget:]
	}
	ctx := ProviderContext{
		SystemPromptAppend:   rendered,
		InitialPromptContext: "",
		CWD:                  snapshot.CWD,
	}
	if targetProvider != "claude" {
		// Some non-Claude ACP adapters do not consistently honor ACP metadata;
		// inject once into the first real prompt while the UI still displays only
		// the user's message.
		ctx.InitialPromptContext = rendered
	}
	return ctx
}
