//go:build windows && integration

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"pokegents/dashboard/server/desktop"
)

func TestBuiltExecutableOfflineMaintenance(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_MAINTENANCE_EXE")
	if exe == "" {
		t.Fatal("explicit built Pokegents executable required")
	}
	root := t.TempDir()
	profile := filepath.Join(root, "日本語 profile &()[]")
	backup := filepath.Join(root, "日本語 backup &()[] .db")
	seedProfile(t, profile, false)
	run := func(wantSuccess bool, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatal("headless maintenance timed out (possible modal error dialog)")
		}
		if (err == nil) != wantSuccess {
			t.Fatalf("maintenance success=%v error=%v output=%s", wantSuccess, err, out)
		}
		return string(out)
	}
	var result map[string]string
	if out := run(true, "--data-dir", profile, "--backup", backup); json.Unmarshal([]byte(out), &result) != nil || result["backup"] != "ok" {
		t.Fatalf("backup output: %s", out)
	}
	seedProfile(t, profile, true)
	if out := run(true, "--data-dir", profile, "--restore", backup); json.Unmarshal([]byte(out), &result) != nil || result["restore"] != "ok" || result["pre_restore_backup"] == "" {
		t.Fatalf("restore output: %s", out)
	}
	assertTasks(t, filepath.Join(profile, "db", "pokegents.db"), 1)
	assertTasks(t, result["pre_restore_backup"], 2)
	invalid := filepath.Join(root, "invalid.db")
	if err := os.WriteFile(invalid, []byte("invalid fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	run(false, "-data-dir", profile, "-restore", invalid)
	assertTasks(t, filepath.Join(profile, "db", "pokegents.db"), 1)
	interrupted := filepath.Join(root, "中断した復元 profile")
	if err := desktop.PrepareProfile(interrupted); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(interrupted, pendingName), []byte("synthetic pending marker"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := run(false, "--data-dir", interrupted, "--verify"); !strings.Contains(out, "RESTORE_INTERRUPTED") {
		t.Fatalf("pending restore ignored: %s", out)
	}
	if _, err := os.Stat(filepath.Join(interrupted, "db", "pokegents.db")); !os.IsNotExist(err) {
		t.Fatal("interrupted restore silently created an empty database")
	}
	t.Log("built GUI executable: headless backup/restore, previous-data retention, Unicode paths, non-modal failure and interrupted-restore startup guard passed")
}
