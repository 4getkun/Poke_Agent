package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	d "pokegents/dashboard/server/taskdomain"
)

func openFixtureStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createFixtureTask(t *testing.T, store *Store) *d.Task {
	t.Helper()
	ctx := context.Background()
	var taskID string
	_, err := store.Update(ctx, "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects["project-1"] = &d.Project{
			ID:       "project-1",
			Name:     "Fixture project",
			Identity: "workspace-fixture",
		}
		task, err := state.Create(d.Spec{
			ProjectID: "project-1",
			Objective: "Verify SQLite persistence",
			Criteria: []d.Criterion{{
				ID:        "store-check",
				Procedure: "run the test",
				Expected:  "passes",
				Required:  true,
			}},
			AllowedScope:    "test fixture",
			OutOfScope:      "external systems",
			ApprovalProfile: "workspace-write",
		})
		if err != nil {
			return nil, nil, err
		}
		taskID = task.ID
		return task.ID, nil, nil
	})
	if err != nil {
		t.Fatalf("create task transaction: %v", err)
	}
	state, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot after task creation: %v", err)
	}
	if state.Tasks[taskID] == nil {
		t.Fatalf("task %q was not reloaded from SQLite", taskID)
	}
	return state.Tasks[taskID]
}

func addFixtureMessage(t *testing.T, store *Store, taskID, body string) string {
	t.Helper()
	ctx := context.Background()
	message := d.Message{ID: d.ID(), TaskID: taskID, Role: "user", Body: body, At: d.Now()}
	_, err := store.Update(ctx, "", nil, func(_ *d.State, tx *sql.Tx) (any, []d.Event, error) {
		return nil, nil, AddMessage(ctx, tx, message)
	})
	if err != nil {
		t.Fatalf("insert fixture message: %v", err)
	}
	return message.ID
}

func requireStoreCode(t *testing.T, err error, want string) {
	t.Helper()
	if got := d.Code(err); got != want {
		t.Fatalf("got error %v (code %s), want %s", err, got, want)
	}
}

func TestOpenRequiresAbsolutePathAndCreatesTemporaryDatabase(t *testing.T) {
	if _, err := Open(filepath.Join("relative", "store.sqlite")); err == nil {
		t.Fatal("Open accepted a relative database path")
	}

	path := filepath.Join(t.TempDir(), "nested", "store.sqlite")
	store := openFixtureStore(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created at absolute path: %v", err)
	}
	var version int
	if err := store.DB().QueryRow(`SELECT version FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 1 {
		t.Fatalf("migration version=%d, want 1", version)
	}
}

func TestViewBoardReturnsLatestBoundedUnicodePreviewAndWatermark(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "board-preview.db"))
	task := createFixtureTask(t, store)
	addFixtureMessage(t, store, task.ID, "older")
	latest := strings.Repeat("界", 281) + "tail"
	addFixtureMessage(t, store, task.ID, latest)

	state, seq, settings, previews, err := store.ViewBoard(context.Background())
	if err != nil {
		t.Fatalf("ViewBoard: %v", err)
	}
	preview, ok := previews[task.ID]
	if !ok || preview.Role != "user" || preview.CreatedAt == "" {
		t.Fatalf("missing latest preview metadata: %+v", preview)
	}
	if got := utf8.RuneCountInString(preview.Body); got != 280 || preview.Body != strings.Repeat("界", 280) {
		t.Fatalf("preview body rune count=%d, want 280; body=%q", got, preview.Body)
	}
	if state.Tasks[task.ID] == nil || len(settings) == 0 {
		t.Fatal("ViewBoard omitted state or settings")
	}
	var watermark int64
	if err := store.DB().QueryRow(`SELECT coalesce(max(seq),0) FROM audit_events`).Scan(&watermark); err != nil {
		t.Fatalf("read audit watermark: %v", err)
	}
	if seq != watermark {
		t.Fatalf("watermark=%d, db=%d", seq, watermark)
	}
}

func TestOpenReopensAndRejectsTamperedChecksumOrFutureVersion(t *testing.T) {
	t.Run("reopen and checksum tamper", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.sqlite")
		store := openFixtureStore(t, path)
		if err := store.Close(); err != nil {
			t.Fatalf("close before reopen: %v", err)
		}
		reopened := openFixtureStore(t, path)
		if _, err := reopened.DB().Exec(`UPDATE schema_migrations SET checksum='tampered'`); err != nil {
			t.Fatalf("tamper checksum: %v", err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("close tampered database: %v", err)
		}
		if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_SCHEMA") {
			t.Fatalf("Open after checksum tamper: got %v, want UNSUPPORTED_SCHEMA", err)
		}
	})

	t.Run("future version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.sqlite")
		store := openFixtureStore(t, path)
		if _, err := store.DB().Exec(`UPDATE schema_migrations SET version=2`); err != nil {
			t.Fatalf("set future migration version: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close future-version database: %v", err)
		}
		if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "UNSUPPORTED_SCHEMA") {
			t.Fatalf("Open future schema version: got %v, want UNSUPPORTED_SCHEMA", err)
		}
	})
}

func TestUpdatePersistsProjectsAndTaskForSnapshotReload(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "store.sqlite"))
	task := createFixtureTask(t, store)
	state, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if state.Projects["project-1"] == nil {
		t.Fatal("project was not reloaded from SQLite")
	}
	loaded := state.Tasks[task.ID]
	if loaded == nil || loaded.Spec.ProjectID != "project-1" || loaded.State != "BACKLOG" {
		t.Fatalf("reloaded task mismatch: %#v", loaded)
	}
	if state.SpecHistory[task.ID+":1"] == nil {
		t.Fatal("task spec history was not reloaded")
	}
}

func TestUpdateFailureRollsBackStateEventsMessagesAndDedup(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "store.sqlite"))
	task := createFixtureTask(t, store)
	ctx := context.Background()
	beforeSeq := store.LastSeq(ctx)
	rollbackErr := errors.New("intentional transaction failure")

	_, err := store.Update(ctx, "rollback-request", []byte(`{"fail":true}`), func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		state.Tasks[task.ID].Spec.Objective = "This mutation must roll back"
		message := d.Message{ID: "rollback-message", TaskID: task.ID, Role: "user", Body: "rolled back message", At: d.Now()}
		if err := AddMessage(ctx, tx, message); err != nil {
			return nil, nil, err
		}
		return map[string]string{"result": "must roll back"}, []d.Event{{Type: "must.rollback", TaskID: task.ID}}, rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("Update error=%v, want callback error", err)
	}

	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot after rollback: %v", err)
	}
	if got := after.Tasks[task.ID].Spec.Objective; got != task.Spec.Objective {
		t.Fatalf("task mutation survived rollback: %q", got)
	}
	if got := store.LastSeq(ctx); got != beforeSeq {
		t.Fatalf("event sequence changed after rollback: got %d, want %d", got, beforeSeq)
	}
	var dedupCount int
	if err := store.DB().QueryRow(`SELECT count(*) FROM request_dedup WHERE id='rollback-request'`).Scan(&dedupCount); err != nil {
		t.Fatalf("query dedup after rollback: %v", err)
	}
	if dedupCount != 0 {
		t.Fatalf("dedup row survived rollback: count=%d", dedupCount)
	}
	var messageCount int
	if err := store.DB().QueryRow(`SELECT count(*) FROM messages WHERE id='rollback-message'`).Scan(&messageCount); err != nil {
		t.Fatalf("query message after rollback: %v", err)
	}
	if messageCount != 0 {
		t.Fatalf("message survived rollback: count=%d", messageCount)
	}
}

func TestUpdateDeduplicatesRequestAndRejectsPayloadReuse(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "store.sqlite"))
	var calls atomic.Int32
	callback := func(_ *d.State, _ *sql.Tx) (any, []d.Event, error) {
		calls.Add(1)
		return map[string]string{"value": "original"}, []d.Event{{Type: "dedup.test"}}, nil
	}

	first, err := store.Update(context.Background(), "request-1", []byte(`{"value":1}`), callback)
	if err != nil {
		t.Fatalf("first Update: %v", err)
	}
	second, err := store.Update(context.Background(), "request-1", []byte(`{"value":1}`), callback)
	if err != nil {
		t.Fatalf("same-payload Update: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("dedup result=%s, want original %s", second, first)
	}
	requireStoreCode(t, func() error {
		_, err := store.Update(context.Background(), "request-1", []byte(`{"value":2}`), callback)
		return err
	}(), "REQUEST_ID_REUSED")
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls=%d, want 1", got)
	}
	events, err := store.Events(context.Background(), 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Type != "dedup.test" {
		t.Fatalf("dedup events=%#v, want one original event", events)
	}
}

func TestFiftyConcurrentSameRequestRunsCallbackOnce(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "store.sqlite"))
	const workers = 50
	var calls atomic.Int32
	callback := func(_ *d.State, _ *sql.Tx) (any, []d.Event, error) {
		calls.Add(1)
		return map[string]bool{"ok": true}, []d.Event{{Type: "concurrent.dedup"}}, nil
	}

	var wg sync.WaitGroup
	results := make(chan string, workers)
	errorsCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.Update(context.Background(), "concurrent-request", []byte(`{"same":true}`), callback)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- string(result)
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent Update: %v", err)
	}
	var first string
	count := 0
	for result := range results {
		if count == 0 {
			first = result
		} else if result != first {
			t.Errorf("concurrent result=%s, want %s", result, first)
		}
		count++
	}
	if count != workers {
		t.Fatalf("successful requests=%d, want %d", count, workers)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls=%d, want exactly 1", got)
	}
	events, err := store.Events(context.Background(), 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("committed events=%d, want 1", len(events))
	}
}

func TestMessagesSearchesLiteralUnicodeAndSpecialText(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "store.sqlite"))
	task := createFixtureTask(t, store)
	bodies := []string{
		"日本語の会話",
		"猫が窓辺にいる",
		"Windows Terminal session",
		"カフェ cafe\u0301",
		`記号 100%_literal 引用符: "quoted phrase" and 'single'`,
		"emoji 😀 line one\nline two",
		`記号混在: "% (C:\temp)_" and 「進化」(完了)`,
		"NUL before\x00after",
	}
	for _, body := range bodies {
		addFixtureMessage(t, store, task.ID, body)
	}

	cases := []struct {
		query string
		want  string
	}{
		{query: "日本語", want: bodies[0]},
		{query: "猫", want: bodies[1]},
		{query: "windows", want: bodies[2]},
		{query: "café", want: bodies[3]},
		{query: "%_", want: bodies[4]},
		{query: "100%_literal", want: bodies[4]},
		{query: `"quoted phrase"`, want: bodies[4]},
		{query: `"% (C:\temp)_"`, want: bodies[6]},
		{query: `「進化」(完了)`, want: bodies[6]},
		{query: "before\x00after", want: bodies[7]},
		{query: "😀", want: bodies[5]},
		{query: "line one\nline two", want: bodies[5]},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			page, err := store.Messages(context.Background(), task.ID, tc.query, "", 0, 100)
			if err != nil {
				t.Fatalf("Messages(%q): %v", tc.query, err)
			}
			if len(page.Items) != 1 || page.Items[0].Body != tc.want {
				t.Fatalf("Messages(%q)=%#v, want one match %q", tc.query, page.Items, tc.want)
			}
		})
	}
}

func TestMessageUpdateAndDeleteKeepFTSInSync(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "store.sqlite"))
	task := createFixtureTask(t, store)
	messageID := addFixtureMessage(t, store, task.ID, "needleold appears here")
	ctx := context.Background()

	page, err := store.Messages(ctx, task.ID, "needleold", "", 0, 100)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("initial FTS search: count=%d error=%v", len(page.Items), err)
	}
	_, err = store.Update(ctx, "", nil, func(_ *d.State, tx *sql.Tx) (any, []d.Event, error) {
		_, err := tx.ExecContext(ctx, `UPDATE messages SET body=?,normalized=? WHERE id=?`, "needlenew replaces old text", Normalize("needlenew replaces old text"), messageID)
		return nil, nil, err
	})
	if err != nil {
		t.Fatalf("update message: %v", err)
	}
	oldPage, err := store.Messages(ctx, task.ID, "needleold", "", 0, 100)
	if err != nil {
		t.Fatalf("search old text after update: %v", err)
	}
	newPage, err := store.Messages(ctx, task.ID, "needlenew", "", 0, 100)
	if err != nil || len(oldPage.Items) != 0 || len(newPage.Items) != 1 {
		t.Fatalf("FTS after update: old=%d new=%d error=%v", len(oldPage.Items), len(newPage.Items), err)
	}

	_, err = store.Update(ctx, "", nil, func(_ *d.State, tx *sql.Tx) (any, []d.Event, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, messageID)
		return nil, nil, err
	})
	if err != nil {
		t.Fatalf("delete message: %v", err)
	}
	deletedPage, err := store.Messages(ctx, task.ID, "needlenew", "", 0, 100)
	if err != nil || len(deletedPage.Items) != 0 {
		t.Fatalf("FTS after delete: count=%d error=%v", len(deletedPage.Items), err)
	}
}

func TestBackupOpensAtDifferentPathWithStateAndMessageSearchable(t *testing.T) {
	store := openFixtureStore(t, filepath.Join(t.TempDir(), "source.sqlite"))
	task := createFixtureTask(t, store)
	addFixtureMessage(t, store, task.ID, "backup restore phrase")
	backupPath := filepath.Join(t.TempDir(), "restored.sqlite")
	if err := store.Backup(context.Background(), backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	backup := openFixtureStore(t, backupPath)
	state, err := backup.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot from backup: %v", err)
	}
	if state.Tasks[task.ID] == nil || state.Projects["project-1"] == nil {
		t.Fatal("backup did not restore project and task state")
	}
	page, err := backup.Messages(context.Background(), task.ID, "restore phrase", "", 0, 100)
	if err != nil || len(page.Items) != 1 || page.Items[0].Body != "backup restore phrase" {
		t.Fatalf("search from backup: page=%#v error=%v", page, err)
	}
}
