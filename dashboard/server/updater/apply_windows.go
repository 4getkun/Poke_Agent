//go:build windows

package updater

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/maintenance"
	"pokegents/dashboard/server/processhost"
)

const pendingName = "update-pending.json"

type Journal struct {
	State      string `json:"state"`
	Stage      string `json:"stage"`
	InstallDir string `json:"install_dir"`
	Version    string `json:"version"`
	Previous   string `json:"previous_version"`
	Error      string `json:"error,omitempty"`
}

func CheckPending(profile string) error {
	var j Journal
	err := readJSON(filepath.Join(profile, pendingName), &j)
	if os.IsNotExist(err) {
		return nil
	}
	if err == nil && (j.State == "completed" || j.State == "rolled_back") {
		return nil
	}
	return errors.New("UPDATE_INTERRUPTED: run update --action recover with the saved stage before opening this profile")
}
func journal(profile string, j Journal) error {
	path := filepath.Join(profile, pendingName)
	tmp := path + ".tmp"
	if err := desktop.CheckDirectLocalPath(path); err != nil {
		return err
	}
	if err := desktop.CheckDirectLocalPath(tmp); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	if err := writeNew(tmp, j); err != nil {
		return err
	}
	from, _ := windows.UTF16PtrFromString(tmp)
	to, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func copyNew(from, to string) error {
	if err := desktop.CheckDirectLocalPath(from); err != nil {
		return err
	}
	if err := desktop.CheckDirectLocalPath(to); err != nil {
		return err
	}
	r, err := os.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer w.Close()
	if _, err := io.Copy(w, r); err != nil {
		return err
	}
	return w.Sync()
}
func pinFile(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}
func localStage(profile, folder string) error {
	if !filepath.IsAbs(folder) || !filepath.IsAbs(profile) {
		return errors.New("UPDATE_ABSOLUTE_PATH_REQUIRED")
	}
	if err := desktop.CheckDirectLocalPath(folder); err != nil {
		return err
	}
	rel, err := filepath.Rel(filepath.Join(profile, "updates"), folder)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." || strings.ContainsAny(rel, `\/`) {
		return errors.New("UPDATE_STAGE_OUTSIDE_PROFILE")
	}
	return nil
}
func loadStage(ctx context.Context, profile, folder, current, pin string, verify func(context.Context, string) (signature, error)) (Stage, error) {
	var stage Stage
	if err := localStage(profile, folder); err != nil {
		return stage, err
	}
	if err := readJSON(filepath.Join(folder, "stage.json"), &stage); err != nil {
		return stage, err
	}
	if stage.CurrentVersion != current || stage.Pin != pin || !hexHash(stage.CurrentHash) || stage.Manifest.Rollback == nil || stage.Manifest.Rollback.Version != current || !newer(stage.Manifest.Installer.Version, current) {
		return stage, errors.New("UPDATE_STAGE_INVALID")
	}
	if err := validate(stage.Manifest, current, stage.Manifest.Installer.URL); err != nil {
		return stage, err
	}
	for i, a := range []Artifact{stage.Manifest.Installer, *stage.Manifest.Rollback} {
		name := "installer.exe"
		if i == 1 {
			name = "rollback.exe"
		}
		if err := checkFile(ctx, filepath.Join(folder, name), a, pin, verify); err != nil {
			return stage, err
		}
	}
	return stage, nil
}

type engine struct {
	verify func(context.Context, string) (signature, error)
	run    func(context.Context, string, []string) error
	health func(context.Context, string, string) error
}

func realEngine() engine {
	return engine{verifySignature, runOwned, func(ctx context.Context, exe, profile string) error {
		return runOwned(ctx, exe, []string{"--verify", "--data-dir", profile})
	}}
}
func runOwned(ctx context.Context, exe string, args []string) (result error) {
	p, err := processhost.Start(exe, args, filepath.Dir(exe), processhost.Environment())
	if err != nil {
		return err
	}
	go io.Copy(io.Discard, p.Stdout)
	go io.Copy(io.Discard, p.Stderr)
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := p.Close(stop); err != nil {
			result = errors.New("UPDATE_PROCESS_NOT_QUIET")
		}
	}()
	return p.Wait(ctx)
}
func (e engine) apply(ctx context.Context, profile, folder, installDir, current, pin string, recoverOnly bool) error {
	if err := desktop.CheckDirectLocalPath(installDir); err != nil {
		return err
	}
	if !filepath.IsAbs(installDir) || strings.ContainsAny(installDir, "\r\n\";") {
		return errors.New("UPDATE_INSTALL_DIRECTORY_INVALID")
	}
	// Pin both verified inputs against replacement through the whole transaction.
	var handles []windows.Handle
	defer func() {
		for _, h := range handles {
			windows.CloseHandle(h)
		}
	}()
	for _, name := range []string{"installer.exe", "rollback.exe"} {
		h, err := pinFile(filepath.Join(folder, name))
		if err != nil {
			return err
		}
		handles = append(handles, h)
	}
	stage, err := loadStage(ctx, profile, folder, current, pin, e.verify)
	if err != nil {
		return err
	}
	installed := filepath.Join(installDir, "Pokegents.exe")
	if !recoverOnly {
		hash, err := hashFile(installed)
		if err != nil || hash != stage.CurrentHash {
			return errors.New("UPDATE_CURRENT_BINARY_CHANGED")
		}
		if err := CheckPending(profile); err != nil {
			return err
		}
	} else {
		var j Journal
		if err := readJSON(filepath.Join(profile, pendingName), &j); err != nil || !strings.EqualFold(j.Stage, folder) || !strings.EqualFold(j.InstallDir, installDir) || j.Previous != current {
			return errors.New("UPDATE_RECOVERY_MISMATCH")
		}
	}
	backup := filepath.Join(folder, "pre-update.db")
	j := Journal{State: "installing", Stage: folder, InstallDir: installDir, Version: stage.Manifest.Installer.Version, Previous: current}
	transaction := func() error {
		healthRoot, err := os.MkdirTemp(filepath.Join(profile, "updates"), "health-")
		if err != nil {
			return err
		}
		health := filepath.Join(healthRoot, "profile")
		if err := desktop.PrepareProfile(health); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(health, "db"), 0700); err != nil {
			return err
		}
		if err := copyNew(backup, filepath.Join(health, "db", "pokegents.db")); err != nil {
			return err
		}
		if err := journal(profile, j); err != nil {
			return err
		}
		installerArgs := []string{"/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/NOCLOSEAPPLICATIONS", "/NORESTARTAPPLICATIONS", "/SP-", "/DIR=" + installDir}
		var applyErr error
		if !recoverOnly {
			applyErr = e.run(ctx, filepath.Join(folder, "installer.exe"), installerArgs)
			if applyErr == nil {
				hash, err := hashFile(installed)
				if err != nil || hash != stage.Manifest.ExecutableSHA256 {
					applyErr = errors.New("UPDATE_INSTALLED_HASH_MISMATCH")
				}
			}
			if applyErr == nil {
				sig, err := e.verify(ctx, installed)
				if err != nil || sig.Pin != pin {
					applyErr = errors.New("UPDATE_INSTALLED_SIGNATURE_INVALID")
				}
			}
			if applyErr == nil {
				applyErr = e.health(ctx, installed, health)
			}
		} else {
			applyErr = errors.New("UPDATE_RECOVERY_REQUESTED")
		}
		if applyErr == nil {
			j.State = "completed"
			return journal(profile, j)
		}
		j.State = "rolling_back"
		j.Error = "UPDATE_INSTALL_OR_HEALTH_FAILED"
		if err := journal(profile, j); err != nil {
			return err
		}
		// Use a fresh deadline after an installer timeout/cancellation. The actual
		// profile was never opened by the candidate; only its private copy was tested.
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		rollbackErr := e.run(rollbackCtx, filepath.Join(folder, "rollback.exe"), installerArgs)
		if rollbackErr == nil {
			hash, err := hashFile(installed)
			if err != nil || hash != stage.CurrentHash {
				rollbackErr = errors.New("UPDATE_ROLLBACK_HASH_MISMATCH")
			}
		}
		if rollbackErr != nil {
			j.State = "failed"
			_ = journal(profile, j)
			return errors.New("UPDATE_ROLLBACK_FAILED: retain stage and pre-update.db; explicit recovery required")
		}
		j.State = "rolled_back"
		if err := journal(profile, j); err != nil {
			return err
		}
		return errors.New("UPDATE_ROLLED_BACK: candidate failed; previous binary and original profile preserved")
	}
	if recoverOnly { // Retain the original backup; do not replace it with changed state.
		instance, primary, err := desktop.TryLockProfile(profile)
		if err != nil {
			return err
		}
		if !primary {
			return errors.New("PROFILE_IN_USE")
		}
		defer instance.Close()
		return transaction()
	}
	return maintenance.WithOfflineBackup(ctx, profile, backup, transaction)
}

// Launching a copy lets Inno replace the installed executable after this command
// exits. No user application is terminated, and the worker must acquire the lock.
func launchWorker(profile, folder, installDir, exe, current string, recoverOnly bool) error {
	if err := localStage(profile, folder); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sig, err := verifySignature(ctx, exe)
	if err != nil {
		return err
	}
	stage, err := loadStage(ctx, profile, folder, current, sig.Pin, verifySignature)
	if err != nil {
		return err
	}
	hash, err := hashFile(exe)
	if err != nil || hash != stage.CurrentHash {
		return errors.New("UPDATE_CURRENT_BINARY_CHANGED")
	}
	helper := filepath.Join(folder, fmt.Sprintf("worker-%d.exe", time.Now().UnixNano()))
	if err := copyNew(exe, helper); err != nil {
		return err
	}
	if sig2, err := verifySignature(ctx, helper); err != nil || sig2.Pin != sig.Pin {
		return errors.New("UPDATE_SIGNATURE_INVALID")
	}
	args := []string{"update-worker", "--data-dir", profile, "--stage", folder, "--install-dir", installDir, "--parent", fmt.Sprint(os.Getpid())}
	if recoverOnly {
		args = append(args, "--recover")
	}
	cmd := exec.Command(helper, args...)
	cmd.Env = processhost.Environment()
	cmd.Dir = folder
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS, HideWindow: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func RunCLI(ctx context.Context, args []string, current string, worker bool) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	action := flags.String("action", "check", "check, stage, apply or recover")
	source := flags.String("source", "", "Explicit HTTPS release manifest URL")
	profile := flags.String("data-dir", "", "Application profile absolute path")
	folder := flags.String("stage", "", "Verified stage directory")
	install := flags.String("install-dir", "", "Installed application directory")
	parent := flags.Uint("parent", 0, "Launcher PID")
	recovery := flags.Bool("recover", false, "Recovery worker")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("UPDATE_INVALID_ARGUMENTS")
	}
	if _, err := version(current); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if worker {
		if *parent == 0 || *parent > 0xffffffff {
			return errors.New("UPDATE_PARENT_REQUIRED")
		}
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(*parent))
		if err == nil {
			result, waitErr := windows.WaitForSingleObject(h, 30000)
			windows.CloseHandle(h)
			if waitErr != nil || result != windows.WAIT_OBJECT_0 {
				return errors.New("UPDATE_PARENT_STILL_RUNNING")
			}
		} else if err != windows.ERROR_INVALID_PARAMETER {
			return err
		}
		sig, err := verifySignature(ctx, exe)
		if err != nil {
			return err
		}
		err = realEngine().apply(ctx, *profile, *folder, *install, current, sig.Pin, *recovery)
		result := map[string]any{"ok": err == nil}
		if err != nil {
			result["error"] = err.Error()
		}
		_ = writeNew(filepath.Join(*folder, fmt.Sprintf("apply-result-%d.json", time.Now().UnixNano())), result)
		return err
	}
	switch *action {
	case "check":
		m, pin, err := Check(ctx, exe, current, *source)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"manifest": m, "signer_sha256": pin})
	case "stage":
		folder, err := Prepare(ctx, *profile, exe, current, *source)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"stage": folder})
	case "apply", "recover":
		if *install == "" {
			*install = filepath.Dir(exe)
		}
		if *action == "apply" && !strings.EqualFold(filepath.Join(*install, "Pokegents.exe"), exe) {
			return errors.New("UPDATE_RUN_INSTALLED_EXECUTABLE_REQUIRED")
		}
		if err := launchWorker(*profile, *folder, *install, exe, current, *action == "recover"); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"status": "worker_started", "result_directory": *folder})
	default:
		return errors.New("UPDATE_INVALID_ACTION")
	}
}
