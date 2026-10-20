package store

// TranscriptReader provides read access to transcript files across multiple
// ACP backends (Claude, Codex, and future agents). It auto-detects the format
// and delegates to the appropriate parser implementation.
type TranscriptReader struct {
	claudeProjectDir string
}

// NewTranscriptReader creates a reader for transcripts. The claudeProjectDir
// is used for Claude path lookups but the reader supports all backends.
func NewTranscriptReader(claudeProjectDir string) *TranscriptReader {
	return &TranscriptReader{claudeProjectDir: claudeProjectDir}
}

// FindPath locates the transcript JSONL file for a session ID across all backends.
func (tr *TranscriptReader) FindPath(sessionID string) string {
	return FindTranscriptPath(sessionID, tr.claudeProjectDir)
}

// ContextUsage holds token counts extracted from a transcript.
type ContextUsage struct {
	Tokens int
	Window int
}

// ExtractContextUsage reads the last assistant message's usage from the transcript.
// Auto-detects the parser format.
func (tr *TranscriptReader) ExtractContextUsage(path string) ContextUsage {
	if path == "" {
		return ContextUsage{}
	}
	parser := DetectParser(path)
	return parser.ExtractContextUsage(path)
}

// ExtractTrace reads the tail of a transcript and returns the last assistant
// text block (the live thinking/output trace). Auto-detects format.
func (tr *TranscriptReader) ExtractTrace(path string) string {
	if path == "" {
		return ""
	}
	parser := DetectParser(path)
	return parser.ExtractTrace(path)
}

// ExtractLastUserPrompt reads the transcript and returns the last user message.
// Auto-detects format.
func (tr *TranscriptReader) ExtractLastUserPrompt(path string) string {
	if path == "" {
		return ""
	}
	parser := DetectParser(path)
	return parser.ExtractLastUserPrompt(path)
}

// TranscriptEntry is a parsed conversation entry for the chat panel.
type TranscriptEntry struct {
	UUID      string         `json:"uuid"`
	Type      string         `json:"type"` // "user", "assistant", "tool_result", "system"
	Timestamp string         `json:"timestamp"`
	Content   string         `json:"content,omitempty"`    // for user messages (plain text)
	Blocks    []ContentBlock `json:"blocks,omitempty"`     // for assistant messages
	ToolUseID string         `json:"tool_use_id,omitempty"` // for tool_result
	Truncated bool           `json:"truncated,omitempty"`
	FullSize  int            `json:"full_size,omitempty"`
	Model     string         `json:"model,omitempty"`
	Tokens    *TokenInfo     `json:"tokens,omitempty"`
}

// ContentBlock is a single block within an assistant message.
type ContentBlock struct {
	Type  string `json:"type"` // "text", "thinking", "tool_use"
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`    // tool_use block ID (matches tool_result.tool_use_id)
	Name  string `json:"name,omitempty"`  // tool name
	Input string `json:"input,omitempty"` // tool input summary (first 200 chars of JSON)
}

// TokenInfo holds token counts for an assistant message.
type TokenInfo struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cache_read,omitempty"`
	CacheCreate int `json:"cache_create,omitempty"`
}

// TranscriptPage is the paginated response for the chat panel.
type TranscriptPage struct {
	Entries []TranscriptEntry `json:"entries"`
	HasMore bool              `json:"has_more"`
}

// BatchExtract is a package-level convenience that auto-detects the format and
// extracts all transcript fields in a single pass. Used by the trace poller.
func BatchExtract(path string) BatchResult {
	if path == "" {
		return BatchResult{}
	}
	parser := DetectParser(path)
	return parser.BatchExtract(path)
}

// BatchExtractMethod is the TranscriptReader method form of BatchExtract.
func (tr *TranscriptReader) BatchExtract(path string) BatchResult {
	return BatchExtract(path)
}

// ParseTranscript reads a transcript file and returns conversation entries.
// Auto-detects the format (Claude, Codex, etc.) and delegates to the
// appropriate parser. tail: number of entries from the end (0 = all).
// afterUUID: only return entries after this UUID.
func (tr *TranscriptReader) ParseTranscript(path string, tail int, afterUUID string) TranscriptPage {
	if path == "" {
		return TranscriptPage{}
	}
	parser := DetectParser(path)
	return parser.Parse(path, tail, afterUUID)
}
