package mcpbridge

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectTestClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestToolListsRespectWritableMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writable bool
		want     int
	}{{"read only", false, 4}, {"writable", true, 11}} {
		t.Run(tc.name, func(t *testing.T) {
			session := connectTestClient(t, NewServer(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			}, tc.writable))
			listed, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			if len(listed.Tools) != tc.want {
				t.Fatalf("got %d tools, want %d", len(listed.Tools), tc.want)
			}
		})
	}
}

func TestToolInputIsForwardedAndStructuredOutputReturned(t *testing.T) {
	var gotName string
	var gotInput map[string]any
	session := connectTestClient(t, NewServer(func(_ context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
		gotName = name
		if err := json.Unmarshal(input, &gotInput); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"task_id":"task-1","version":3}`), nil
	}, true))
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "task_get", Arguments: map[string]any{"task_id": "task-1"},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %v", result.GetError())
	}
	if gotName != "task_get" || gotInput["task_id"] != "task-1" {
		t.Fatalf("gateway received %q %#v", gotName, gotInput)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["task_id"] != "task-1" || structured["version"] != float64(3) {
		t.Fatalf("structured content = %#v", result.StructuredContent)
	}
}

func TestSchemaAndUnknownToolRejectInvalidCalls(t *testing.T) {
	calls := 0
	session := connectTestClient(t, NewServer(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{}`), nil
	}, false))

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "task_get", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("invalid tool call returned protocol error: %v", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("missing required task_id was accepted")
	}
	_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "not_a_tool", Arguments: map[string]any{}})
	if err == nil {
		t.Fatal("unknown tool was accepted")
	}
	if calls != 0 {
		t.Fatalf("gateway called %d times for invalid tools", calls)
	}
}

func TestReadToolAnnotationsAreReadOnly(t *testing.T) {
	session := connectTestClient(t, NewServer(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}, true))
	listed, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	wantReadOnly := map[string]bool{"board_list": true, "task_get": true, "task_context": true, "task_messages": true}
	for _, tool := range listed.Tools {
		if want, ok := wantReadOnly[tool.Name]; ok && (tool.Annotations == nil || tool.Annotations.ReadOnlyHint != want) {
			t.Errorf("%s annotations = %#v", tool.Name, tool.Annotations)
		}
		if tool.Name == "task_start" || tool.Name == "task_cancel" {
			if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("%s missing destructive hint", tool.Name)
			}
		}
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"board_list", "task_get", "task_context", "task_messages", "task_create", "task_revise", "task_ready", "task_start", "task_cancel", "task_submit", "context_save"}
	sort.Strings(want)
	if len(names) != len(want) {
		t.Fatalf("tool names = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tool names = %v", names)
		}
	}
}
