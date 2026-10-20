//go:build windows && integration

package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pokegents/dashboard/server/processhost"
	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

func TestNativeHistoryListPreviewImportAndDuplicate(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_CODEX_EXE")
	if exe == "" {
		t.Skip("POKEGENTS_TEST_CODEX_EXE is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	workspacePath := filepath.Join(root, "synthetic-workspace")
	if err := os.MkdirAll(workspacePath, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := processhost.Workspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	// Create exactly one synthetic thread in the user's configured Codex profile;
	// history reads below stay scoped to this new thread and synthetic cwd.
	creator, err := runtimeadapter.Start(ctx, exe, workspace.ResolvedPath)
	if err != nil {
		t.Fatal(err)
	}
	threadID, err := creator.Open(ctx, workspace.ResolvedPath, "read-only", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := creator.Turn(ctx, threadID, "Reply with exactly HISTORY_IMPORT_OK.")
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for !completed {
		frame, nextErr := creator.Next(ctx)
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if frame.Method != "turn/completed" {
			continue
		}
		var event struct {
			ThreadID string                      `json:"threadId"`
			Turn     struct{ ID, Status string } `json:"turn"`
		}
		if json.Unmarshal(frame.Params, &event) != nil || event.ThreadID != threadID || event.Turn.ID != turnID {
			continue
		}
		if event.Turn.Status != "completed" {
			t.Fatalf("synthetic turn ended with status %q", event.Turn.Status)
		}
		completed = true
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
	if err := creator.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	closeCancel()
	store, err := sqlstore.Open(filepath.Join(root, "private-store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := &d.Project{ID: "native-history-project", Name: "Synthetic history project", DisplayPath: workspace.DisplayPath, ResolvedPath: workspace.ResolvedPath, Identity: workspace.Identity, Ancestors: workspace.Ancestors}
	var taskID string
	if _, err = store.Update(ctx, "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects[project.ID] = project
		task, createErr := state.Create(d.Spec{ProjectID: project.ID, Objective: "Import synthetic Codex history", Criteria: []d.Criterion{{ID: "history", Procedure: "inspect imported messages", Expected: "synthetic messages appear", Required: true}}, AllowedScope: "private synthetic history fixture", OutOfScope: "provider execution", ApprovalProfile: "read-only"})
		if createErr != nil {
			return nil, nil, createErr
		}
		taskID = task.ID
		return task.ID, nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := New(store, Config{Executable: exe})
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer closeCancel()
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("history manager close: %v", err)
		}
	}()
	info := manager.Info()
	if !info.Available {
		t.Fatalf("native Codex history unavailable: %s", info.Reason)
	}
	page, err := manager.List(ctx, project, "", false)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, thread := range page.Items {
		if thread.ID == threadID {
			found = true
		}
	}
	if !found {
		t.Fatalf("native thread/list did not return synthetic fixture (items=%d)", len(page.Items))
	}
	preview, err := manager.Preview(ctx, project, threadID)
	if err != nil {
		reader, readErr := runtimeadapter.Start(ctx, exe, workspace.ResolvedPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		thread, readErr := reader.ReadHistory(ctx, threadID)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 4*time.Second)
		_ = reader.Close(closeCtx)
		closeCancel()
		if readErr != nil {
			t.Fatal(readErr)
		}
		shape := []string{fmt.Sprintf("historyMode=%q turns=%d", thread.HistoryMode, len(thread.Turns))}
		for i, turn := range thread.Turns {
			shape = append(shape, fmt.Sprintf("turn[%d].status=%q itemsView=%q startedAt=%t items=%d", i, turn.Status, turn.ItemsView, turn.StartedAt != nil, len(turn.Items)))
			for j, raw := range turn.Items {
				var item map[string]json.RawMessage
				_ = json.Unmarshal(raw, &item)
				keys := make([]string, 0, len(item))
				for k := range item {
					keys = append(keys, k)
				}
				shape = append(shape, fmt.Sprintf("item[%d] fields=%v id=%t type=%t", j, keys, len(item["id"]) > 0, len(item["type"]) > 0))
			}
		}
		t.Logf("native history shape (metadata only): %v", shape)
		t.Fatal(err)
	}
	if preview.Thread.ID != threadID || len(preview.Messages) == 0 {
		t.Fatalf("native thread/read produced no importable messages (messages=%d skipped=%d)", len(preview.Messages), len(preview.Skipped))
	}
	state, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := ImportRequest{RequestID: "native-history-import-001", TaskID: taskID, ExpectedVersion: state.Tasks[taskID].Version, ThreadID: threadID, PreviewSHA256: preview.SHA256}
	first, err := manager.Import(ctx, project, request)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "native-history-import-002"
	request.ExpectedVersion = state.Tasks[taskID].Version
	second, err := manager.Import(ctx, project, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Imported != len(preview.Messages) || second.Imported != 0 || second.Duplicates != len(preview.Messages) {
		t.Fatalf("unexpected import counts: first=%d duplicate=%d second=%d/%d", first.Imported, first.Duplicates, second.Imported, second.Duplicates)
	}
	var provenance, messages, fts int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM import_sources`).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE task_id=?`, taskID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_fts`).Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if messages != len(preview.Messages) || fts < messages || provenance != 1 {
		t.Fatalf("import persistence counts: messages=%d fts=%d provenance=%d", messages, fts, provenance)
	}
	t.Logf("native Codex history: list/read/import/duplicate passed; listed=%d preview=%d imported=%d duplicates=%d messages=%d fts=%d provenance=%d", len(page.Items), len(preview.Messages), first.Imported, second.Duplicates, messages, fts, provenance)
}
