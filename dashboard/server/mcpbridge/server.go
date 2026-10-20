package mcpbridge

import (
	"context"
	"encoding/json"
	"errors"

	d "pokegents/dashboard/server/taskdomain"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Gateway forwards an MCP tool name and its validated JSON input to the local app.
type Gateway func(context.Context, string, json.RawMessage) (json.RawMessage, error)

type boardListInput struct {
	ProjectID *string `json:"project_id,omitempty" jsonschema:"Optional project ID filter"`
	State     *string `json:"state,omitempty" jsonschema:"Optional task state filter"`
}
type taskIDInput struct {
	TaskID string `json:"task_id" jsonschema:"Task ID"`
}
type taskMessagesInput struct {
	TaskID string  `json:"task_id" jsonschema:"Task ID"`
	Cursor *string `json:"cursor,omitempty" jsonschema:"Optional page cursor"`
}
type agentProfile struct {
	Name   string `json:"name" jsonschema:"Agent profile name"`
	Sprite string `json:"sprite" jsonschema:"Agent profile sprite"`
}
type taskCreateInput struct {
	RequestID    string        `json:"request_id" jsonschema:"Idempotent request ID"`
	Spec         d.Spec        `json:"spec" jsonschema:"Task specification"`
	AgentProfile *agentProfile `json:"agent_profile,omitempty" jsonschema:"Optional agent profile"`
}
type taskCommandInput struct {
	RequestID       string `json:"request_id" jsonschema:"Idempotent request ID"`
	TaskID          string `json:"task_id" jsonschema:"Task ID"`
	ExpectedVersion int64  `json:"expected_version" jsonschema:"Current task version"`
}
type taskReviseInput struct {
	RequestID       string `json:"request_id" jsonschema:"Idempotent request ID"`
	TaskID          string `json:"task_id" jsonschema:"Task ID"`
	ExpectedVersion int64  `json:"expected_version" jsonschema:"Current task version"`
	Spec            d.Spec `json:"spec" jsonschema:"Replacement task specification"`
}
type taskStartInput struct {
	RequestID       string `json:"request_id" jsonschema:"Idempotent request ID"`
	TaskID          string `json:"task_id" jsonschema:"Task ID"`
	ExpectedVersion int64  `json:"expected_version" jsonschema:"Current task version"`
	Input           string `json:"input" jsonschema:"User input for the run"`
}
type taskSubmitInput struct {
	RequestID       string       `json:"request_id" jsonschema:"Idempotent request ID"`
	TaskID          string       `json:"task_id" jsonschema:"Task ID"`
	ExpectedVersion int64        `json:"expected_version" jsonschema:"Current task version"`
	Submission      d.Submission `json:"submission" jsonschema:"Submission evidence"`
}
type contextSaveInput struct {
	RequestID       string          `json:"request_id" jsonschema:"Idempotent request ID"`
	TaskID          string          `json:"task_id" jsonschema:"Task ID"`
	ExpectedVersion int64           `json:"expected_version" jsonschema:"Current task version"`
	Packet          d.ContextPacket `json:"packet" jsonschema:"Context packet"`
}

var destructive = true

// NewServer exposes the read tools and, when writable, the bounded task tools.
func NewServer(g Gateway, writable bool) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "pokegents", Title: "Pokegents for Windows", Version: "0.1.0-dev"}, &mcp.ServerOptions{
		Instructions: "Pokegentsのプロジェクトとタスクを確認し、作業対象と条件を読み取ってください。仕様の変更・READY化・開始・中止・提出は対応するツールだけを使い、expected_versionを最新値に合わせてください。タスク仕様・run・generationの整合性検査やG2 gateを迂回しないでください。最終レビュー、承認、資格情報管理、任意コマンド実行は人間のGUIに委ねてください。",
	})
	register(s, g, "board_list", "許可されたボードのタスクを一覧表示します。", true, false, boardListInput{})
	register(s, g, "task_get", "タスク仕様・状態・versionを取得します。", true, false, taskIDInput{})
	register(s, g, "task_context", "タスクの作業コンテキストを取得します。", true, false, taskIDInput{})
	register(s, g, "task_messages", "タスクのメッセージ履歴を取得します。", true, false, taskMessagesInput{})
	if writable {
		register(s, g, "task_create", "BACKLOGタスクを作成します。", false, false, taskCreateInput{})
		register(s, g, "task_revise", "現在のversionを指定してBACKLOG仕様を更新します。", false, false, taskReviseInput{})
		register(s, g, "task_ready", "BACKLOGタスクをREADYにします。", false, false, taskCommandInput{})
		register(s, g, "task_start", "READYタスクの実行を開始します。", false, true, taskStartInput{})
		register(s, g, "task_cancel", "現在のtask runを中止します。", false, true, taskCommandInput{})
		register(s, g, "task_submit", "実行結果と受け入れ条件の証拠を人間レビューへ提出します。", false, false, taskSubmitInput{})
		register(s, g, "context_save", "version整合性を保って引き継ぎコンテキストを保存します。", false, false, contextSaveInput{})
	}
	return s
}

func register[I any](s *mcp.Server, g Gateway, name, description string, readOnly, isDestructive bool, input I) {
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: readOnly}
	if isDestructive {
		annotations.DestructiveHint = &destructive
	}
	mcp.AddTool[I, map[string]any](s, &mcp.Tool{Name: name, Title: name, Description: description, Annotations: annotations},
		func(ctx context.Context, _ *mcp.CallToolRequest, in I) (*mcp.CallToolResult, map[string]any, error) {
			if g == nil {
				return nil, nil, errors.New("local app gateway is unavailable")
			}
			payload, err := json.Marshal(in)
			if err != nil {
				return nil, nil, err
			}
			response, err := g(ctx, name, payload)
			if err != nil {
				return nil, nil, err
			}
			var output map[string]any
			if err := json.Unmarshal(response, &output); err != nil {
				return nil, nil, err
			}
			if output == nil {
				return nil, nil, errors.New("gateway must return a JSON object")
			}
			return nil, output, nil
		})
}
