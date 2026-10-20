//go:build darwin

package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ITerm2Terminal implements TerminalIntegration for macOS using
// iTerm2 AppleScript automation.
type ITerm2Terminal struct{}

// NewTerminal returns the platform terminal integration.
func NewTerminal() TerminalIntegration {
	return &ITerm2Terminal{}
}

func (t *ITerm2Terminal) IsAvailable() bool {
	return true
}

func (t *ITerm2Terminal) FocusSession(itermSessionID, tty string) error {
	script := buildFocusScript(itermSessionID, tty)
	err := exec.Command("osascript", "-e", script).Run()
	// Focus the target window via SkyLight private API (runs as separate process, fast)
	if focusWindowBin != "true" {
		if pidBytes, pidErr := exec.Command("pgrep", "-x", "iTerm2").Output(); pidErr == nil {
			pid := strings.TrimSpace(string(pidBytes))
			exec.Command(focusWindowBin, pid, "0").Run()
		}
	}
	return err
}

func (t *ITerm2Terminal) WriteText(itermSessionID, tty, text string) error {
	// Collapse newlines to spaces — multi-line text triggers iTerm2 paste bracketing
	// which shows "[Pasted Text: X lines]" and waits for manual confirmation
	text = strings.Join(strings.Fields(text), " ")
	script := buildWriteScript(itermSessionID, tty, text)
	return exec.Command("osascript", "-e", script).Run()
}

// IsSessionFocused checks if the given iTerm2 session is the currently selected session
// in the frontmost window. Used to avoid nudging a terminal the user is actively typing in.
func (t *ITerm2Terminal) IsSessionFocused(itermSessionID, tty string) bool {
	script := fmt.Sprintf(`
tell application "iTerm2"
	if (count of windows) = 0 then return "no"
	tell current window
		tell current session of current tab
			set sid to id
			set stty to tty
		end tell
	end tell
	if "%s" is not "" and sid = "%s" then return "yes"
	if "%s" is not "" and stty = "%s" then return "yes"
	return "no"
end tell`, itermSessionID, itermSessionID, tty, tty)
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "yes"
}

func (t *ITerm2Terminal) SetTabName(itermSessionID, tty, name string) error {
	script := buildSetTabNameScript(itermSessionID, tty, name)
	return exec.Command("osascript", "-e", script).Run()
}

func (t *ITerm2Terminal) CloseSession(itermSessionID, tty string) error {
	script := buildCloseScript(itermSessionID, tty)
	return exec.Command("osascript", "-e", script).Run()
}

func (t *ITerm2Terminal) CloneSession(profile, sessionIDPrefix string) error {
	safeProfile := strings.ReplaceAll(profile, `"`, `\"`)
	safeSID := strings.ReplaceAll(sessionIDPrefix, `"`, `\"`)
	// Delay 1s after creating tab to let the shell initialize PATH/shims.
	script := fmt.Sprintf(`
tell application "iTerm2"
	tell current window
		create tab with default profile
		delay 1
		tell current session
			write text "pokegent %s --resume %s --fork-session"
		end tell
	end tell
end tell`, safeProfile, safeSID)
	return exec.Command("osascript", "-e", script).Run()
}

func (t *ITerm2Terminal) LaunchProfile(opts LaunchOptions) error {
	safeProfile := strings.ReplaceAll(opts.Profile, `"`, `\"`)
	createTab := `create tab with default profile`
	if opts.ITermProfile != "" {
		safeITermProf := strings.ReplaceAll(opts.ITermProfile, `"`, `\"`)
		createTab = fmt.Sprintf(`create tab with profile "%s"`, safeITermProf)
	}
	cmd := fmt.Sprintf("pokegent %s", safeProfile)
	// --pokegent-id pins the identity (so dashboard's pre-written running file
	// and pokegent.sh's later writes agree on the same pokegent_id).
	if opts.PokegentID != "" {
		safePGID := strings.ReplaceAll(opts.PokegentID, `"`, `\"`)
		cmd = fmt.Sprintf("%s --pokegent-id %s", cmd, safePGID)
	}
	if opts.TaskGroup != "" {
		safeGroup := strings.ReplaceAll(opts.TaskGroup, `"`, `\"`)
		cmd = fmt.Sprintf("%s --group %s", cmd, safeGroup)
	}
	if opts.HandoffContextPath != "" {
		cmd = fmt.Sprintf("%s --handoff-context-file %s", cmd, shellQuote(opts.HandoffContextPath))
	}
	cmd = strings.ReplaceAll(cmd, `"`, `\"`)
	script := fmt.Sprintf(`
tell application "iTerm2"
	tell current window
		%s
		delay 1
		tell current session
			write text "%s"
		end tell
	end tell
end tell`, createTab, cmd)
	return exec.Command("osascript", "-e", script).Run()
}

func (t *ITerm2Terminal) ResumeSession(profile, sessionID, compact string) error {
	return t.ResumePokegent(profile, sessionID, "", compact)
}

// ResumePokegent resumes a Claude session while forcing pokegent.sh to reuse the
// given pokegent_id — this prevents a new identity file from being minted.
func (t *ITerm2Terminal) ResumePokegent(profile, sessionID, pokegentID, compact string) error {
	safeProfile := strings.ReplaceAll(profile, `"`, `\"`)
	safeSession := strings.ReplaceAll(sessionID, `"`, `\"`)
	safePgid := strings.ReplaceAll(pokegentID, `"`, `\"`)
	pgidArg := ""
	if safePgid != "" {
		pgidArg = fmt.Sprintf(" --pokegent-id %s", safePgid)
	}
	// Delay 1s after creating tab to let the shell initialize PATH/shims.
	autoAnswer := ""
	if compact == "yes" {
		autoAnswer = `
			delay 5
			write text "yes"`
	}
	script := fmt.Sprintf(`
tell application "iTerm2"
	tell current window
		create tab with default profile
		delay 1
		tell current session
			write text "pokegent %s -r %s%s"%s
		end tell
	end tell
end tell`, safeProfile, safeSession, pgidArg, autoAnswer)
	return exec.Command("osascript", "-e", script).Run()
}

// buildFocusScript returns an AppleScript that finds and activates the correct iTerm2 session.
// Primary: match by iTerm2 session UUID (stable, unique, survives tab moves).
// Fallback: match by TTY (for sessions launched before iterm_session_id was stored).
func buildFocusScript(itermSessionID, tty string) string {
	safeTTY := strings.ReplaceAll(tty, `"`, `\"`)
	return fmt.Sprintf(`
tell application "iTerm2"
	set targetID to "%s"
	set targetTTY to "%s"
	set targetWinIdx to -1
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if targetID is not "" and (id of s) = targetID then
					tell t to select
					tell s to select
					set targetWinIdx to index of w
				end if
			end repeat
		end repeat
	end repeat
	-- Fallback: match by TTY
	if targetWinIdx = -1 and targetTTY is not "" then
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s = targetTTY then
						tell t to select
						tell s to select
						set targetWinIdx to index of w
					end if
				end repeat
			end repeat
		end repeat
	end if
	if targetWinIdx = -1 then return
	-- Make target iTerm's frontmost window within iTerm
	set index of window targetWinIdx to 1
end tell`, itermSessionID, safeTTY)
}

var focusWindowBin string

func init() {
	// Resolve the focus-window helper path relative to the executable
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "helpers", "focus-window")
		if _, err := os.Stat(candidate); err == nil {
			focusWindowBin = candidate
		}
	}
	if focusWindowBin == "" {
		focusWindowBin = "true" // no-op fallback
	}
}

// buildWriteScript returns an AppleScript that types text into the correct iTerm2 session.
// Primary: match by iTerm2 session UUID. Fallback: match by TTY.
func buildWriteScript(itermSessionID, tty, prompt string) string {
	safeTTY := strings.ReplaceAll(tty, `"`, `\"`)
	safePrompt := strings.ReplaceAll(prompt, `\`, `\\`)
	safePrompt = strings.ReplaceAll(safePrompt, `"`, `\"`)

	return fmt.Sprintf(`
tell application "iTerm2"
	set targetID to "%s"
	set targetTTY to "%s"
	-- Primary: match by iTerm2 session UUID
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if targetID is not "" and (id of s) = targetID then
					tell s to write text (character id 21) newline NO
					delay 0.05
					tell s to write text "%s" newline NO
					delay 0.05
					tell s to write text (character id 13) newline NO
					return
				end if
			end repeat
		end repeat
	end repeat
	-- Fallback: match by TTY
	if targetTTY is not "" then
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s = targetTTY then
						tell s to write text (character id 21) newline NO
						delay 0.05
						tell s to write text "%s" newline NO
						delay 0.05
						tell s to write text (character id 13) newline NO
						return
					end if
				end repeat
			end repeat
		end repeat
	end if
end tell`, itermSessionID, safeTTY, safePrompt, safePrompt)
}

// buildCloseScript returns an AppleScript that closes the iTerm2 session/tab.
func buildCloseScript(itermSessionID, tty string) string {
	safeTTY := strings.ReplaceAll(tty, `"`, `\"`)
	return fmt.Sprintf(`
tell application "iTerm2"
	set targetID to "%s"
	set targetTTY to "%s"
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if targetID is not "" and (id of s) = targetID then
					tell s to close
					return
				end if
			end repeat
		end repeat
	end repeat
	if targetTTY is not "" then
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s = targetTTY then
						tell s to close
						return
					end if
				end repeat
			end repeat
		end repeat
	end if
end tell`, itermSessionID, safeTTY)
}

// buildSetTabNameScript returns an AppleScript that renames the iTerm2 tab.
func buildSetTabNameScript(itermSessionID, tty, name string) string {
	safeTTY := strings.ReplaceAll(tty, `"`, `\"`)
	safeName := strings.ReplaceAll(name, `"`, `\"`)
	return fmt.Sprintf(`
tell application "iTerm2"
	set targetID to "%s"
	set targetTTY to "%s"
	set newName to "%s"
	repeat with w in windows
		repeat with t in tabs of w
			repeat with s in sessions of t
				if targetID is not "" and (id of s) = targetID then
					tell s to set name to newName
					return
				end if
			end repeat
		end repeat
	end repeat
	if targetTTY is not "" then
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if tty of s = targetTTY then
						tell s to set name to newName
						return
					end if
				end repeat
			end repeat
		end repeat
	end if
end tell`, itermSessionID, safeTTY, safeName)
}
