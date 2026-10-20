//go:build windows && integration

package localapp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

func TestRealCodexTaskLifecycleAndResume(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_CODEX_EXE")
	if exe == "" {
		t.Fatal("explicit native Codex exe required")
	}
	store, err := sqlstore.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Enable only this integration fixture; product capability stays gated.
	service := NewService(store, Capabilities{RuntimePath: exe, RuntimeVersion: runtimeadapter.SupportedVersion, ChatEnabled: true})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	invoke := func(action, taskID string, version int64, data any) json.RawMessage {
		t.Helper()
		body, _ := json.Marshal(data)
		result, err := service.Execute(context.Background(), Command{RequestID: d.ID(), Action: action, TaskID: taskID, ExpectedVersion: version, Data: body})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		return result
	}
	var project d.Project
	if err = json.Unmarshal(invoke("project.create", "", 0, map[string]any{"path": t.TempDir(), "name": "日本語検証"}), &project); err != nil {
		t.Fatal(err)
	}
	spec := d.Spec{ProjectID: project.ID, Objective: "公開の挨拶だけを返答する", AllowedScope: "読み取り専用の挨拶", OutOfScope: "ファイルや外部操作", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "greeting", Procedure: "実Codexストリームと正常終了を観測", Expected: "日本語挨拶", Required: true}}}
	var task d.Task
	if err = json.Unmarshal(invoke("task.create", "", 0, map[string]any{"spec": spec}), &task); err != nil {
		t.Fatal(err)
	}
	invoke("task.ready", task.ID, task.Version, map[string]any{})
	read := func() (*d.Task, *d.Run) {
		t.Helper()
		state, err := store.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		current := state.Tasks[task.ID]
		return current, state.Runs[current.CurrentRunID]
	}
	await := func() (*d.Task, *d.Run) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			current, run := read()
			if run != nil && run.Terminal() && run.WorkspaceQuiet {
				if run.State != "succeeded" {
					t.Fatalf("run state=%s code=%s", run.State, run.Error)
				}
				return current, run
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("run did not finish/quiesce")
		return nil, nil
	}
	current, _ := read()
	invoke("task.start", task.ID, current.Version, map[string]any{"input": "ツールを使わず、日本語で「こんにちは」とだけ返答してください。"})
	current, first := await()
	if current.State != "DOING" || current.SubmissionID != "" {
		t.Fatal("runtime completed task without review")
	}
	invoke("task.retry", task.ID, current.Version, map[string]any{})
	current, _ = read()
	invoke("task.start", task.ID, current.Version, map[string]any{"input": "ツールを使わず、日本語で「ありがとうございます」とだけ返答してください。"})
	current, second := await()
	if first.ID == second.ID || first.ProviderThreadID == "" || first.ProviderThreadID != second.ProviderThreadID || second.Generation <= first.Generation {
		t.Fatal("new run did not resume the previous thread with a new generation")
	}
	page, err := store.Messages(context.Background(), task.ID, "", "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assistant := 0
	for _, m := range page.Items {
		if m.Role == "assistant" && m.Body != "" {
			assistant++
		}
	}
	if assistant < 2 {
		t.Fatalf("assistant messages=%d", assistant)
	}
	submission := d.Submission{TaskID: task.ID, RunID: second.ID, SpecVersion: current.Spec.Version, Summary: "実Codexで2回の日本語応答と正式resumeを観測", Evidence: []d.Evidence{{CriterionID: "greeting", Revision: runtimeadapter.SupportedVersion, Procedure: "TestRealCodexTaskLifecycleAndResume", Result: "日本語ストリームとcompleted、異なるrunの同thread resumeを観測", Passed: true}}}
	invoke("task.submit", task.ID, current.Version, map[string]any{"submission": submission})
	current, _ = read()
	if current.State != "REVIEW" {
		t.Fatal(current.State)
	}
	invoke("task.review", task.ID, current.Version, map[string]any{"submission_id": current.SubmissionID, "decision": "accept"})
	current, _ = read()
	if current.State != "DONE" {
		t.Fatal(current.State)
	}
	t.Logf("native Codex %s: task/run/stream/quiesce/resume/submission/human-review succeeded; assistant messages=%d", runtimeadapter.SupportedVersion, assistant)
}
