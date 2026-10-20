//go:build windows

package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pokegents/dashboard/server/processhost"
	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func textItem(t *testing.T, id, kind, text string) json.RawMessage {
	t.Helper()
	return rawJSON(t, map[string]any{"id": id, "type": kind, "text": text})
}

func userTextItem(t *testing.T, id, text string) json.RawMessage {
	t.Helper()
	return rawJSON(t, map[string]any{"id": id, "type": "userMessage", "content": []any{map[string]string{"type": "text", "text": text}}})
}

func fixtureThread(cwd string) runtimeadapter.HistoryThread {
	started := int64(1_700_000_000)
	return runtimeadapter.HistoryThread{
		ID: "01234567-89ab-cdef-0123-456789abcdef", Preview: "synthetic history", CWD: cwd,
		CreatedAt: started, UpdatedAt: started + 10, Source: json.RawMessage(`"cli"`),
		Status: json.RawMessage(`{"type":"idle"}`), HistoryMode: "legacy",
		Turns: []runtimeadapter.HistoryTurn{{ID: "turn-1", Status: "completed", ItemsView: "full", StartedAt: &started}},
	}
}

func TestMakePreviewSkipsUnsupportedAndIncompleteContent(t *testing.T) {
	thread := fixtureThread(`C:\work`)
	thread.Turns[0].Items = []json.RawMessage{
		rawJSON(t, map[string]any{"id": "u1", "type": "userMessage", "content": []any{
			map[string]string{"type": "text", "text": "hello"}, map[string]string{"type": "image", "text": ""},
		}}),
		textItem(t, "a1", "agentMessage", "answer"),
		rawJSON(t, map[string]any{"id": "c1", "type": "commandExecution", "aggregatedOutput": "done"}),
		textItem(t, "f1", "fileChange", ""),
	}
	incomplete := thread.Turns[0]
	incomplete.ID, incomplete.Status = "turn-2", "inProgress"
	thread.Turns = append(thread.Turns, incomplete, runtimeadapter.HistoryTurn{Status: "completed"})
	preview, err := makePreview(thread)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Messages) != 3 || preview.Messages[0].Role != "user" || preview.Messages[0].Body != "hello" || preview.Messages[1].Body != "answer" || preview.Messages[2].Role != "tool" || preview.Messages[2].Body != "done" {
		t.Fatalf("unexpected parsed messages: %#v", preview.Messages)
	}
	if !preview.Partial || len(preview.Skipped) != 4 {
		t.Fatalf("expected partial report with five skips, got %#v", preview)
	}
}

func TestMakePreviewRejectsUnsupportedFormatAndLimits(t *testing.T) {
	thread := fixtureThread(`C:\work`)
	thread.HistoryMode = "paginated"
	if _, err := makePreview(thread); d.Code(err) != "UNSUPPORTED_HISTORY_FORMAT" {
		t.Fatalf("history mode error = %v", err)
	}
	thread = fixtureThread(`C:\work`)
	thread.Turns[0].ItemsView = "summary"
	if _, err := makePreview(thread); d.Code(err) != "UNSUPPORTED_HISTORY_FORMAT" {
		t.Fatalf("items view error = %v", err)
	}
	thread = fixtureThread(`C:\work`)
	thread.Turns[0].Items = []json.RawMessage{textItem(t, "large", "agentMessage", strings.Repeat("x", maxBody+1))}
	if _, err := makePreview(thread); d.Code(err) != "HISTORY_LIMIT" {
		t.Fatalf("body limit error = %v", err)
	}
	thread = fixtureThread(`C:\work`)
	thread.Turns[0].Items = make([]json.RawMessage, 9)
	for i := range thread.Turns[0].Items {
		thread.Turns[0].Items[i] = textItem(t, string(rune('a'+i)), "agentMessage", strings.Repeat("x", 1<<20))
	}
	if _, err := makePreview(thread); d.Code(err) != "HISTORY_LIMIT" {
		t.Fatalf("total limit error = %v", err)
	}
}

type fakeHistoryClient struct {
	mu      sync.Mutex
	thread  runtimeadapter.HistoryThread
	started chan struct{}
	block   bool
	reads   int
}

func (f *fakeHistoryClient) ListHistory(ctx context.Context, cwd, cursor string, archived bool) (runtimeadapter.HistoryPage, error) {
	f.mu.Lock()
	started, block, thread := f.started, f.block, f.thread
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		return runtimeadapter.HistoryPage{}, ctx.Err()
	}
	return runtimeadapter.HistoryPage{Data: []runtimeadapter.HistoryThread{thread}, NextCursor: "next"}, nil
}
func (f *fakeHistoryClient) ReadHistory(ctx context.Context, id string) (runtimeadapter.HistoryThread, error) {
	f.mu.Lock()
	f.reads++
	started, block, thread := f.started, f.block, f.thread
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		return runtimeadapter.HistoryThread{}, ctx.Err()
	}
	return thread, nil
}
func (f *fakeHistoryClient) Close(context.Context) error { return nil }

type historyFixture struct {
	store   *sqlstore.Store
	manager *Manager
	project *d.Project
	taskID  string
	fake    *fakeHistoryClient
}

func newHistoryFixture(t *testing.T) historyFixture {
	t.Helper()
	root := t.TempDir()
	db, err := sqlstore.Open(filepath.Join(root, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspacePath, home := filepath.Join(root, "workspace"), filepath.Join(root, "codex-home")
	if err := os.MkdirAll(workspacePath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	project, err := processhost.Workspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	project.ID, project.Name = "project-history", "History fixture"
	var taskID string
	_, err = db.Update(context.Background(), "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects[project.ID] = project
		task, err := state.Create(d.Spec{ProjectID: project.ID, Objective: "Import selected history", Criteria: []d.Criterion{{ID: "check", Procedure: "inspect", Expected: "text imported", Required: true}}, AllowedScope: "synthetic history", OutOfScope: "runtime execution", ApprovalProfile: "read-only"})
		if err != nil {
			return nil, nil, err
		}
		taskID = task.ID
		return task.ID, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	manager := New(db, Config{Executable: exe, Home: home})
	fake := &fakeHistoryClient{thread: fixtureThread(project.ResolvedPath)}
	manager.start = func(context.Context, string) (client, error) { return fake, nil }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	return historyFixture{db, manager, project, taskID, fake}
}
func currentTask(t *testing.T, f historyFixture) *d.Task {
	t.Helper()
	state, err := f.store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state.Tasks[f.taskID]
}
func importRequest(task *d.Task, preview Preview, requestID string) ImportRequest {
	return ImportRequest{RequestID: requestID, TaskID: task.ID, ExpectedVersion: task.Version, ThreadID: preview.Thread.ID, PreviewSHA256: preview.SHA256}
}

func TestImportAppendChangedSourceStaleVersionAndReceiptRetry(t *testing.T) {
	f := newHistoryFixture(t)
	f.fake.thread.Turns[0].Items = []json.RawMessage{userTextItem(t, "m1", "first")}
	preview, err := f.manager.Preview(context.Background(), f.project, f.fake.thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale := importRequest(currentTask(t, f), preview, "stale-request")
	stale.ExpectedVersion++
	if _, err = f.manager.Import(context.Background(), f.project, stale); d.Code(err) != "STALE_VERSION" {
		t.Fatalf("stale import error = %v", err)
	}
	request := importRequest(currentTask(t, f), preview, "import-request-1")
	report, err := f.manager.Import(context.Background(), f.project, request)
	if err != nil || report.Imported != 1 || report.Duplicates != 0 {
		t.Fatalf("first import: %#v %v", report, err)
	}
	f.fake.thread.Turns[0].Items = append(f.fake.thread.Turns[0].Items, textItem(t, "m2", "agentMessage", "second"))
	appended, err := f.manager.Preview(context.Background(), f.project, f.fake.thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err = f.manager.Import(context.Background(), f.project, importRequest(currentTask(t, f), appended, "import-request-2"))
	if err != nil || report.Imported != 1 || report.Duplicates != 1 {
		t.Fatalf("append import: %#v %v", report, err)
	}
	f.fake.mu.Lock()
	before := f.fake.reads
	f.fake.thread.Turns[0].Items[0] = userTextItem(t, "m1", "changed")
	f.fake.mu.Unlock()
	replayed, err := f.manager.Import(context.Background(), f.project, request)
	if err != nil || replayed.Imported != 1 {
		t.Fatalf("receipt retry: %#v %v", replayed, err)
	}
	f.fake.mu.Lock()
	after := f.fake.reads
	f.fake.mu.Unlock()
	if after != before {
		t.Fatalf("receipt retry reread source: before=%d after=%d", before, after)
	}
	changed, err := f.manager.Preview(context.Background(), f.project, f.fake.thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.Import(context.Background(), f.project, importRequest(currentTask(t, f), changed, "changed-source")); d.Code(err) != "HISTORY_SOURCE_CHANGED" {
		t.Fatalf("changed source error = %v", err)
	}
}

func TestImportTransactionRollbackAndRetry(t *testing.T) {
	f := newHistoryFixture(t)
	f.fake.thread.Turns[0].Items = []json.RawMessage{userTextItem(t, "m1", "atomic")}
	preview, err := f.manager.Preview(context.Background(), f.project, f.fake.thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := importRequest(currentTask(t, f), preview, "atomic-request")
	if _, err = f.store.DB().Exec(`CREATE TRIGGER fail_import_source BEFORE INSERT ON import_sources BEGIN SELECT RAISE(ABORT,'forced rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.manager.Import(context.Background(), f.project, request); err == nil {
		t.Fatal("expected trigger abort")
	}
	state, err := f.store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks[f.taskID].Version != request.ExpectedVersion {
		t.Fatal("task version escaped rollback")
	}
	for table, want := range map[string]int{"messages": 0, "import_sources": 0, "import_checkpoints": 0, "request_dedup": 0, "audit_events": 0} {
		var count int
		if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s rows after rollback = %d, want %d", table, count, want)
		}
	}
	if _, err = f.store.DB().Exec(`DROP TRIGGER fail_import_source`); err != nil {
		t.Fatal(err)
	}
	report, err := f.manager.Import(context.Background(), f.project, request)
	if err != nil || report.Imported != 1 {
		t.Fatalf("retry after rollback: %#v %v", report, err)
	}
}

func TestCloseCancelsHistoryReadAndWaitsForWorker(t *testing.T) {
	f := newHistoryFixture(t)
	f.fake.block = true
	f.fake.started = make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() { _, err := f.manager.List(context.Background(), f.project, "", false); result <- err }()
	select {
	case <-f.fake.started:
	case <-time.After(2 * time.Second):
		t.Fatal("list did not reach fake client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := f.manager.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("list error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("list worker remained blocked after Close")
	}
	if _, err := f.manager.List(context.Background(), f.project, "", false); d.Code(err) != "APP_CLOSING" {
		t.Fatalf("list after close error = %v", err)
	}
}
