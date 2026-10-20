//go:build windows && integration

package localapp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	d "pokegents/dashboard/server/taskdomain"
)

// This uses the built Windows executable and an isolated synthetic profile. It
// never registers a client or reads the user's MCP/provider credentials.
func TestMCPNativeStdioRoundTrip(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_MCP_EXE")
	if exe == "" {
		t.Skip("set POKEGENTS_TEST_MCP_EXE to the built native executable")
	}
	s := newHTTPTestServer(t)
	if err := s.ConfigureMCP(filepath.Join(t.TempDir(), "profile"), exe); err != nil {
		t.Fatal(err)
	}
	go s.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, scope := range []string{"read", "task-write"} {
		t.Run(scope, func(t *testing.T) {
			_, settings := mcpOwnerRequest(t, s, "POST", map[string]any{"request_id": d.ID(), "enabled": true, "scope": scope})
			cmd := exec.Command(exe, "mcp", "--connection", settings.ConnectionPath)
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			cmd.Stderr = io.Discard
			client := mcp.NewClient(&mcp.Implementation{Name: "pokegents-integration", Version: "0.1.0"}, nil)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil)
			if err != nil {
				t.Fatal("native stdio initialize failed", err)
			}
			defer session.Close()
			tools, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 4
			if scope == "task-write" {
				want = 11
			}
			if len(tools.Tools) != want {
				t.Fatalf("tools=%d want=%d", len(tools.Tools), want)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "board_list", Arguments: map[string]any{}})
			if err != nil || result.IsError {
				t.Fatal("native MCP to authenticated app read failed", err)
			}
			if scope == "task-write" {
				projectJSON, _ := json.Marshal(map[string]any{"name": "MCP stdio合成プロジェクト", "path": t.TempDir()})
				projectRaw, err := s.Service.Execute(ctx, Command{RequestID: d.ID(), Action: "project.create", Data: projectJSON})
				if err != nil {
					t.Fatal(err)
				}
				var project d.Project
				if json.Unmarshal(projectRaw, &project) != nil {
					t.Fatal("invalid project response")
				}
				spec := d.Spec{ProjectID: project.ID, Objective: "stdio経由の合成登録", AllowedScope: "試験フォルダー", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "proof", Procedure: "疎通を確認", Expected: "一致", Required: true}}}
				created, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "task_create", Arguments: map[string]any{"request_id": d.ID(), "spec": spec}})
				if err != nil || created.IsError {
					t.Fatal("native MCP task creation failed", err)
				}
				raw, _ := json.Marshal(created.StructuredContent)
				var task d.Task
				if json.Unmarshal(raw, &task) != nil || task.ID == "" || task.State != "BACKLOG" {
					t.Fatal("native structured task result is invalid")
				}
				got, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "task_get", Arguments: map[string]any{"task_id": task.ID}})
				if err != nil || got.IsError {
					t.Fatal("created task read failed", err)
				}
			}
			_, settings = mcpOwnerRequest(t, s, "POST", map[string]any{"request_id": d.ID(), "enabled": false, "scope": "read"})
			if settings.Enabled {
				t.Fatal("test revocation failed")
			}
			revoked, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "board_list", Arguments: map[string]any{}})
			if err == nil && revoked != nil && !revoked.IsError {
				t.Fatal("native MCP remained usable after revocation")
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != 0 {
				t.Fatal("native MCP did not exit cleanly on stdin close")
			}
			t.Logf("native Windows stdio: scope=%s tools=%d initialize/list/read/revoke/close verified", scope, want)
		})
	}
}
