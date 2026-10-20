//go:build windows

// Package maintenance performs explicit offline profile backup and restore.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

func lock(profile string) (*desktop.Instance, error) {
	instance, primary, err := desktop.TryLockProfile(profile)
	if err != nil {
		return nil, err
	}
	if !primary {
		return nil, errors.New("PROFILE_IN_USE: close the application before maintenance")
	}
	if err := desktop.PrepareProfile(profile); err != nil {
		instance.Close()
		return nil, err
	}
	return instance, nil
}

func Backup(ctx context.Context, profile, destination string) error {
	instance, err := lock(profile)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := CheckPending(profile); err != nil {
		return err
	}
	return backupLocked(ctx, profile, destination)
}

const pendingName = "restore-pending.json"

// WithOfflineBackup keeps the canonical profile lock for the complete installer
// transaction. The callback must not open this profile in another process.
func WithOfflineBackup(ctx context.Context, profile, destination string, apply func() error) error {
	instance, err := lock(profile)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := CheckPending(profile); err != nil {
		return err
	}
	if err := backupLocked(ctx, profile, destination); err != nil {
		return err
	}
	return apply()
}

type restoreJournal struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	Profile        string `json:"profile"`
	Stage          string `json:"stage"`
	PreviousBackup string `json:"previous_backup"`
	RawRollback    string `json:"raw_rollback"`
}

// CheckPending must run before normal startup opens or creates a database.
// Windows replacement has documented partial-failure states; retaining this
// marker prevents a missing DB from silently becoming an empty new profile.
func CheckPending(profile string) error {
	if _, err := os.Lstat(filepath.Join(profile, pendingName)); !os.IsNotExist(err) {
		return errors.New("RESTORE_INTERRUPTED: restore a validated backup explicitly before opening this profile")
	}
	return nil
}

func backupLocked(ctx context.Context, profile, destination string) error {
	if err := desktop.CheckDirectLocalPath(destination); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("BACKUP_DESTINATION_EXISTS_OR_UNAVAILABLE")
	}
	database := filepath.Join(profile, "db", "pokegents.db")
	if err := desktop.CheckDirectLocalPath(database); err != nil {
		return err
	}
	if info, err := os.Lstat(database); err != nil || !info.Mode().IsRegular() {
		return errors.New("DATABASE_NOT_FOUND")
	}
	store, err := sqlstore.Open(database)
	if err != nil {
		return err
	}
	defer store.Close()
	stage := filepath.Join(profile, "db", ".backup-"+d.ID()+".db")
	defer os.Remove(stage)
	if err := store.Backup(ctx, stage); err != nil {
		return err
	}
	if err := syncFile(stage); err != nil {
		return err
	}
	if err := sqlstore.ValidateSnapshot(ctx, stage); err != nil {
		return err
	}
	// Export via a protected same-directory temporary file; never overwrite an
	// existing user file, and never expose task data before its private ACL.
	export := filepath.Join(filepath.Dir(destination), ".Pokegents-backup-"+d.ID()+".tmp")
	if err := copyPrivate(stage, export); err != nil {
		return err
	}
	defer os.Remove(export)
	if err := ctx.Err(); err != nil {
		return err
	}
	return move(export, destination, false)
}

// Restore validates a copied snapshot before touching the current database.
// A supported current DB is backed up first. Corrupt/unsupported current stores
// can instead be recovered into a new profile, preserving the original bytes.
func Restore(ctx context.Context, profile, source string) (string, error) {
	instance, err := lock(profile)
	if err != nil {
		return "", err
	}
	defer instance.Close()
	journalPath := filepath.Join(profile, pendingName)
	if err := desktop.CheckDirectLocalPath(journalPath); err != nil {
		return "", err
	}
	if info, err := os.Lstat(journalPath); err == nil && (!info.Mode().IsRegular() || info.Size() > 16384) {
		return "", errors.New("UNRECOGNIZED_RESTORE_MARKER")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	oldJournal, oldErr := os.ReadFile(journalPath)
	if oldErr == nil {
		var old restoreJournal
		if json.Unmarshal(oldJournal, &old) != nil || old.Version != 1 || len(old.ID) != 32 || !strings.EqualFold(filepath.Clean(old.Profile), filepath.Clean(profile)) {
			return "", errors.New("UNRECOGNIZED_RESTORE_MARKER")
		}
	} else if !os.IsNotExist(oldErr) {
		return "", oldErr
	}
	if err := desktop.CheckDirectLocalPath(source); err != nil {
		return "", err
	}
	database := filepath.Join(profile, "db", "pokegents.db")
	if err := desktop.CheckDirectLocalPath(database); err != nil {
		return "", err
	}
	if sourceInfo, err := os.Stat(source); err != nil || !sourceInfo.Mode().IsRegular() {
		return "", errors.New("INVALID_DATABASE_SNAPSHOT")
	} else if targetInfo, err := os.Stat(database); err == nil && os.SameFile(sourceInfo, targetInfo) {
		return "", errors.New("RESTORE_SOURCE_IS_CURRENT_DATABASE")
	}
	if err := standalone(source); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(database), 0700); err != nil {
		return "", err
	}
	stage := filepath.Join(filepath.Dir(database), ".restore-"+d.ID()+".db")
	if err := copyPrivate(source, stage); err != nil {
		return "", err
	}
	pending := false
	defer func() {
		if !pending {
			os.Remove(stage)
		}
	}()
	if err := standalone(source); err != nil {
		return "", err
	}
	if err := sqlstore.ValidateSnapshot(ctx, stage); err != nil {
		return "", err
	}
	backup := ""
	folder := filepath.Join(profile, "backups")
	if err := desktop.CheckDirectLocalPath(folder); err != nil {
		return "", err
	}
	if err := os.MkdirAll(folder, 0700); err != nil {
		return "", err
	}
	if _, err := os.Stat(database); err == nil {
		backup = filepath.Join(folder, "pre-restore-"+d.ID()+".db")
		if err := backupLocked(ctx, profile, backup); err != nil {
			return "", fmt.Errorf("CURRENT_DATABASE_BACKUP_FAILED: restore into a new profile instead: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := standalone(database); err != nil {
		return backup, err
	}
	// Refuse an external writer. FILE_SHARE_DELETE permits our atomic rename,
	// while denying write sharing prevents a new SQLite writer during replace.
	var current windows.Handle
	if _, err := os.Stat(database); err == nil {
		path, _ := windows.UTF16PtrFromString(database)
		current, err = windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return backup, errors.New("CURRENT_DATABASE_BUSY")
		}
		defer windows.CloseHandle(current)
	}
	if err := ctx.Err(); err != nil {
		return backup, err
	}
	if oldErr == nil {
		if err := copyPrivate(journalPath, filepath.Join(folder, "restore-interrupted-"+d.ID()+".json")); err != nil {
			return backup, err
		}
	}
	journal := restoreJournal{Version: 1, ID: d.ID(), Profile: profile, Stage: stage, PreviousBackup: backup, RawRollback: filepath.Join(folder, "pre-restore-raw-"+d.ID()+".db")}
	if err := writeJournal(journalPath, journal); err != nil {
		return backup, err
	}
	pending = true
	if err := replace(stage, database, journal.RawRollback); err != nil {
		return backup, err
	}
	if err := sqlstore.ValidateSnapshot(ctx, database); err != nil {
		return backup, fmt.Errorf("RESTORED_DATABASE_VALIDATION_FAILED: previous backup=%s: %w", backup, err)
	}
	if err := os.Remove(journalPath); err != nil {
		return backup, err
	}
	pending = false
	return backup, nil
}

func writeJournal(path string, journal restoreJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	stage := filepath.Join(filepath.Dir(path), ".restore-journal-"+d.ID()+".tmp")
	file, err := os.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	src, _ := windows.UTF16PtrFromString(stage)
	dst, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func standalone(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return errors.New("DATABASE_NOT_STANDALONE: close all database connections first")
		}
	}
	return nil
}

func copyPrivate(source, target string) (err error) {
	if err := desktop.CheckDirectLocalPath(source); err != nil {
		return err
	}
	if err := desktop.CheckDirectLocalPath(target); err != nil {
		return err
	}
	ptr, _ := windows.UTF16PtrFromString(source)
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return errors.New("SNAPSHOT_SOURCE_BUSY_OR_UNAVAILABLE")
	}
	input := os.NewFile(uintptr(h), source)
	defer input.Close()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	ptr, _ = windows.UTF16PtrFromString(target)
	h, err = windows.CreateFile(ptr, windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	output := os.NewFile(uintptr(h), target)
	defer func() {
		closeErr := output.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(target) // Only the file created with CREATE_NEW above.
		}
	}()
	if _, err = io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func move(source, target string, _ bool) error {
	src, _ := windows.UTF16PtrFromString(source)
	dst, _ := windows.UTF16PtrFromString(target)
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_WRITE_THROUGH)
}

func replace(source, target, rollback string) error {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return move(source, target, false)
	} else if err != nil {
		return err
	}
	src, _ := windows.UTF16PtrFromString(source)
	dst, _ := windows.UTF16PtrFromString(target)
	old, _ := windows.UTF16PtrFromString(rollback)
	result, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW").Call(uintptr(unsafe.Pointer(dst)), uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(old)), 0, 0, 0)
	if result == 0 {
		return callErr
	}
	return nil
}
