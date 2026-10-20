//go:build windows

package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	d "pokegents/dashboard/server/taskdomain"
)

// Resolve only the OS user's documented Windows Terminal execution alias.
// Do not search a project directory or PATH for an executable named wt.exe.
func terminalExecutable() (string, error) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", err
	}
	path := filepath.Join(local, "Microsoft", "WindowsApps", "wt.exe")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", d.Fail("TERMINAL_UNAVAILABLE")
	}
	return path, nil
}

func TerminalAvailable() bool { _, err := terminalExecutable(); return err == nil }

func terminalArguments(profile, directory, mode string) ([]string, error) {
	if !filepath.IsAbs(directory) || strings.HasPrefix(directory, `\\`) || strings.ContainsAny(directory, ";\x00\r\n\t\"") {
		return nil, d.Fail("TERMINAL_PATH_UNSUPPORTED")
	}
	directory = filepath.Clean(directory)
	hash := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(profile)) + "|" + strings.ToLower(directory)))
	window := "Pokegents-" + hex.EncodeToString(hash[:10])
	args := []string{"--window", window}
	switch mode {
	case "open":
		return append(args, "new-tab", "--startingDirectory", directory, "--title", "Pokegents", "--suppressApplicationTitle"), nil
	case "focus":
		return append(args, "focus-tab", "--target", "0"), nil
	default:
		return nil, d.Fail("INVALID_REQUEST")
	}
}

// OpenTerminal requests an independent user terminal with its configured
// default profile. It does not attach that terminal to an application-owned run.
// In particular, never kill Terminal on a timeout or on Pokegents shutdown.
func (h *Host) OpenTerminal(directory, mode string) error {
	executable, err := terminalExecutable()
	if err != nil {
		return err
	}
	args, err := terminalArguments(h.options.DataPath, directory, mode)
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = directory
	if err := cmd.Start(); err != nil {
		return d.Fail("TERMINAL_REQUEST_FAILED")
	}
	return cmd.Process.Release()
}
