package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type UsageLogger struct {
	dir string
	mu  sync.Mutex
	f   *os.File
	day string // "2006-01-02" — rotates when day changes
}

func NewUsageLogger(dataDir string) *UsageLogger {
	dir := filepath.Join(dataDir, "usage")
	_ = os.MkdirAll(dir, 0o755)
	return &UsageLogger{dir: dir}
}

type UsageEntry struct {
	Timestamp    string `json:"ts"`
	Event        string `json:"event"`
	PokegentID   string `json:"pgid"`
	AgentName    string `json:"agent"`
	Model        string `json:"model,omitempty"`
	Profile      string `json:"profile,omitempty"`

	// turn_end fields
	Prompt       string `json:"prompt,omitempty"`
	PromptKind   string `json:"prompt_kind,omitempty"` // "user", "nudge", "system"
	DurationMs   int64  `json:"duration_ms,omitempty"`
	TokensBefore int    `json:"tokens_before,omitempty"`
	TokensAfter  int    `json:"tokens_after,omitempty"`
	TokensDelta  int    `json:"tokens_delta,omitempty"`
	TurnID       uint64 `json:"turn_id,omitempty"`
	Error        string `json:"error,omitempty"`

	// usage_update fields
	ContextUsed   int     `json:"context_used,omitempty"`
	ContextWindow int     `json:"context_window,omitempty"`
	CostUSD       float64 `json:"cost_usd,omitempty"`

	// result_meta fields
	NumTurns      int                    `json:"num_turns,omitempty"`
	DurationApiMs int                    `json:"duration_api_ms,omitempty"`
	ModelUsage    map[string]interface{} `json:"model_usage,omitempty"`

	// mcp fields
	ToolName   string `json:"tool_name,omitempty"`
	ToolTarget string `json:"tool_target,omitempty"`
}

func classifyPrompt(text string) string {
	t := strings.TrimSpace(strings.ToLower(text))
	if t == "check messages" {
		return "nudge"
	}
	if strings.HasPrefix(t, "/") {
		return "slash"
	}
	return "user"
}

func (l *UsageLogger) Log(e UsageEntry) {
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	today := time.Now().UTC().Format("2006-01-02")
	if l.f == nil || l.day != today {
		if l.f != nil {
			l.f.Close()
		}
		path := filepath.Join(l.dir, today+".jsonl")
		l.f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			l.f = nil
			return
		}
		l.day = today
	}
	_, _ = l.f.Write(data)
}

func (l *UsageLogger) LogTurnEnd(pgid, agentName, profile, model, prompt string, turnID uint64, tokensBefore, tokensAfter int, duration time.Duration, turnErr error) {
	e := UsageEntry{
		Event:        "turn_end",
		PokegentID:   pgid,
		AgentName:    agentName,
		Model:        model,
		Profile:      profile,
		Prompt:       truncateUsage(prompt, 200),
		PromptKind:   classifyPrompt(prompt),
		DurationMs:   duration.Milliseconds(),
		TokensBefore: tokensBefore,
		TokensAfter:  tokensAfter,
		TokensDelta:  tokensAfter - tokensBefore,
		TurnID:       turnID,
	}
	if turnErr != nil {
		e.Error = turnErr.Error()
	}
	l.Log(e)
}

func (l *UsageLogger) LogUsageUpdate(pgid, agentName string, used, window int, costUSD float64) {
	l.Log(UsageEntry{
		Event:         "usage_update",
		PokegentID:    pgid,
		AgentName:     agentName,
		ContextUsed:   used,
		ContextWindow: window,
		CostUSD:       costUSD,
	})
}

func (l *UsageLogger) LogResultMeta(pgid, agentName string, params json.RawMessage) {
	var meta struct {
		DurationMs    int                    `json:"durationMs"`
		DurationApiMs int                    `json:"durationApiMs"`
		NumTurns      int                    `json:"numTurns"`
		ModelUsage    map[string]interface{} `json:"modelUsage"`
	}
	if json.Unmarshal(params, &meta) != nil {
		return
	}
	l.Log(UsageEntry{
		Event:         "result_meta",
		PokegentID:    pgid,
		AgentName:     agentName,
		DurationMs:    int64(meta.DurationMs),
		DurationApiMs: meta.DurationApiMs,
		NumTurns:      meta.NumTurns,
		ModelUsage:    meta.ModelUsage,
	})
}

func (l *UsageLogger) LogMCPCall(pgid, agentName, toolName, target string) {
	l.Log(UsageEntry{
		Event:      "mcp_call",
		PokegentID: pgid,
		AgentName:  agentName,
		ToolName:   toolName,
		ToolTarget: target,
	})
}

func truncateUsage(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d chars)", len(s))
}
