//go:build windows

package localapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/mcpbridge"
	d "pokegents/dashboard/server/taskdomain"
)

var mcpReadTools = []string{"board_list", "task_get", "task_context", "task_messages"}
var mcpWriteTools = []string{"task_create", "task_revise", "task_ready", "task_start", "task_cancel", "task_submit", "context_save"}

type mcpRecord struct {
	Enabled   bool   `json:"enabled"`
	Scope     string `json:"scope"`
	TokenHash string `json:"token_hash,omitempty"`
}

type mcpManager struct {
	mu         sync.RWMutex
	path, exe  string
	connection mcpbridge.Connection
	enabled    bool
}

type mcpSettings struct {
	Supported          bool     `json:"supported"`
	Enabled            bool     `json:"enabled"`
	Scope              string   `json:"scope"`
	ConnectionPath     string   `json:"connection_path"`
	ExecutablePath     string   `json:"executable_path"`
	RegistrationPrompt string   `json:"registration_prompt"`
	Tools              []string `json:"tools"`
}

func mcpFingerprint(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (s *Server) readMCPRecord(ctx context.Context) (mcpRecord, error) {
	record := mcpRecord{Scope: "read"}
	var raw []byte
	err := s.Service.Store.DB().QueryRowContext(ctx, "SELECT payload FROM settings WHERE id='mcp'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	if record.Scope != "read" && record.Scope != "task-write" {
		return record, d.Fail("INVALID_REQUEST")
	}
	return record, nil
}

// ConfigureMCP attaches an optional stdio bridge to this profile's sole API/DB
// owner. It does not enable access or create an external client registration.
func (s *Server) ConfigureMCP(profile, executable string) error {
	if !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Ext(executable), ".exe") {
		return d.Fail("INVALID_REQUEST")
	}
	dir := filepath.Join(profile, "mcp")
	if err := desktop.PrepareProfile(dir); err != nil {
		return err
	}
	s.mcp = &mcpManager{path: filepath.Join(dir, "connection.json"), exe: executable}
	record, err := s.readMCPRecord(context.Background())
	if err != nil {
		return err
	}
	if !record.Enabled {
		return nil
	}
	c, err := mcpbridge.ReadConnection(s.mcp.path)
	if err != nil || mcpFingerprint(c.Token) != record.TokenHash || c.Scope != record.Scope {
		// A missing/incomplete credential disables MCP, never the desktop app.
		return nil
	}
	c.Origin = s.Origin
	if err := mcpbridge.WriteConnection(s.mcp.path, c); err != nil {
		return nil
	}
	s.mcp.connection, s.mcp.enabled = c, true
	return nil
}

func (s *Server) mcpSettingsLocked() mcpSettings {
	if s.mcp == nil {
		return mcpSettings{Scope: "read", Tools: []string{}}
	}
	m := s.mcp
	scope := m.connection.Scope
	if scope == "" {
		scope = "read"
	}
	tools := append([]string{}, mcpReadTools...)
	if scope == "task-write" {
		tools = append(tools, mcpWriteTools...)
	}
	settings := mcpSettings{Supported: true, Enabled: m.enabled, Scope: scope, ConnectionPath: m.path, ExecutablePath: m.exe, Tools: tools}
	if m.enabled {
		config, _ := json.MarshalIndent(map[string]any{"command": m.exe, "args": []string{"mcp", "--connection", m.path}}, "", "  ")
		settings.RegistrationPrompt = "このPCのLLMクライアントに、PokegentsのMCPサーバーを名前 pokegents で登録してください。\n" +
			"stdio設定は次のとおりです。既存のMCP設定を保持し、重複があれば更新してください。クライアントの設定形式を確認して適用してください。\n\n" + string(config) + "\n\n" +
			"Pokegentsアプリを起動してから接続し、tools/list と board_list の読み取りだけで疎通を確認してください。登録確認のためのタスク作成・開始・停止・承認操作は行わないでください。\n" +
			"connection.jsonは専用認証情報です。本文やtokenを表示・コピー・ログ出力・外部送信せず、ファイルパスだけを設定してください。URLやコマンドラインへtokenを入れないでください。\n" +
			"MCPからの実行もPokegentsの版・run・権限・実行ゲートに従います。架空の実行や証拠を作らず、成果の受け入れと権限承認は人間に任せてください。\n" +
			"変更した設定箇所と疎通結果を、秘密を含めず簡潔に報告してください。"
	}
	return settings
}

func (s *Server) serveMCPSettings(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		if r.Method != "GET" {
			writeError(w, d.Fail("NATIVE_UNAVAILABLE"))
			return
		}
		writeJSON(w, s.mcpSettingsLocked())
		return
	}
	m := s.mcp
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method == "GET" {
		writeJSON(w, s.mcpSettingsLocked())
		return
	}
	var input struct {
		RequestID string `json:"request_id"`
		Enabled   bool   `json:"enabled"`
		Scope     string `json:"scope"`
		Rotate    bool   `json:"rotate"`
	}
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if len(input.RequestID) < 8 || len(input.RequestID) > 128 || (input.Scope != "read" && input.Scope != "task-write") {
		writeError(w, d.Fail("INVALID_REQUEST"))
		return
	}
	ctx := r.Context()
	previous, previousEnabled := m.connection, m.enabled
	payload, _ := json.Marshal(input)
	_, err := s.Service.Store.Update(ctx, input.RequestID, append([]byte("mcp.configure:"), payload...), func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		record := mcpRecord{Enabled: input.Enabled, Scope: input.Scope}
		if input.Enabled {
			c := previous
			if !previousEnabled || input.Rotate || c.Scope != input.Scope {
				var token [32]byte
				if _, err := rand.Read(token[:]); err != nil {
					return nil, nil, err
				}
				c = mcpbridge.Connection{Version: 1, Origin: s.Origin, Token: hex.EncodeToString(token[:]), Scope: input.Scope}
			}
			if err := mcpbridge.WriteConnection(m.path, c); err != nil {
				return nil, nil, err
			}
			record.TokenHash = mcpFingerprint(c.Token)
		}
		raw, _ := json.Marshal(record)
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(id,payload) VALUES('mcp',?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", string(raw)); err != nil {
			return nil, nil, err
		}
		return map[string]any{"enabled": input.Enabled, "scope": input.Scope}, []d.Event{{Type: "mcp.settings_changed"}}, nil
	})
	if err != nil {
		if previousEnabled {
			if restoreErr := mcpbridge.WriteConnection(m.path, previous); restoreErr != nil {
				m.enabled = false
			}
		}
		writeError(w, err)
		return
	}
	// Always reload the current DB record: a replayed old request must not
	// re-enable an integration that was subsequently revoked.
	record, err := s.readMCPRecord(ctx)
	if err != nil {
		m.enabled = false
		writeError(w, err)
		return
	}
	m.enabled = false
	m.connection = mcpbridge.Connection{Scope: record.Scope}
	if record.Enabled {
		c, err := mcpbridge.ReadConnection(m.path)
		if err != nil || c.Scope != record.Scope || mcpFingerprint(c.Token) != record.TokenHash {
			writeError(w, d.Fail("AUTH_REQUIRED"))
			return
		}
		m.connection, m.enabled = c, true
	} else {
		// The persisted revocation is effective even if an antivirus temporarily
		// holds the now-useless credential file open.
		_ = os.Remove(m.path)
	}
	writeJSON(w, s.mcpSettingsLocked())
}

func containsMCPTool(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func (s *Server) serveMCPTool(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeError(w, d.Fail("AUTH_REQUIRED"))
		return
	}
	m := s.mcp
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.enabled || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Pokegents-MCP-Token")), []byte(m.connection.Token)) != 1 {
		writeError(w, d.Fail("AUTH_REQUIRED"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/mcp/tools/")
	isRead := containsMCPTool(mcpReadTools, name)
	if r.Method != "POST" || (!isRead && !containsMCPTool(mcpWriteTools, name)) {
		http.NotFound(w, r)
		return
	}
	if !isRead && m.connection.Scope != "task-write" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]string{"code": "MCP_READ_ONLY"})
		return
	}
	var input map[string]json.RawMessage
	if err := readJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.callMCPTool(r.Context(), name, input)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(result)
}

func (s *Server) callMCPTool(ctx context.Context, name string, input map[string]json.RawMessage) (json.RawMessage, error) {
	getString := func(key string) string { var value string; _ = json.Unmarshal(input[key], &value); return value }
	taskID := getString("task_id")
	if containsMCPTool(mcpReadTools, name) {
		state, err := s.Service.Store.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		if name == "board_list" {
			projects, roles, tasks := []*d.Project{}, []*d.Role{}, []*d.Task{}
			for _, p := range state.Projects {
				projects = append(projects, p)
			}
			for _, role := range state.Roles {
				roles = append(roles, role)
			}
			for _, task := range state.Tasks {
				if project := getString("project_id"); project != "" && task.Spec.ProjectID != project {
					continue
				}
				if status := getString("state"); status != "" && task.State != status {
					continue
				}
				tasks = append(tasks, task)
			}
			sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
			sort.Slice(roles, func(i, j int) bool { return roles[i].ID < roles[j].ID })
			sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
			return json.Marshal(map[string]any{"projects": projects, "roles": roles, "tasks": tasks, "capabilities": s.Service.Capabilities})
		}
		task := state.Tasks[taskID]
		if task == nil {
			return nil, d.Fail("NOT_FOUND")
		}
		switch name {
		case "task_get":
			return json.Marshal(map[string]any{"task": task, "run": state.Runs[task.CurrentRunID], "submission": state.Submissions[task.SubmissionID]})
		case "task_context":
			return json.Marshal(map[string]any{"task": task, "context": state.Contexts[task.ID]})
		case "task_messages":
			cursor, err := parseCursor(getString("cursor"))
			if err != nil {
				return nil, err
			}
			page, err := s.Service.Store.Messages(ctx, taskID, "", "", cursor, 50)
			if err != nil {
				return nil, err
			}
			var next any
			if page.NextCursor > 0 {
				next = fmt.Sprint(page.NextCursor)
			}
			return json.Marshal(map[string]any{"items": page.Items, "next_cursor": next, "interrupted": page.Interrupted})
		}
	}
	// The allow-list is independent of the generic UI command endpoint. In
	// particular, approval.respond/task.review/recovery.confirm are never exposed.
	actions := map[string]string{"task_create": "task.create", "task_revise": "task.revise", "task_ready": "task.ready", "task_start": "task.start", "task_cancel": "task.cancel", "task_submit": "task.submit", "context_save": "context.save"}
	action := actions[name]
	if action == "" {
		return nil, d.Fail("INVALID_REQUEST")
	}
	var version int64
	if raw, ok := input["expected_version"]; ok {
		if json.Unmarshal(raw, &version) != nil {
			return nil, d.Fail("INVALID_REQUEST")
		}
	}
	data := map[string]json.RawMessage{}
	for _, key := range []string{"spec", "submission", "packet", "input"} {
		if value, ok := input[key]; ok {
			data[key] = value
		}
	}
	if name == "task_create" {
		if value, ok := input["agent_profile"]; ok {
			data["agent"] = value
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return s.Service.Execute(ctx, Command{RequestID: getString("request_id"), Action: action, TaskID: taskID, ExpectedVersion: version, Data: raw})
}
