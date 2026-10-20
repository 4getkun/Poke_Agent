//go:build windows

package maintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

func seedProfile(t *testing.T, profile string, additional bool) {
	t.Helper()
	if err := desktop.PrepareProfile(profile); err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.Open(filepath.Join(profile, "db", "pokegents.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.Update(context.Background(), "", nil, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		if !additional {
			state.Projects["project"] = &d.Project{ID: "project", Identity: "synthetic-workspace", Name: "合成試験"}
		}
		task, err := state.Create(d.Spec{ProjectID: "project", Objective: "日本語の保存試験", AllowedScope: "合成データのみ", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "restore", Procedure: "backup/restore", Expected: "データを保持", Required: true}}})
		if err != nil {
			return nil, nil, err
		}
		err = sqlstore.AddMessage(context.Background(), tx, d.Message{ID: d.ID(), TaskID: task.ID, Role: "assistant", Body: "検索できる日本語メッセージ", At: d.Now()})
		return true, nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertTasks(t *testing.T, path string, count int) {
	t.Helper()
	store, err := sqlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Snapshot(context.Background())
	if err != nil || len(state.Tasks) != count {
		t.Fatalf("restored tasks=%d expected=%d error=%v", len(state.Tasks), count, err)
	}
	page, err := store.Messages(context.Background(), "", "日本語メッセージ", "", 0, 100)
	if err != nil || len(page.Items) != count {
		t.Fatalf("restored FTS messages=%d expected=%d error=%v", len(page.Items), count, err)
	}
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestBackupRestoreUnicodePathsAndPreRestoreRetention(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	profile := filepath.Join(root, "日本語 profile &()[]")
	backup := filepath.Join(root, "バックアップ &()[] .db")
	database := filepath.Join(profile, "db", "pokegents.db")
	seedProfile(t, profile, false)
	if err := Backup(ctx, profile, backup); err != nil {
		t.Fatal(err)
	}
	before := fileHash(t, backup)
	if err := sqlstore.ValidateSnapshot(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if fileHash(t, backup) != before {
		t.Fatal("read-only validation changed the source backup")
	}
	if err := standalone(backup); err != nil {
		t.Fatalf("read-only validation created a journal: %v", err)
	}
	sd, err := windows.GetNamedSecurityInfo(backup, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || !strings.HasPrefix(sd.String(), "D:P(") {
		t.Fatal("backup has no protected private ACL")
	}
	user, _ := windows.GetCurrentProcessToken().GetTokenUser()
	expected, _ := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if sd.String() != expected.String() {
		t.Fatalf("backup ACL contains unexpected permissions: %s", sd.String())
	}
	if err := Backup(ctx, profile, backup); err == nil || fileHash(t, backup) != before {
		t.Fatal("existing backup was overwritten")
	}
	seedProfile(t, profile, true)
	// Simulate an independent reader that disallows deletion/replacement.
	ptr, _ := windows.UTF16PtrFromString(database)
	reader, err := windows.CreateFile(ptr, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	previous, restoreErr := Restore(ctx, profile, backup)
	windows.CloseHandle(reader)
	if restoreErr == nil || previous == "" {
		t.Fatal("locked database replacement was accepted or previous backup was lost")
	}
	if CheckPending(profile) == nil {
		t.Fatal("failed replacement did not block normal startup")
	}
	if err := Backup(ctx, profile, filepath.Join(root, "interrupted-backup.db")); err == nil {
		t.Fatal("interrupted restore was treated as a normal profile")
	}
	assertTasks(t, database, 2)
	assertTasks(t, previous, 2)
	previous, err = Restore(ctx, profile, backup)
	if err != nil || previous == "" {
		t.Fatalf("restore failed, previous=%s error=%v", previous, err)
	}
	if err := CheckPending(profile); err != nil {
		t.Fatalf("successful explicit recovery did not clear the marker: %v", err)
	}
	assertTasks(t, database, 1)
	assertTasks(t, previous, 2)
	newProfile := filepath.Join(root, "復元用の新規profile")
	if previous, err := Restore(ctx, newProfile, backup); err != nil || previous != "" {
		t.Fatalf("new profile restore: previous=%s error=%v", previous, err)
	}
	assertTasks(t, filepath.Join(newProfile, "db", "pokegents.db"), 1)
	if fileHash(t, backup) != before {
		t.Fatal("restore modified the source backup")
	}
}

func TestInvalidOrBusyRestoreDoesNotReplaceCurrentData(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	profile := filepath.Join(root, "profile")
	seedProfile(t, profile, false)
	database := filepath.Join(profile, "db", "pokegents.db")
	before := fileHash(t, database)
	invalid := filepath.Join(root, "invalid.db")
	if err := os.WriteFile(invalid, []byte("not a database"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, profile, invalid); err == nil || fileHash(t, database) != before {
		t.Fatal("invalid backup changed current data")
	}
	valid := filepath.Join(root, "valid.db")
	if err := Backup(ctx, profile, valid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valid+"-wal", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(ctx, profile, valid); err == nil {
		t.Fatal("snapshot with WAL accepted")
	}
	if err := os.Remove(valid + "-wal"); err != nil {
		t.Fatal(err)
	}
	instance, primary, err := desktop.TryLockProfile(profile)
	if err != nil || !primary {
		t.Fatal("profile lock unavailable")
	}
	defer instance.Close()
	if _, err := Restore(ctx, strings.ToUpper(profile), valid); err == nil || !strings.Contains(err.Error(), "PROFILE_IN_USE") {
		t.Fatal("busy profile accepted for restore")
	}
	if err := Backup(ctx, profile, filepath.Join(root, "busy-backup.db")); err == nil || !strings.Contains(err.Error(), "PROFILE_IN_USE") {
		t.Fatal("busy profile accepted for backup")
	}
	if _, err := os.Stat(filepath.Join(root, "busy-backup.db")); !os.IsNotExist(err) {
		t.Fatal("busy backup created output")
	}
	assertTasks(t, database, 1)
}
