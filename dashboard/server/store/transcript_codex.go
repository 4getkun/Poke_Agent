package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// CodexTranscriptParser implements TranscriptParser for Codex's JSONL format.
// Codex stores transcripts at ~/.codex/sessions/YYYY/MM/DD/{name}-{session_id}.jsonl
// with entry types: "session_meta", "response_item", "event_msg", "turn_context", "compacted".
type CodexTranscriptParser struct{}

// Parse reads a Codex transcript file and returns conversation entries in the
// common TranscriptEntry format used by the dashboard frontend.
func (p *CodexTranscriptParser) Parse(path string, tail int, afterUUID string) TranscriptPage {
	if path == "" {
		return TranscriptPage{}
	}

	f, err := os.Open(path)
	if err != nil {
		return TranscriptPage{}
	}
	defer f.Close()

	// Codex compaction can be one very large JSONL record. We still scan from the
	// head so that compaction records are parseable, but keep only the requested
	// tail in memory instead of retaining every raw JSON line for the whole file.
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)

	var entries []TranscriptEntry
	foundAfter := afterUUID == ""
	entryIdx := 0
	seenAfter := 0
	hasMore := false

	appendEntry := func(entry TranscriptEntry) {
		seenAfter++
		if tail > 0 && seenAfter > tail {
			hasMore = true
			if len(entries) >= tail {
				copy(entries, entries[1:])
				entries[len(entries)-1] = entry
				return
			}
		}
		entries = append(entries, entry)
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &raw) != nil || len(raw.Payload) == 0 {
			continue
		}

		var entry *TranscriptEntry
		switch raw.Type {
		case "response_item":
			var payload map[string]any
			if json.Unmarshal(raw.Payload, &payload) != nil {
				continue
			}
			entry = p.parseResponseItem(payload, raw.Timestamp, &entryIdx)
		case "event_msg":
			var payload map[string]any
			if json.Unmarshal(raw.Payload, &payload) != nil {
				continue
			}
			entry = p.parseEventMsg(payload, raw.Timestamp, &entryIdx)
		case "compacted":
			entry = p.parseCompactedEntry(raw.Payload, raw.Timestamp, &entryIdx)
		}
		if entry == nil {
			continue
		}
		if !foundAfter {
			if entry.UUID == afterUUID {
				foundAfter = true
			}
			continue
		}
		appendEntry(*entry)
	}
	if sc.Err() != nil {
		return TranscriptPage{}
	}
	return TranscriptPage{Entries: entries, HasMore: hasMore}
}

// ExtractCwd reads the session_meta entry to get the working directory.
// session_meta is always the first line, so we read from the HEAD of the file.
func (p *CodexTranscriptParser) ExtractCwd(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	data := readHead(path, 64*1024)
	if data == nil {
		return "", fmt.Errorf("cannot read file: %s", path)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw struct {
			Type    string `json:"type"`
			Payload struct {
				Cwd string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		if raw.Type == "session_meta" && raw.Payload.Cwd != "" {
			return raw.Payload.Cwd, nil
		}
		if raw.Type == "turn_context" && raw.Payload.Cwd != "" {
			return raw.Payload.Cwd, nil
		}
	}
	return "", fmt.Errorf("no cwd found in codex jsonl %s", path)
}

// ExtractLastMessages returns the last user message and last assistant response.
// Uses only response_item entries (canonical source, consistent with Parse()).
func (p *CodexTranscriptParser) ExtractLastMessages(path string) (userPrompt, lastSummary string) {
	if path == "" {
		return "", ""
	}

	data := readTail(path, 512*1024)
	if data == nil {
		return "", ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		entryType, _ := raw["type"].(string)
		if entryType != "response_item" {
			continue
		}
		payload, _ := raw["payload"].(map[string]any)
		if payload == nil {
			continue
		}

		role, _ := payload["role"].(string)
		if role == "user" {
			text := p.extractTextFromContent(payload)
			if text != "" {
				trimmed := strings.TrimSpace(text)
				if !strings.HasPrefix(trimmed, "<") && len(trimmed) > 10 {
					userPrompt = text
				}
			}
		} else if role == "assistant" {
			text := p.extractTextFromContent(payload)
			if text != "" && len(text) > 20 {
				lastSummary = text
			}
		}
	}

	if len(userPrompt) > 500 {
		userPrompt = userPrompt[:500]
	}
	if len(lastSummary) > 500 {
		lastSummary = lastSummary[:500]
	}
	return userPrompt, lastSummary
}

// ExtractContextUsage extracts token and window info from the transcript.
// Codex stores model_context_window in task_started/turn_context events and
// token counts in response_item metadata or token_count events.
func (p *CodexTranscriptParser) ExtractContextUsage(path string) ContextUsage {
	if path == "" {
		return ContextUsage{}
	}

	data := readTail(path, 256*1024)
	if data == nil {
		return ContextUsage{}
	}

	var window int
	var lastTokens int

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		entryType, _ := raw["type"].(string)
		payload, _ := raw["payload"].(map[string]any)
		if payload == nil {
			continue
		}

		switch entryType {
		case "event_msg":
			msgType, _ := payload["type"].(string)
			if msgType == "task_started" {
				if w, ok := payload["model_context_window"].(float64); ok && w > 0 {
					window = int(w)
				}
			} else if msgType == "token_count" {
				if info, ok := payload["info"].(map[string]any); ok && info != nil {
					// Codex nests per-turn tokens under info.last_token_usage.total_tokens
					if ttu, ok := info["last_token_usage"].(map[string]any); ok {
						if total, ok := ttu["total_tokens"].(float64); ok && total > 0 {
							lastTokens = int(total)
						}
					}
					if w, ok := info["model_context_window"].(float64); ok && w > 0 {
						window = int(w)
					}
				}
			}
		}
	}

	if window == 0 {
		window = 200000 // GPT-class model default
	}

	return ContextUsage{Tokens: lastTokens, Window: window}
}

// ExtractTrace returns the last assistant text from response_item entries.
func (p *CodexTranscriptParser) ExtractTrace(path string) string {
	if path == "" {
		return ""
	}

	data := readTail(path, 64*1024)
	if data == nil {
		return ""
	}

	lastText := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		entryType, _ := raw["type"].(string)
		if entryType != "response_item" {
			continue
		}
		payload, _ := raw["payload"].(map[string]any)
		if payload == nil {
			continue
		}

		role, _ := payload["role"].(string)
		if role == "assistant" {
			text := p.extractTextFromContent(payload)
			if text != "" {
				lastText = text
			}
		}
	}

	if len(lastText) > 200 {
		lastText = lastText[len(lastText)-200:]
	}
	return lastText
}

// ExtractLastUserPrompt returns the last user message.
// Uses only response_item entries (consistent with Parse()).
func (p *CodexTranscriptParser) ExtractLastUserPrompt(path string) string {
	if path == "" {
		return ""
	}

	data := readTail(path, 256*1024)
	if data == nil {
		return ""
	}

	lastPrompt := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		entryType, _ := raw["type"].(string)
		if entryType != "response_item" {
			continue
		}
		payload, _ := raw["payload"].(map[string]any)
		if payload == nil {
			continue
		}

		role, _ := payload["role"].(string)
		if role == "user" {
			text := p.extractTextFromContent(payload)
			if text != "" {
				trimmed := strings.TrimSpace(text)
				if !strings.HasPrefix(trimmed, "<") {
					lastPrompt = text
				}
			}
		}
	}

	r := []rune(lastPrompt)
	if len(r) > 200 {
		return string(r[:200])
	}
	return lastPrompt
}

// ExtractActivityFeed builds an activity timeline from the last turn.
// Pivots on response_item role=user (consistent with Parse()) to find turn boundary.
func (p *CodexTranscriptParser) ExtractActivityFeed(path string) []ActivityItem {
	if path == "" {
		return nil
	}

	data := readTail(path, 256*1024)
	if data == nil {
		return nil
	}

	type parsedEntry struct {
		entryType string
		payload   map[string]any
		timestamp string
	}

	var allEntries []parsedEntry
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		entryType, _ := raw["type"].(string)
		payload, _ := raw["payload"].(map[string]any)
		timestamp, _ := raw["timestamp"].(string)
		if payload != nil {
			allEntries = append(allEntries, parsedEntry{entryType, payload, timestamp})
		}
	}

	// Find last user response_item (start of latest turn).
	lastUserIdx := -1
	for i := len(allEntries) - 1; i >= 0; i-- {
		e := allEntries[i]
		if e.entryType == "response_item" {
			role, _ := e.payload["role"].(string)
			if role == "user" {
				text := p.extractTextFromContent(e.payload)
				trimmed := strings.TrimSpace(text)
				if text != "" && !strings.HasPrefix(trimmed, "<") {
					lastUserIdx = i
					break
				}
			}
		}
	}

	startIdx := lastUserIdx + 1
	if startIdx < 0 {
		startIdx = 0
	}

	var feed []ActivityItem
	for _, e := range allEntries[startIdx:] {
		ts := ""
		if e.timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.timestamp); err == nil {
				ts = t.Local().Format("15:04:05")
			} else if t, err := time.Parse(time.RFC3339, e.timestamp); err == nil {
				ts = t.Local().Format("15:04:05")
			}
		}

		if e.entryType != "response_item" {
			continue
		}

		payloadType, _ := e.payload["type"].(string)
		if payloadType == "function_call" {
			name, _ := e.payload["name"].(string)
			args, _ := e.payload["arguments"].(string)
			input := ""
			if args != "" {
				var argsMap map[string]any
				if json.Unmarshal([]byte(args), &argsMap) == nil {
					for _, key := range []string{"cmd", "command", "file_path", "pattern", "query"} {
						if v, ok := argsMap[key]; ok {
							input = truncateStr(fmt.Sprintf("%v", v), 80)
							break
						}
					}
				}
			}
			feed = append(feed, ActivityItem{Time: ts, Type: "tool", Text: name + ": " + input})
		}

		role, _ := e.payload["role"].(string)
		if role == "assistant" {
			text := p.extractTextFromContent(e.payload)
			if text != "" {
				for _, line := range strings.Split(text, "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					if len(line) > 200 {
						line = line[:200]
					}
					feed = append(feed, ActivityItem{Time: ts, Type: "text", Text: line})
				}
			}
		}
	}

	if len(feed) > 30 {
		feed = feed[len(feed)-30:]
	}
	return feed
}

// IsInterrupted checks if the session was aborted/interrupted.
func (p *CodexTranscriptParser) IsInterrupted(path string) bool {
	if path == "" {
		return false
	}

	data := readTail(path, 4096)
	if data == nil {
		return false
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	// Check last few lines for turn_aborted event
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		var raw map[string]any
		if json.Unmarshal([]byte(lines[i]), &raw) != nil {
			continue
		}
		entryType, _ := raw["type"].(string)
		if entryType == "event_msg" {
			payload, _ := raw["payload"].(map[string]any)
			if payload != nil {
				msgType, _ := payload["type"].(string)
				if msgType == "turn_aborted" {
					return true
				}
			}
		}
	}
	return false
}

// BatchExtract reads the transcript tail once and extracts all fields in a single pass.
func (p *CodexTranscriptParser) BatchExtract(path string) BatchResult {
	if path == "" {
		return BatchResult{}
	}

	data := readTail(path, 256*1024)
	if data == nil {
		return BatchResult{}
	}

	var result BatchResult
	var window int
	var lastTokens int
	var lastTraceText string
	var lastUserIdx int = -1

	type entryData struct {
		entryType string
		payload   map[string]any
		timestamp string
	}

	var entries []entryData
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		entryType, _ := raw["type"].(string)
		payload, _ := raw["payload"].(map[string]any)
		timestamp, _ := raw["timestamp"].(string)
		if payload != nil {
			entries = append(entries, entryData{entryType, payload, timestamp})
		}
	}

	for i, e := range entries {
		switch e.entryType {
		case "response_item":
			role, _ := e.payload["role"].(string)
			payloadType, _ := e.payload["type"].(string)

			if role == "user" {
				text := p.extractTextFromContent(e.payload)
				if text != "" {
					trimmed := strings.TrimSpace(text)
					if !strings.HasPrefix(trimmed, "<") && len(trimmed) > 10 {
						lastUserIdx = i
						r := []rune(text)
						if len(r) > 200 {
							result.LastUserPrompt = string(r[:200])
						} else {
							result.LastUserPrompt = text
						}
					}
				}
			} else if role == "assistant" {
				text := p.extractTextFromContent(e.payload)
				if text != "" {
					lastTraceText = text
					if len(text) > 500 {
						result.LastSummary = text[:500]
					} else {
						result.LastSummary = text
					}
				}
			}

			if usage, ok := e.payload["usage"].(map[string]any); ok && usage != nil {
				if total, ok := usage["total_tokens"].(float64); ok && total > 0 {
					lastTokens = int(total)
				}
			}
			_ = payloadType // used below for activity feed

		case "event_msg":
			msgType, _ := e.payload["type"].(string)
			if msgType == "task_started" {
				if w, ok := e.payload["model_context_window"].(float64); ok && w > 0 {
					window = int(w)
				}
			} else if msgType == "token_count" {
				if info, ok := e.payload["info"].(map[string]any); ok && info != nil {
					if ttu, ok := info["last_token_usage"].(map[string]any); ok {
						if total, ok := ttu["total_tokens"].(float64); ok && total > 0 {
							lastTokens = int(total)
						}
					}
					if w, ok := info["model_context_window"].(float64); ok && w > 0 {
						window = int(w)
					}
				}
			}

		case "turn_context":
			if w, ok := e.payload["model_context_window"].(float64); ok && w > 0 {
				window = int(w)
			}
		}
	}

	// Trace: last 200 chars from end
	if len(lastTraceText) > 200 {
		result.Trace = lastTraceText[len(lastTraceText)-200:]
	} else {
		result.Trace = lastTraceText
	}

	// Context usage
	if window == 0 {
		window = 200000
	}
	result.ContextUsage = ContextUsage{Tokens: lastTokens, Window: window}

	// Interrupt detection: check last few entries for turn_aborted
	for i := len(entries) - 1; i >= 0 && i >= len(entries)-5; i-- {
		if entries[i].entryType == "event_msg" {
			msgType, _ := entries[i].payload["type"].(string)
			if msgType == "turn_aborted" {
				result.IsInterrupted = true
				break
			}
		}
	}

	// Activity feed: response_items after lastUserIdx
	startIdx := lastUserIdx + 1
	if startIdx < 0 {
		startIdx = 0
	}
	for _, e := range entries[startIdx:] {
		if e.entryType != "response_item" {
			continue
		}
		ts := ""
		if e.timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.timestamp); err == nil {
				ts = t.Local().Format("15:04:05")
			} else if t, err := time.Parse(time.RFC3339, e.timestamp); err == nil {
				ts = t.Local().Format("15:04:05")
			}
		}

		payloadType, _ := e.payload["type"].(string)
		if payloadType == "function_call" {
			name, _ := e.payload["name"].(string)
			args, _ := e.payload["arguments"].(string)
			input := ""
			if args != "" {
				var argsMap map[string]any
				if json.Unmarshal([]byte(args), &argsMap) == nil {
					for _, key := range []string{"cmd", "command", "file_path", "pattern", "query"} {
						if v, ok := argsMap[key]; ok {
							input = truncateStr(fmt.Sprintf("%v", v), 80)
							break
						}
					}
				}
			}
			result.ActivityFeed = append(result.ActivityFeed, ActivityItem{Time: ts, Type: "tool", Text: name + ": " + input})
		}

		role, _ := e.payload["role"].(string)
		if role == "assistant" {
			text := p.extractTextFromContent(e.payload)
			if text != "" {
				for _, line := range strings.Split(text, "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					if len(line) > 200 {
						line = line[:200]
					}
					result.ActivityFeed = append(result.ActivityFeed, ActivityItem{Time: ts, Type: "text", Text: line})
				}
			}
		}
	}
	if len(result.ActivityFeed) > 30 {
		result.ActivityFeed = result.ActivityFeed[len(result.ActivityFeed)-30:]
	}

	return result
}

// --- internal helpers ---

// extractTextFromContent extracts text from a response_item's content array.
// Codex uses content blocks with type "input_text" or "output_text".
func (p *CodexTranscriptParser) extractTextFromContent(payload map[string]any) string {
	content, ok := payload["content"].([]any)
	if !ok {
		return ""
	}

	var texts []string
	for _, block := range content {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := m["type"].(string)
		switch blockType {
		case "input_text":
			if t, ok := m["text"].(string); ok && t != "" {
				// Skip system/permission text blocks
				trimmed := strings.TrimSpace(t)
				if strings.HasPrefix(trimmed, "<permissions") ||
					strings.HasPrefix(trimmed, "<environment_context") ||
					strings.HasPrefix(trimmed, "<collaboration_mode") ||
					strings.HasPrefix(trimmed, "<skills_instructions") {
					continue
				}
				texts = append(texts, t)
			}
		case "output_text":
			if t, ok := m["text"].(string); ok && t != "" {
				texts = append(texts, t)
			}
		}
	}

	if len(texts) == 0 {
		return ""
	}
	return texts[len(texts)-1] // Return the last meaningful text block
}

// parseResponseItem converts a Codex response_item into a TranscriptEntry.
func (p *CodexTranscriptParser) parseResponseItem(payload map[string]any, timestamp string, idx *int) *TranscriptEntry {
	role, _ := payload["role"].(string)
	payloadType, _ := payload["type"].(string)

	*idx++
	uuid := fmt.Sprintf("codex-%d", *idx)

	switch {
	case role == "user":
		text := p.extractTextFromContent(payload)
		if text == "" {
			return nil
		}
		// Skip system-injected user messages
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "<") ||
			strings.HasPrefix(trimmed, "# Context from my IDE") {
			return nil
		}
		return &TranscriptEntry{
			UUID:      uuid,
			Type:      "user",
			Timestamp: timestamp,
			Content:   text,
		}

	case role == "assistant":
		text := p.extractTextFromContent(payload)
		if text == "" {
			return nil
		}
		return &TranscriptEntry{
			UUID:      uuid,
			Type:      "assistant",
			Timestamp: timestamp,
			Blocks:    []ContentBlock{{Type: "text", Text: text}},
		}

	case payloadType == "function_call":
		name, _ := payload["name"].(string)
		callID, _ := payload["call_id"].(string)
		args, _ := payload["arguments"].(string)
		inputSummary := args
		maxLen := 2000
		preserveValidJSON := false
		if name == "exec_command" {
			// Long heredoc commands often contain the only record of which
			// notebooks/scripts were edited. Keep more so Files/Commands tabs can
			// recover meaningful targets from transcript backfill.
			maxLen = 12000
			var argsMap map[string]any
			if err := json.Unmarshal([]byte(args), &argsMap); err == nil && argsMap != nil {
				for _, key := range []string{"cmd", "command"} {
					if cmd, ok := argsMap[key].(string); ok && len(cmd) > maxLen {
						argsMap[key] = cmd[:maxLen] + "..."
					}
				}
				if b, err := json.Marshal(argsMap); err == nil {
					inputSummary = string(b)
					preserveValidJSON = true
				}
			}
		}
		if len(inputSummary) > maxLen && !preserveValidJSON {
			inputSummary = inputSummary[:maxLen] + "..."
		}
		return &TranscriptEntry{
			UUID:      uuid,
			Type:      "assistant",
			Timestamp: timestamp,
			Blocks:    []ContentBlock{{Type: "tool_use", ID: callID, Name: name, Input: inputSummary}},
		}

	case payloadType == "custom_tool_call":
		name, _ := payload["name"].(string)
		callID, _ := payload["call_id"].(string)
		input, _ := payload["input"].(string)
		inputSummary := input
		maxLen := 2000
		if name == "apply_patch" {
			maxLen = 12000
		}
		if len(inputSummary) > maxLen {
			inputSummary = inputSummary[:maxLen] + "..."
		}
		return &TranscriptEntry{
			UUID:      uuid,
			Type:      "assistant",
			Timestamp: timestamp,
			Blocks:    []ContentBlock{{Type: "tool_use", ID: callID, Name: name, Input: inputSummary}},
		}

	case payloadType == "function_call_output":
		callID, _ := payload["call_id"].(string)
		output, _ := payload["output"].(string)
		truncated := len(output) > 2000
		fullSize := len(output)
		if truncated {
			output = output[:2000]
		}
		return &TranscriptEntry{
			UUID:      uuid,
			Type:      "tool_result",
			Timestamp: timestamp,
			Content:   output,
			ToolUseID: callID,
			Truncated: truncated,
			FullSize:  fullSize,
		}

	case payloadType == "custom_tool_call_output":
		callID, _ := payload["call_id"].(string)
		output, _ := payload["output"].(string)
		truncated := len(output) > 2000
		fullSize := len(output)
		if truncated {
			output = output[:2000]
		}
		return &TranscriptEntry{
			UUID:      uuid,
			Type:      "tool_result",
			Timestamp: timestamp,
			Content:   output,
			ToolUseID: callID,
			Truncated: truncated,
			FullSize:  fullSize,
		}

	case role == "developer":
		// Skip developer/system messages (permissions, skills instructions, etc.)
		return nil
	}

	return nil
}

// parseEventMsg converts a Codex event_msg into a TranscriptEntry (if applicable).
func (p *CodexTranscriptParser) parseEventMsg(payload map[string]any, timestamp string, idx *int) *TranscriptEntry {
	msgType, _ := payload["type"].(string)

	*idx++

	switch msgType {
	case "agent_message":
		// Skip — the response_item with role=assistant is the canonical source.
		return nil

	case "user_message":
		// Skip — the response_item with role=user is the canonical source.
		return nil
	}

	return nil
}

// parseCompactedEntry converts Codex's compacted JSONL record into the same
// user-visible shape Claude uses after /compact: a normal user transcript line
// containing the full summary, which the chat panel can collapse/expand.
//
// Important: the raw Codex payload also contains replacement_history, which can
// be many MB and includes the entire old conversation. Never forward it to the
// browser; only the generated compact summary belongs in the transcript UI.
func (p *CodexTranscriptParser) parseCompactedEntry(payload json.RawMessage, timestamp string, idx *int) *TranscriptEntry {
	*idx++

	var compacted struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(payload, &compacted) != nil {
		return nil
	}
	content := compactedTranscriptContent(compacted.Message)
	if content == "" {
		return nil
	}

	return &TranscriptEntry{
		UUID:      fmt.Sprintf("codex-%d", *idx),
		Type:      "user",
		Timestamp: timestamp,
		Content:   content,
	}
}

func compactedTranscriptContent(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "Context compacted."
	}
	return "Context compacted.\n\n" + message
}

// --- Ensure CodexTranscriptParser implements TranscriptParser ---
var _ TranscriptParser = (*CodexTranscriptParser)(nil)
