//go:build windows

package localapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pokegents/dashboard/server/mcpbridge"
	d "pokegents/dashboard/server/taskdomain"
)

func mcpOwnerRequest(t *testing.T, s *Server, method string, input any) (*httptest.ResponseRecorder, mcpSettings) {
	t.Helper()
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	r := httptest.NewRequest(method, s.Origin+"/api/mcp/settings", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Pokegents-Token", s.Token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var result mcpSettings
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return w, result
}

func mcpToolRequest(t *testing.T, s *Server, token, name string, input any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(input)
	r := httptest.NewRequest("POST", s.Origin+"/api/mcp/tools/"+name, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Pokegents-MCP-Token", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestMCPScopesRevocationReplayAndDomainGates(t *testing.T) {
	s := newHTTPTestServer(t)
	profile := filepath.Join(t.TempDir(), "profile")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureMCP(profile, exe); err != nil {
		t.Fatal(err)
	}
	w, settings := mcpOwnerRequest(t, s, "GET", nil)
	if w.Code != 200 || !settings.Supported || settings.Enabled || settings.RegistrationPrompt != "" {
		t.Fatal("MCP must begin disabled")
	}
	if got := mcpToolRequest(t, s, "", "board_list", map[string]any{}); got.Code == 200 {
		t.Fatal("unauthenticated MCP read accepted")
	}
	enableRead := map[string]any{"request_id": d.ID(), "enabled": true, "scope": "read"}
	w, settings = mcpOwnerRequest(t, s, "POST", enableRead)
	if w.Code != 200 || !settings.Enabled || len(settings.Tools) != 4 {
		t.Fatalf("failed to enable read-only MCP status=%d response=%s", w.Code, w.Body.String())
	}
	readConnection, err := mcpbridge.ReadConnection(settings.ConnectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), readConnection.Token) || strings.Contains(w.Body.String(), s.Token) {
		t.Fatal("credential exposed in settings/prompt")
	}
	if !strings.Contains(settings.RegistrationPrompt, `"--connection"`) || !strings.Contains(settings.RegistrationPrompt, "board_list") {
		t.Fatal("registration prompt missing stdio/smoke instructions")
	}
	if got := mcpToolRequest(t, s, readConnection.Token, "board_list", map[string]any{}); got.Code != 200 {
		t.Fatal("authenticated MCP read failed")
	}
	if got := mcpToolRequest(t, s, readConnection.Token, "task_create", map[string]any{}); got.Code != 403 {
		t.Fatal("read credential allowed a write")
	}
	// Neither credential can be substituted for the other API boundary.
	if got := mcpToolRequest(t, s, s.Token, "board_list", map[string]any{}); got.Code == 200 {
		t.Fatal("UI credential accepted as MCP credential")
	}
	r := httptest.NewRequest("GET", s.Origin+"/api/snapshot", nil)
	r.Header.Set("X-Pokegents-MCP-Token", readConnection.Token)
	out := httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code == 200 {
		t.Fatal("MCP credential accepted for generic UI API")
	}
	r = httptest.NewRequest("GET", s.Origin+"/api/mcp/settings", nil)
	r.Header.Set("X-Pokegents-MCP-Token", readConnection.Token)
	out = httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code == 200 {
		t.Fatal("MCP credential accepted for owner settings")
	}
	_, settings = mcpOwnerRequest(t, s, "POST", map[string]any{"request_id": d.ID(), "enabled": true, "scope": "task-write"})
	if !settings.Enabled || len(settings.Tools) != 11 {
		t.Fatal("task-write MCP not enabled")
	}
	connection, err := mcpbridge.ReadConnection(settings.ConnectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if connection.Token == readConnection.Token {
		t.Fatal("scope change did not rotate credential")
	}
	if got := mcpToolRequest(t, s, readConnection.Token, "board_list", map[string]any{}); got.Code == 200 {
		t.Fatal("old scope credential remains valid")
	}
	projectInput, _ := json.Marshal(map[string]any{"name": "MCP合成プロジェクト", "path": t.TempDir()})
	projectRaw, err := s.Service.Execute(context.Background(), Command{RequestID: d.ID(), Action: "project.create", Data: projectInput})
	if err != nil {
		t.Fatal(err)
	}
	var project d.Project
	if err = json.Unmarshal(projectRaw, &project); err != nil {
		t.Fatal(err)
	}
	spec := d.Spec{ProjectID: project.ID, Objective: "MCP合成タスク", AllowedScope: "試験フォルダーのみ", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "evidence", Procedure: "合成試験", Expected: "一致", Required: true}}}
	create := map[string]any{"request_id": d.ID(), "spec": spec, "agent_profile": map[string]string{"name": "MCP検証係", "sprite": "pikachu"}}
	created := mcpToolRequest(t, s, connection.Token, "task_create", create)
	if created.Code != 200 {
		t.Fatalf("create status=%d", created.Code)
	}
	var task d.Task
	if err = json.Unmarshal(created.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	duplicate := mcpToolRequest(t, s, connection.Token, "task_create", create)
	if duplicate.Code != 200 || duplicate.Body.String() != created.Body.String() {
		t.Fatal("request replay created another task")
	}
	for _, name := range []string{"task_get", "task_context", "task_messages"} {
		if got := mcpToolRequest(t, s, connection.Token, name, map[string]any{"task_id": task.ID}); got.Code != 200 {
			t.Fatalf("%s status=%d", name, got.Code)
		}
	}
	stale := mcpToolRequest(t, s, connection.Token, "task_ready", map[string]any{"request_id": d.ID(), "task_id": task.ID, "expected_version": task.Version + 1})
	if stale.Code == 200 || !strings.Contains(stale.Body.String(), "STALE_VERSION") {
		t.Fatal("stale task version accepted")
	}
	ready := mcpToolRequest(t, s, connection.Token, "task_ready", map[string]any{"request_id": d.ID(), "task_id": task.ID, "expected_version": task.Version})
	if ready.Code != 200 {
		t.Fatal("task readiness failed")
	}
	if err = json.Unmarshal(ready.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	start := mcpToolRequest(t, s, connection.Token, "task_start", map[string]any{"request_id": d.ID(), "task_id": task.ID, "expected_version": task.Version, "input": "試験"})
	if start.Code == 200 || !strings.Contains(start.Body.String(), "RUNTIME_GATE_CLOSED") {
		t.Fatal("MCP bypassed runtime acceptance gate")
	}
	for _, name := range []string{"task.review", "approval.respond", "recovery.confirm", "execute", "task_claim"} {
		if got := mcpToolRequest(t, s, connection.Token, name, map[string]any{}); got.Code != 404 {
			t.Fatalf("unexposed operation %s status=%d", name, got.Code)
		}
	}
	r = httptest.NewRequest("POST", s.Origin+"/api/mcp/tools/board_list", strings.NewReader(`{}`))
	r.Header.Set("X-Pokegents-MCP-Token", connection.Token)
	r.Header.Set("Origin", "https://malicious.invalid")
	out = httptest.NewRecorder()
	s.ServeHTTP(out, r)
	if out.Code != 403 {
		t.Fatal("foreign Origin accepted for MCP")
	}
	_, settings = mcpOwnerRequest(t, s, "POST", map[string]any{"request_id": d.ID(), "enabled": false, "scope": "read"})
	if settings.Enabled {
		t.Fatal("MCP revoke failed")
	}
	if got := mcpToolRequest(t, s, connection.Token, "board_list", map[string]any{}); got.Code == 200 {
		t.Fatal("revoked credential remains valid")
	}
	_, settings = mcpOwnerRequest(t, s, "POST", enableRead)
	if settings.Enabled {
		t.Fatal("replayed old enable request resurrected revoked access")
	}
	state, err := s.Service.Store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 1 || len(state.Runs) != 0 {
		t.Fatal("unexpected task or runtime side effects")
	}
}

func TestMCPReloadUpdatesPortButRejectsUncommittedCredential(t *testing.T) {
	s := newHTTPTestServer(t)
	profile := filepath.Join(t.TempDir(), "profile")
	exe, _ := os.Executable()
	if err := s.ConfigureMCP(profile, exe); err != nil {
		t.Fatal(err)
	}
	_, settings := mcpOwnerRequest(t, s, "POST", map[string]any{"request_id": d.ID(), "enabled": true, "scope": "read"})
	old, err := mcpbridge.ReadConnection(settings.ConnectionPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewServer(s.Service, s.assets)
	if err != nil {
		t.Fatal(err)
	}
	defer second.listener.Close()
	if err := second.ConfigureMCP(profile, exe); err != nil {
		t.Fatal(err)
	}
	fresh, err := mcpbridge.ReadConnection(settings.ConnectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !second.mcp.enabled || fresh.Origin != second.Origin || fresh.Token != old.Token {
		t.Fatal("restart failed to update ephemeral port")
	}
	fresh.Token = strings.Repeat("ef", 32)
	if err := mcpbridge.WriteConnection(settings.ConnectionPath, fresh); err != nil {
		t.Fatal(err)
	}
	third, err := NewServer(s.Service, s.assets)
	if err != nil {
		t.Fatal(err)
	}
	defer third.listener.Close()
	if err := third.ConfigureMCP(profile, exe); err != nil {
		t.Fatal(err)
	}
	if third.mcp.enabled {
		t.Fatal("credential without committed DB fingerprint was enabled")
	}
}
