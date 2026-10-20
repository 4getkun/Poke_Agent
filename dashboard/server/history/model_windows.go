//go:build windows

// Package history imports explicitly selected provider text without adopting a
// provider process, resuming a turn, or modifying the provider's stored files.
package history

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"pokegents/dashboard/server/runtimeadapter"
	d "pokegents/dashboard/server/taskdomain"
)

const ImporterVersion = "codex-api-1"
const maxMessages = 10000
const maxBody = 1 << 20
const maxTotal = 8 << 20

type Summary struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Preview     string `json:"preview"`
	ProjectPath string `json:"project_path"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	Source      string `json:"source"`
	Status      string `json:"status"`
}
type Message struct {
	Key        string `json:"key"`
	Role       string `json:"role"`
	Body       string `json:"body"`
	At         string `json:"at"`
	TimeSource string `json:"time_source"`
}
type Skipped struct {
	Position string `json:"position"`
	Reason   string `json:"reason"`
	Kind     string `json:"kind"`
}
type Preview struct {
	Thread   Summary   `json:"thread"`
	SHA256   string    `json:"sha256"`
	Messages []Message `json:"messages"`
	Skipped  []Skipped `json:"skipped"`
	Partial  bool      `json:"partial"`
}
type Page struct {
	Items      []Summary `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}
type ImportRequest struct {
	RequestID       string `json:"request_id"`
	TaskID          string `json:"task_id"`
	ExpectedVersion int64  `json:"expected_version"`
	ThreadID        string `json:"thread_id"`
	PreviewSHA256   string `json:"preview_sha256"`
}
type Report struct {
	TaskID     string    `json:"task_id"`
	SourceID   string    `json:"source_id"`
	Imported   int       `json:"imported"`
	Duplicates int       `json:"duplicates"`
	Skipped    []Skipped `json:"skipped"`
	Partial    bool      `json:"partial"`
}

func uuid(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
func digest(value any) string {
	data, _ := json.Marshal(value)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func sourceKind(raw json.RawMessage) string {
	var kind string
	if json.Unmarshal(raw, &kind) != nil {
		return "unknown"
	}
	return kind
}
func supportedSource(raw json.RawMessage) bool {
	switch sourceKind(raw) {
	case "cli", "vscode", "appServer":
		return true
	}
	return false
}
func summary(thread runtimeadapter.HistoryThread) Summary {
	var status struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(thread.Status, &status)
	title := thread.Preview
	if thread.Name != nil && strings.TrimSpace(*thread.Name) != "" {
		title = *thread.Name
	}
	if title == "" {
		title = thread.ID
	}
	clip := func(s string, n int) string {
		r := []rune(s)
		if len(r) > n {
			return string(r[:n]) + "…"
		}
		return s
	}
	return Summary{ID: thread.ID, Title: clip(title, 160), Preview: clip(thread.Preview, 280), ProjectPath: thread.CWD, CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt, Source: sourceKind(thread.Source), Status: status.Type}
}

func makePreview(thread runtimeadapter.HistoryThread) (Preview, error) {
	if thread.HistoryMode != "" && thread.HistoryMode != "legacy" && !(thread.HistoryMode=="paginated"&&thread.Complete) {
		return Preview{}, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
	}
	preview := Preview{Thread: summary(thread), Messages: []Message{}, Skipped: []Skipped{}}
	total := 0
	keys := map[string]bool{}
	skip := func(position, reason, kind string) {
		preview.Skipped = append(preview.Skipped, Skipped{position, reason, kind})
	}
	for turnIndex, turn := range thread.Turns {
		if len(thread.Turns) > maxMessages || len(preview.Skipped) > maxMessages {
			return Preview{}, d.Fail("HISTORY_LIMIT")
		}
		position := fmt.Sprintf("turn[%d]", turnIndex)
		if turn.ID == "" {
			skip(position, "missing_id", "turn")
			continue
		}
		if turn.Status == "inProgress" {
			skip(position, "incomplete_turn", "turn")
			continue
		}
		if turn.Status != "completed" && turn.Status != "interrupted" && turn.Status != "failed" {
			return Preview{}, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
		}
		if turn.ItemsView != "" && turn.ItemsView != "full" {
			return Preview{}, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
		}
		at := thread.CreatedAt
		timeSource := "session_created"
		if turn.StartedAt != nil {
			at = *turn.StartedAt
			timeSource = "turn_started"
		}
		if at <= 0 {
			return Preview{}, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
		}
		for index, raw := range turn.Items {
			if len(turn.Items) > maxMessages || len(preview.Skipped) > maxMessages {
				return Preview{}, d.Fail("HISTORY_LIMIT")
			}
			position := fmt.Sprintf("turn[%d]/item[%d]", turnIndex, index)
			var item struct {
				ID               string  `json:"id"`
				Type             string  `json:"type"`
				Text             string  `json:"text"`
				AggregatedOutput *string `json:"aggregatedOutput"`
				Content          []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			}
			if json.Unmarshal(raw, &item) != nil {
				return Preview{}, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
			}
			if item.ID == "" {
				skip(position, "missing_id", item.Type)
				continue
			}
			role, body := "", ""
			switch item.Type {
			case "userMessage":
				role = "user"
				var parts []string
				for _, part := range item.Content {
					if part.Type == "text" {
						parts = append(parts, part.Text)
					} else {
						skip(position, "non_text_content", part.Type)
					}
				}
				body = strings.Join(parts, "\n")
			case "agentMessage":
				role, body = "assistant", item.Text
			case "commandExecution":
				role = "tool"
				if item.AggregatedOutput != nil {
					body = *item.AggregatedOutput
				}
			default:
				skip(position, "unsupported_item", item.Type)
				continue
			}
			if body == "" {
				skip(position, "no_text", item.Type)
				continue
			}
			if len(body) > maxBody {
				return Preview{}, d.Fail("HISTORY_LIMIT")
			}
			total += len(body)
			if total > maxTotal || len(preview.Messages) >= maxMessages || len(preview.Skipped) > maxMessages {
				return Preview{}, d.Fail("HISTORY_LIMIT")
			}
			key := turn.ID + "/" + item.ID
			if len(key) > 512 || keys[key] {
				return Preview{}, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
			}
			keys[key] = true
			preview.Messages = append(preview.Messages, Message{key, role, body, time.Unix(at, 0).UTC().Format(time.RFC3339Nano), timeSource})
		}
	}
	if len(preview.Skipped) > maxMessages {
		return Preview{}, d.Fail("HISTORY_LIMIT")
	}
	preview.Partial = len(preview.Skipped) > 0
	preview.SHA256 = digest(struct {
		ID, CWD  string
		Messages []Message
		Skipped  []Skipped
	}{thread.ID, thread.CWD, preview.Messages, preview.Skipped})
	return preview, nil
}
