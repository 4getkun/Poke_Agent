//go:build windows && integration

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/sqlstore"
)

const (
	installerIntegrationCurrent = "0.1.0"
	installerIntegrationNext    = "0.1.1"
)

// This exercises the real Inno installers and real installed --verify command.
// Only the Authenticode verifier is replaced because signing is tested separately.
func TestInstallerUpdateTransactionWithRealInnoAndHealth(t *testing.T) {
	newInstaller := requiredInstaller(t, "POKEGENTS_TEST_UPDATE_INSTALLER")
	previousInstaller := requiredInstaller(t, "POKEGENTS_TEST_UPDATE_PREVIOUS")
	key, regErr := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\{B2075B47-8E23-4A2A-9D74-4B1DA9BC68C1}_is1`, registry.QUERY_VALUE)
	if regErr == nil {
		key.Close()
		t.Fatal("existing installation: refuse to modify its uninstall entry")
	}
	if regErr != registry.ErrNotExist {
		t.Fatal(regErr)
	}
	programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(programs, "Pokegents.lnk")); !os.IsNotExist(err) {
		t.Fatal("existing shortcut: refuse installer test")
	}

	root, err := os.MkdirTemp("", "pokegents-update-integration-")
	if err != nil {
		t.Fatal(err)
	}
	keepRoot := true
	activeInstallDir := ""
	defer func() {
		if activeInstallDir != "" {
			if err := uninstallOwnedTestInstall(root, activeInstallDir); err != nil {
				t.Errorf("safe cleanup of owned install %s: %v", activeInstallDir, err)
				keepRoot = true
			} else {
				activeInstallDir = ""
			}
		}
		if !keepRoot && !t.Failed() {
			if err := desktop.CheckDirectLocalPath(root); err != nil {
				t.Error(err)
				return
			}
			rel, err := filepath.Rel(os.TempDir(), root)
			if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
				t.Error("refuse cleanup outside temporary test root")
				return
			}
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("remove owned test root: %v", err)
			}
		} else {
			t.Logf("retained installer integration diagnostics: %s", root)
		}
	}()

	newExecutableHash := os.Getenv("POKEGENTS_TEST_UPDATE_EXE")
	if newExecutableHash != "" {
		if !filepath.IsAbs(newExecutableHash) {
			t.Fatalf("POKEGENTS_TEST_UPDATE_EXE must be absolute: %q", newExecutableHash)
		}
		if _, err := os.Stat(newExecutableHash); err != nil {
			t.Fatalf("POKEGENTS_TEST_UPDATE_EXE: %v", err)
		}
		newExecutableHash, err = hashFile(newExecutableHash)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		// Install once in an owned throwaway directory to obtain the exact payload
		// hash carried by the new Inno installer, then uninstall it before testing.
		harvestDir := filepath.Join(root, "harvest-install")
		activeInstallDir = harvestDir
		installTestPackage(t, newInstaller, harvestDir)
		newExecutableHash, err = hashFile(filepath.Join(harvestDir, "Pokegents.exe"))
		if err != nil {
			t.Fatalf("hash executable installed from new installer: %v", err)
		}
		if err := uninstallOwnedTestInstall(root, harvestDir); err != nil {
			t.Fatalf("uninstall hash-harvest installation: %v", err)
		}
		activeInstallDir = ""
	}
	if !hexHash(newExecutableHash) {
		t.Fatalf("invalid new executable hash %q", newExecutableHash)
	}

	newBytes, err := os.ReadFile(newInstaller)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, err := os.ReadFile(previousInstaller)
	if err != nil {
		t.Fatal(err)
	}
	newInstallerHash := sha256.Sum256(newBytes)
	oldInstallerHash := sha256.Sum256(oldBytes)
	pinBytes := sha256.Sum256([]byte("Pokegents integration fixture signer"))
	pin := hex.EncodeToString(pinBytes[:])
	versionsByHash := map[string]string{
		hex.EncodeToString(newInstallerHash[:]): installerIntegrationNext,
		hex.EncodeToString(oldInstallerHash[:]): installerIntegrationCurrent,
		newExecutableHash:                       installerIntegrationNext,
	}
	verifyFixture := func(_ context.Context, path string) (signature, error) {
		hash, err := hashFile(path)
		if err != nil {
			return signature{}, err
		}
		version, ok := versionsByHash[hash]
		if !ok {
			return signature{}, fmt.Errorf("unexpected fixture signature path/hash: %s %s", path, hash)
		}
		return signature{Pin: pin, Product: "Pokegents", Version: version}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if !t.Run("upgrade", func(t *testing.T) {
		profile := filepath.Join(root, "profile-upgrade")
		installDir := filepath.Join(root, "install-upgrade")
		originalHash, sentinel := seedInstallerProfile(t, profile)
		activeInstallDir = installDir
		installTestPackage(t, previousInstaller, installDir)
		oldExe := filepath.Join(installDir, "Pokegents.exe")
		oldHash, err := hashFile(oldExe)
		if err != nil {
			t.Fatal(err)
		}
		stageDir, stage := prepareInstallerStage(t, profile, newInstaller, previousInstaller, installerIntegrationCurrent, installerIntegrationNext, oldHash, newExecutableHash, pin)
		e := realEngine()
		e.verify = verifyFixture
		if err := e.apply(ctx, profile, stageDir, installDir, installerIntegrationCurrent, pin, false); err != nil {
			t.Fatalf("apply real new installer: %v", err)
		}
		if got, err := hashFile(oldExe); err != nil || got != stage.Manifest.ExecutableSHA256 {
			t.Fatalf("installed new executable hash=%q err=%v", got, err)
		}
		assertInstallerProfile(t, profile, originalHash, sentinel)
		assertInstallerBackup(t, stageDir)
		assertInstallerJournal(t, profile, "completed")
		if err := CheckPending(profile); err != nil {
			t.Fatalf("completed update remains pending: %v", err)
		}
		if err := uninstallOwnedTestInstall(root, installDir); err != nil {
			t.Fatalf("uninstall successful test installation: %v", err)
		}
		activeInstallDir = ""
		if _, err := os.Stat(filepath.Join(installDir, "installer-integration-owned.txt")); err != nil {
			t.Fatalf("unowned install sentinel was removed by uninstall: %v", err)
		}
	}) {
		return
	}

	t.Run("health-failure-rolls-back", func(t *testing.T) {
		profile := filepath.Join(root, "profile-rollback")
		installDir := filepath.Join(root, "install-rollback")
		originalHash, sentinel := seedInstallerProfile(t, profile)
		activeInstallDir = installDir
		installTestPackage(t, previousInstaller, installDir)
		installedExe := filepath.Join(installDir, "Pokegents.exe")
		oldHash, err := hashFile(installedExe)
		if err != nil {
			t.Fatal(err)
		}
		stageDir, _ := prepareInstallerStage(t, profile, newInstaller, previousInstaller, installerIntegrationCurrent, installerIntegrationNext, oldHash, newExecutableHash, pin)
		e := realEngine()
		e.verify = verifyFixture
		baseHealth := e.health
		e.health = func(ctx context.Context, exe, healthProfile string) error {
			// Force the real --verify process to observe the same lock contention
			// a second app instance would cause; the check itself remains real.
			lock, primary, err := desktop.TryLockProfile(healthProfile)
			if err != nil {
				return err
			}
			if !primary {
				return fmt.Errorf("could not acquire forced health lock")
			}
			defer lock.Close()
			return baseHealth(ctx, exe, healthProfile)
		}
		err = e.apply(ctx, profile, stageDir, installDir, installerIntegrationCurrent, pin, false)
		if err == nil || !strings.Contains(err.Error(), "UPDATE_ROLLED_BACK") {
			t.Fatalf("health failure result=%v, want UPDATE_ROLLED_BACK", err)
		}
		if got, err := hashFile(installedExe); err != nil || got != oldHash {
			t.Fatalf("rollback executable hash=%q err=%v, want %s", got, err, oldHash)
		}
		assertInstallerProfile(t, profile, originalHash, sentinel)
		assertInstallerBackup(t, stageDir)
		assertInstallerJournal(t, profile, "rolled_back")
		if err := CheckPending(profile); err != nil {
			t.Fatalf("rolled-back update remains pending: %v", err)
		}
		if err := uninstallOwnedTestInstall(root, installDir); err != nil {
			t.Fatalf("uninstall rolled-back test installation: %v", err)
		}
		activeInstallDir = ""
	})

	keepRoot = false
}

func requiredInstaller(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path == "" {
		t.Skipf("set %s to an absolute installer path to run the Inno integration test", name)
	}
	if !filepath.IsAbs(path) {
		t.Skipf("%s must be absolute: %q", name, path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Skipf("%s installer unavailable: %v", name, err)
	}
	return filepath.Clean(path)
}

func installTestPackage(t *testing.T, installer, installDir string) {
	t.Helper()
	if err := os.MkdirAll(installDir, 0700); err != nil {
		t.Fatalf("create owned install directory: %v", err)
	}
	args := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/NOCLOSEAPPLICATIONS", "/NORESTARTAPPLICATIONS", "/LANG=japanese", "/DIR=" + installDir}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runOwned(ctx, installer, args); err != nil {
		t.Fatalf("run owned Inno installer %s: %v", installer, err)
	}
	marker := filepath.Join(installDir, "installer-integration-owned.txt")
	if err := os.WriteFile(marker, []byte("owned by installer integration test\n"), 0600); err != nil {
		t.Fatalf("write install ownership sentinel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "Pokegents.exe")); err != nil {
		t.Fatalf("installed executable missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installDir, "unins000.exe")); err != nil {
		t.Fatalf("owned uninstaller missing: %v", err)
	}
}

func uninstallOwnedTestInstall(root, installDir string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	installAbs, err := filepath.Abs(installDir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, installAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refuse uninstall outside owned test root: %s", installAbs)
	}
	marker, err := os.ReadFile(filepath.Join(installAbs, "installer-integration-owned.txt"))
	if err != nil {
		return fmt.Errorf("refuse uninstall without ownership sentinel: %w", err)
	}
	if string(marker) != "owned by installer integration test\n" {
		return fmt.Errorf("refuse uninstall with unexpected ownership sentinel")
	}
	uninstaller := filepath.Join(installAbs, "unins000.exe")
	info, err := os.Stat(uninstaller)
	if err != nil {
		return fmt.Errorf("refuse uninstall without owned uninstaller %s: %w", uninstaller, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refuse uninstall with non-regular uninstaller %s", uninstaller)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := runOwned(ctx, uninstaller, []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/LANG=japanese"}); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(installAbs, "Pokegents.exe")); !os.IsNotExist(err) {
		return fmt.Errorf("owned executable remains after uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installAbs, "installer-integration-owned.txt")); err != nil {
		return fmt.Errorf("uninstaller removed ownership sentinel: %w", err)
	}
	return nil
}

func seedInstallerProfile(t *testing.T, profile string) (string, string) {
	t.Helper()
	if err := desktop.PrepareProfile(profile); err != nil {
		t.Fatalf("prepare profile: %v", err)
	}
	database := filepath.Join(profile, "db", "pokegents.db")
	store, err := sqlstore.Open(database)
	if err != nil {
		t.Fatalf("open profile database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close profile database: %v", err)
	}
	sentinel := filepath.Join(profile, "update-integration-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("profile sentinel must survive update\n"), 0600); err != nil {
		t.Fatalf("write profile sentinel: %v", err)
	}
	hash, err := hashFile(database)
	if err != nil {
		t.Fatalf("hash original profile database: %v", err)
	}
	return hash, sentinel
}

func prepareInstallerStage(t *testing.T, profile, newInstaller, oldInstaller, current, next, currentHash, executableHash, pin string) (string, Stage) {
	t.Helper()
	folder := filepath.Join(profile, "updates", "installer-integration")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatalf("create update stage: %v", err)
	}
	for _, file := range []struct{ source, name string }{{newInstaller, "installer.exe"}, {oldInstaller, "rollback.exe"}} {
		if err := copyNew(file.source, filepath.Join(folder, file.name)); err != nil {
			t.Fatalf("copy staged %s: %v", file.name, err)
		}
	}
	newInfo, err := os.Stat(filepath.Join(folder, "installer.exe"))
	if err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(filepath.Join(folder, "rollback.exe"))
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := hashFile(filepath.Join(folder, "installer.exe"))
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := hashFile(filepath.Join(folder, "rollback.exe"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		ExecutableSHA256: executableHash,
		Schema:           1,
		Product:          "Pokegents",
		Target:           "windows/amd64",
		Installer:        Artifact{Version: next, URL: "https://updates.example/installer.exe", SHA256: newHash, Size: newInfo.Size()},
		Rollback:         &Artifact{Version: current, URL: "https://updates.example/rollback.exe", SHA256: oldHash, Size: oldInfo.Size()},
	}
	stage := Stage{Manifest: manifest, Pin: pin, CurrentVersion: current, CurrentHash: currentHash}
	if err := writeNew(filepath.Join(folder, "stage.json"), stage); err != nil {
		t.Fatalf("write stage manifest: %v", err)
	}
	return folder, stage
}

func assertInstallerProfile(t *testing.T, profile, expectedDatabaseHash, sentinel string) {
	t.Helper()
	got, err := hashFile(filepath.Join(profile, "db", "pokegents.db"))
	if err != nil || got != expectedDatabaseHash {
		t.Fatalf("profile database hash=%q err=%v, want original %s", got, err, expectedDatabaseHash)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("profile sentinel missing: %v", err)
	}
}

func assertInstallerBackup(t *testing.T, stageDir string) {
	t.Helper()
	backup := filepath.Join(stageDir, "pre-update.db")
	if err := sqlstore.ValidateSnapshot(context.Background(), backup); err != nil {
		t.Fatalf("validate pre-update database backup: %v", err)
	}
}

func assertInstallerJournal(t *testing.T, profile, want string) {
	t.Helper()
	var got Journal
	contents, err := os.ReadFile(filepath.Join(profile, pendingName))
	if err != nil {
		t.Fatalf("read update journal: %v", err)
	}
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatalf("decode update journal: %v", err)
	}
	if got.State != want {
		t.Fatalf("journal state=%q, want %q", got.State, want)
	}
}
