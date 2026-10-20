package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ClaudeTranscriptParser implements TranscriptParser for Claude Code's JSONL format.
// Claude stores transcripts at ~/.claude/projects/{cwd-hash}/{session_id}.jsonl
// with entry types: "init", "summary", "user", "assistant", "system", etc.
type ClaudeTranscriptParser struct{}

// Compile-time interface compliance check.
var _ TranscriptParser = (*ClaudeTranscriptParser)(nil)

// Parse reads a Claude transcript file and returns conversation entries.
func (p *ClaudeTranscriptParser) Parse(path string, tail int, afterUUID string) TranscriptPage {
	if path == "" {
		return TranscriptPage{}
	}

	readSize := int64(512 * 1024)
	if tail > 200 || afterUUID != "" {
		readSize = 2 * 1024 * 1024
	}

	data := readTail(path, readSize)
	if data == nil {
		return TranscriptPage{}
	}

	var entries []TranscriptEntry
	foundAfter := afterUUID == ""

	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}

		entryType, _ := raw["type"].(string)
		uuid, _ := raw["uuid"].(string)
		timestamp, _ := raw["timestamp"].(string)

		if !foundAfter {
			if uuid == afterUUID {
				foundAfter = true
			}
			continue
		}

		switch entryType {
		case "user":
			entry := p.parseUserEntry(raw, uuid, timestamp)
			if entry != nil {
				entries = append(entries, *entry)
			}
		case "assistant":
			entry := p.parseAssistantEntry(raw, uuid, timestamp)
			if entry != nil {
				entries = append(entries, *entry)
			}
		case "system":
			if subtype, _ := raw["subtype"].(string); subtype == "stop_hook_summary" {
				continue
			}
		default:
			continue
		}
	}

	hasMore := false
	if tail > 0 && len(entries) > tail {
		entries = entries[len(entries)-tail:]
		hasMore = true
	}

	return TranscriptPage{Entries: entries, HasMore: hasMore}
}

// ExtractCwd reads the JSONL line by line and returns the first non-null cwd field.
func (p *ClaudeTranscriptParser) ExtractCwd(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var entry struct {
			Cwd *string `json:"cwd"`
		}
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Cwd != nil && *entry.Cwd != "" {
			return *entry.Cwd, nil
		}
	}
	return "", fmt.Errorf("no cwd found in jsonl %s", path)
}

// ExtractLastMessages returns the last user prompt and last assistant summary.
func (p *ClaudeTranscriptParser) ExtractLastMessages(path string) (userPrompt, lastSummary string) {
	if path == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", ""
	}
	offset := info.Size() - 1024*1024
	if offset < 0 {
		offset = 0
	}
	f.Seek(offset, 0)

	data, err := io.ReadAll(f)
	if err != nil {
		return "", ""
	}

	tail := string(data)
	if idx := strings.LastIndex(tail, `"compact_boundary"`); idx >= 0 {
		if nl := strings.Index(tail[idx:], "\n"); nl >= 0 {
			tail = tail[idx+nl+1:]
		}
	}

	stripTags := func(s string) string {
		out := s
		for {
			start := strings.Index(out, "<")
			if start < 0 {
				break
			}
			end := strings.Index(out[start:], ">")
			if end < 0 {
				break
			}
			out = out[:start] + out[start+end+1:]
		}
		return strings.Join(strings.Fields(out), " ")
	}
	var lastSubstantivePrompt, lastAnyPrompt string
	var lastSubstantiveSummary, lastAnySummary string

	for _, line := range strings.Split(tail, "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		entryType, _ := entry["type"].(string)
		switch entryType {
		case "user":
			msg, ok := entry["message"].(map[string]any)
			if !ok {
				continue
			}
			var text string
			switch c := msg["content"].(type) {
			case string:
				text = c
			case []any:
				for _, block := range c {
					m, ok := block.(map[string]any)
					if !ok {
						continue
					}
					blockType, _ := m["type"].(string)
					if blockType == "text" {
						if t, ok := m["text"].(string); ok && t != "" {
							text = t
						}
					}
				}
			}
			if text != "" {
				stripped := stripTags(text)
				trimmed := strings.TrimSpace(text)
				isSystem := strings.HasPrefix(trimmed, "<") ||
					strings.HasPrefix(trimmed, "This session is being continued") ||
					strings.HasPrefix(trimmed, "[Image:")
				lastAnyPrompt = text
				if !isSystem && len(stripped) > 20 {
					lastSubstantivePrompt = text
				}
			}
		case "assistant":
			msg, ok := entry["message"].(map[string]any)
			if !ok {
				continue
			}
			content, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				m, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok && t != "" {
						lastAnySummary = t
						if len(stripTags(t)) > 30 {
							lastSubstantiveSummary = t
						}
					}
				}
			}
		}
	}

	userPrompt = lastSubstantivePrompt
	if userPrompt == "" {
		userPrompt = lastAnyPrompt
	}
	lastSummary = lastSubstantiveSummary
	if lastSummary == "" {
		lastSummary = lastAnySummary
	}

	if len(userPrompt) > 500 {
		userPrompt = userPrompt[:500]
	}
	if len(lastSummary) > 500 {
		lastSummary = lastSummary[:500]
	}
	return userPrompt, lastSummary
}

// ExtractContextUsage reads the last assistant message's usage from the transcript.
func (p *ClaudeTranscriptParser) ExtractContextUsage(path string) ContextUsage {
	if path == "" {
		return ContextUsage{}
	}
	f, err := os.Open(path)
	if err != nil {
		return ContextUsage{}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ContextUsage{}
	}
	offset := info.Size() - 256*1024
	if offset < 0 {
		offset = 0
	}
	f.Seek(offset, 0)

	data, err := io.ReadAll(f)
	if err != nil {
		return ContextUsage{}
	}

	if idx := strings.LastIndex(string(data), `"compact_boundary"`); idx >= 0 {
		data = data[idx:]
	}

	var lastTokens int
	var model string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["type"] != "assistant" {
			continue
		}
		msg, ok := entry["message"].(map[string]any)
		if !ok {
			continue
		}
		if m, ok := msg["model"].(string); ok && m != "" {
			model = m
		}
		usage, ok := msg["usage"].(map[string]any)
		if !ok {
			continue
		}
		total := 0
		if v, ok := usage["input_tokens"].(float64); ok {
			total += int(v)
		}
		if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
			total += int(v)
		}
		if v, ok := usage["cache_read_input_tokens"].(float64); ok {
			total += int(v)
		}
		if total > 100 {
			lastTokens = total
		}
	}

	window := 200000
	if strings.Contains(model, "opus") {
		window = 1000000
	}

	return ContextUsage{Tokens: lastTokens, Window: window}
}

// ExtractTrace returns the last assistant text block from the transcript tail.
func (p *ClaudeTranscriptParser) ExtractTrace(path string) string {
	if path == "" {
		return ""
	}
	data := readTail(path, 256*1024)
	if data == nil {
		return ""
	}

	lastText := ""
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["type"] != "assistant" {
			continue
		}
		msg, ok := entry["message"].(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range content {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "text" {
				if t, ok := m["text"].(string); ok && t != "" {
					lastText = t
				}
			}
		}
	}

	if len(lastText) > 200 {
		lastText = lastText[len(lastText)-200:]
	}
	return lastText
}

// ExtractLastUserPrompt returns the last user message from the transcript.
func (p *ClaudeTranscriptParser) ExtractLastUserPrompt(path string) string {
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
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["type"] != "user" {
			continue
		}
		msg, ok := entry["message"].(map[string]any)
		if !ok {
			continue
		}
		switch c := msg["content"].(type) {
		case string:
			if c != "" {
				lastPrompt = c
			}
		case []any:
			for _, block := range c {
				if m, ok := block.(map[string]any); ok {
					if t, ok := m["text"].(string); ok && t != "" {
						lastPrompt = t
					}
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

// ExtractActivityFeed builds a timeline of tool calls and text from the last user turn.
func (p *ClaudeTranscriptParser) ExtractActivityFeed(path string) []ActivityItem {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	offset := info.Size() - 256*1024
	if offset < 0 {
		offset = 0
	}
	f.Seek(offset, 0)
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	type rawEntry struct {
		Type    string `json:"type"`
		Message struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
		Timestamp string `json:"timestamp"`
	}

	var entries []rawEntry
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var e rawEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			entries = append(entries, e)
		}
	}

	lastUserIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "user" {
			lastUserIdx = i
			break
		}
	}

	var feed []ActivityItem
	startIdx := lastUserIdx + 1
	if startIdx < 0 {
		startIdx = 0
	}

	for _, e := range entries[startIdx:] {
		if e.Type != "assistant" {
			continue
		}
		ts := ""
		if e.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil {
				ts = t.Local().Format("15:04:05")
			} else if t, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
				ts = t.Local().Format("15:04:05")
			}
		}

		for _, raw := range e.Message.Content {
			var block map[string]any
			if json.Unmarshal(raw, &block) != nil {
				continue
			}
			btype, _ := block["type"].(string)
			switch btype {
			case "thinking":
				if t, ok := block["thinking"].(string); ok && t != "" {
					text := t
					if len(text) > 150 {
						text = text[len(text)-150:]
					}
					feed = append(feed, ActivityItem{Time: ts, Type: "thinking", Text: text})
				}
			case "text":
				if t, ok := block["text"].(string); ok && t != "" {
					for _, line := range strings.Split(t, "\n") {
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
			case "tool_use":
				name, _ := block["name"].(string)
				input := ""
				if m, ok := block["input"].(map[string]any); ok {
					for _, key := range []string{"command", "file_path", "pattern", "query", "description", "prompt"} {
						if v, ok := m[key]; ok {
							input = truncateStr(fmt.Sprintf("%v", v), 80)
							break
						}
					}
				}
				feed = append(feed, ActivityItem{Time: ts, Type: "tool", Text: name + ": " + input})
			}
		}
	}

	if len(feed) > 30 {
		feed = feed[len(feed)-30:]
	}
	return feed
}

// IsInterrupted checks if the last entry shows "[Request interrupted by user]".
func (p *ClaudeTranscriptParser) IsInterrupted(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	info, _ := f.Stat()
	if info == nil {
		return false
	}
	offset := info.Size() - 4096
	if offset < 0 {
		offset = 0
	}
	f.Seek(offset, 0)
	data, _ := io.ReadAll(f)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return false
	}
	lastLine := lines[len(lines)-1]

	var entry struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(lastLine), &entry) != nil {
		return false
	}
	if entry.Type == "user" && len(entry.Message.Content) > 0 {
		return strings.Contains(entry.Message.Content[0].Text, "interrupted by user")
	}
	return false
}

// BatchExtract reads the transcript tail once and extracts all fields in a single pass.
func (p *ClaudeTranscriptParser) BatchExtract(path string) BatchResult {
	if path == "" {
		return BatchResult{}
	}

	data := readTail(path, 256*1024)
	if data == nil {
		return BatchResult{}
	}

	var result BatchResult
	var lastTokens int
	var model string
	var lastUserIdx int = -1
	var lastTraceText string

	type rawEntry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
	}

	type entryData struct {
		raw       map[string]any
		entryType string
		timestamp string
	}

	var entries []entryData
	afterCompact := string(data)
	if idx := strings.LastIndex(afterCompact, `"compact_boundary"`); idx >= 0 {
		afterCompact = afterCompact[idx:]
	}

	for _, line := range strings.Split(afterCompact, "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		entryType, _ := raw["type"].(string)
		timestamp, _ := raw["timestamp"].(string)
		entries = append(entries, entryData{raw: raw, entryType: entryType, timestamp: timestamp})
	}

	for i, e := range entries {
		switch e.entryType {
		case "user":
			lastUserIdx = i
			msg, ok := e.raw["message"].(map[string]any)
			if !ok {
				continue
			}
			var text string
			switch c := msg["content"].(type) {
			case string:
				text = c
			case []any:
				for _, block := range c {
					if m, ok := block.(map[string]any); ok {
						if t, ok := m["text"].(string); ok && t != "" {
							text = t
						}
					}
				}
			}
			if text != "" {
				r := []rune(text)
				if len(r) > 200 {
					result.LastUserPrompt = string(r[:200])
				} else {
					result.LastUserPrompt = text
				}
			}

		case "assistant":
			msg, ok := e.raw["message"].(map[string]any)
			if !ok {
				continue
			}
			if m, ok := msg["model"].(string); ok && m != "" {
				model = m
			}
			if usage, ok := msg["usage"].(map[string]any); ok {
				total := 0
				if v, ok := usage["input_tokens"].(float64); ok {
					total += int(v)
				}
				if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
					total += int(v)
				}
				if v, ok := usage["cache_read_input_tokens"].(float64); ok {
					total += int(v)
				}
				if total > 100 {
					lastTokens = total
				}
			}
			content, ok := msg["content"].([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				m, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok && t != "" {
						lastTraceText = t
						if len(t) > 500 {
							result.LastSummary = t[:500]
						} else {
							result.LastSummary = t
						}
					}
				}
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
	window := 200000
	if strings.Contains(model, "opus") {
		window = 1000000
	}
	result.ContextUsage = ContextUsage{Tokens: lastTokens, Window: window}

	// Interrupt detection: check last entry
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		if last.entryType == "user" {
			msg, _ := last.raw["message"].(map[string]any)
			if msg != nil {
				if content, ok := msg["content"].([]any); ok && len(content) > 0 {
					if m, ok := content[0].(map[string]any); ok {
						if t, ok := m["text"].(string); ok {
							result.IsInterrupted = strings.Contains(t, "interrupted by user")
						}
					}
				}
			}
		}
	}

	// Activity feed: entries after lastUserIdx
	startIdx := lastUserIdx + 1
	if startIdx < 0 {
		startIdx = 0
	}
	for _, e := range entries[startIdx:] {
		if e.entryType != "assistant" {
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
		msg, _ := e.raw["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].([]any)
		for _, block := range content {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			btype, _ := m["type"].(string)
			switch btype {
			case "thinking":
				if t, ok := m["thinking"].(string); ok && t != "" {
					text := t
					if len(text) > 150 {
						text = text[len(text)-150:]
					}
					result.ActivityFeed = append(result.ActivityFeed, ActivityItem{Time: ts, Type: "thinking", Text: text})
				}
			case "text":
				if t, ok := m["text"].(string); ok && t != "" {
					for _, line := range strings.Split(t, "\n") {
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
			case "tool_use":
				name, _ := m["name"].(string)
				input := ""
				if inp, ok := m["input"].(map[string]any); ok {
					for _, key := range []string{"command", "file_path", "pattern", "query", "description", "prompt"} {
						if v, ok := inp[key]; ok {
							input = truncateStr(fmt.Sprintf("%v", v), 80)
							break
						}
					}
				}
				result.ActivityFeed = append(result.ActivityFeed, ActivityItem{Time: ts, Type: "tool", Text: name + ": " + input})
			}
		}
	}
	if len(result.ActivityFeed) > 30 {
		result.ActivityFeed = result.ActivityFeed[len(result.ActivityFeed)-30:]
	}

	return result
}

// --- internal helpers for Claude parser ---

func (p *ClaudeTranscriptParser) parseUserEntry(raw map[string]any, uuid, timestamp string) *TranscriptEntry {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return nil
	}

	entry := &TranscriptEntry{
		UUID:      uuid,
		Type:      "user",
		Timestamp: timestamp,
	}

	switch c := msg["content"].(type) {
	case string:
		entry.Content = c
	case []any:
		var texts []string
		for _, block := range c {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := m["type"].(string)
			if blockType == "tool_result" {
				// tool_result blocks are handled separately
			} else if blockType == "text" {
				if t, ok := m["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
		if len(texts) > 0 {
			entry.Content = strings.Join(texts, "\n")
		} else {
			return nil
		}
	}

	if entry.Content == "" {
		return nil
	}
	return entry
}

func (p *ClaudeTranscriptParser) parseAssistantEntry(raw map[string]any, uuid, timestamp string) *TranscriptEntry {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return nil
	}

	entry := &TranscriptEntry{
		UUID:      uuid,
		Type:      "assistant",
		Timestamp: timestamp,
	}

	if m, ok := msg["model"].(string); ok {
		entry.Model = m
	}

	if usage, ok := msg["usage"].(map[string]any); ok {
		tokens := &TokenInfo{}
		if v, ok := usage["input_tokens"].(float64); ok {
			tokens.Input = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			tokens.Output = int(v)
		}
		if v, ok := usage["cache_read_input_tokens"].(float64); ok {
			tokens.CacheRead = int(v)
		}
		if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
			tokens.CacheCreate = int(v)
		}
		if tokens.Input > 0 || tokens.Output > 0 {
			entry.Tokens = tokens
		}
	}

	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}

	for _, block := range content {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := m["type"].(string)
		switch blockType {
		case "text":
			if t, ok := m["text"].(string); ok && t != "" {
				entry.Blocks = append(entry.Blocks, ContentBlock{Type: "text", Text: t})
			}
		case "thinking":
			if t, ok := m["thinking"].(string); ok && t != "" {
				entry.Blocks = append(entry.Blocks, ContentBlock{Type: "thinking", Text: t})
			}
		case "tool_use":
			blockID, _ := m["id"].(string)
			name, _ := m["name"].(string)
			inputSummary := ""
			if input, ok := m["input"].(map[string]any); ok {
				j, _ := json.Marshal(input)
				inputSummary = string(j)
				if len(inputSummary) > 2000 {
					inputSummary = inputSummary[:2000] + "..."
				}
			}
			entry.Blocks = append(entry.Blocks, ContentBlock{Type: "tool_use", ID: blockID, Name: name, Input: inputSummary})
		}
	}

	if len(entry.Blocks) == 0 {
		return nil
	}
	return entry
}

// --- shared utility functions ---

// readHead reads the first n bytes of a file.
func readHead(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	buf := make([]byte, n)
	nRead, err := f.Read(buf)
	if err != nil && nRead == 0 {
		return nil
	}
	return buf[:nRead]
}

// readTail reads the last n bytes of a file.
func readTail(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	offset := info.Size() - n
	if offset < 0 {
		offset = 0
	}
	f.Seek(offset, 0)

	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return data
}

// truncateStr truncates a string to maxLen characters.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
