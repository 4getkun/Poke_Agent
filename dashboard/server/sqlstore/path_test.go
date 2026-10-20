package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	d "pokegents/dashboard/server/taskdomain"
)

func TestOpenUpdateSearchBackupReopenUnicodeAndSpecialPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "日本語 path &()[]")
	sourcePath := filepath.Join(root, "元 database 日本語 &()[] .sqlite")
	backupPath := filepath.Join(root, "復元 copy 日本語 &()[] .sqlite")
	store, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("Open source database at %q: %v", sourcePath, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task := createFixtureTask(t, store)
	messageID := addFixtureMessage(t, store, task.ID, "更新前の日本語検索語")

	ctx := context.Background()
	updatedBody := "更新後の日本語検索語 &()[]"
	_, err = store.Update(ctx, "", nil, func(_ *d.State, tx *sql.Tx) (any, []d.Event, error) {
		_, updateErr := tx.ExecContext(ctx,
			`UPDATE messages SET body=?,normalized=? WHERE id=?`,
			updatedBody, Normalize(updatedBody), messageID,
		)
		return nil, nil, updateErr
	})
	if err != nil {
		t.Fatalf("update message in %q: %v", sourcePath, err)
	}
	assertPathFixtureSearch(t, store, task.ID, "更新後の日本語検索語", updatedBody)
	if page, err := store.Messages(ctx, task.ID, "更新前の日本語検索語", "", 0, 100); err != nil || len(page.Items) != 0 {
		t.Fatalf("old search after update: count=%d error=%v", len(page.Items), err)
	}

	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup to %q: %v", backupPath, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close source database: %v", err)
	}

	reopened, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("reopen source database at %q: %v", sourcePath, err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertPathFixtureSearch(t, reopened, task.ID, "更新後の日本語検索語", updatedBody)

	backup, err := Open(backupPath)
	if err != nil {
		t.Fatalf("reopen backup database at %q: %v", backupPath, err)
	}
	t.Cleanup(func() { _ = backup.Close() })
	state, err := backup.Snapshot(ctx)
	if err != nil {
		t.Fatalf("snapshot backup %q: %v", backupPath, err)
	}
	if state.Tasks[task.ID] == nil {
		t.Fatalf("backup %q omitted task %q", backupPath, task.ID)
	}
	assertPathFixtureSearch(t, backup, task.ID, "更新後の日本語検索語", updatedBody)
}

func assertPathFixtureSearch(t *testing.T, store *Store, taskID, query, wantBody string) {
	t.Helper()
	page, err := store.Messages(context.Background(), taskID, query, "", 0, 100)
	if err != nil {
		t.Fatalf("Messages(%q): %v", query, err)
	}
	if len(page.Items) != 1 || page.Items[0].Body != wantBody {
		t.Fatalf("Messages(%q)=%#v, want one match %q", query, page.Items, wantBody)
	}
}
