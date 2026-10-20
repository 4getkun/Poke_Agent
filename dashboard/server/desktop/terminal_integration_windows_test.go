//go:build windows && integration

package desktop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsTerminalChild(t *testing.T) {
	explicit := false
	for _, arg := range os.Args {
		if arg == "-test.run=^TestWindowsTerminalChild$" {
			explicit = true
		}
	}
	if !explicit {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(cwd), "Terminal 日本語") {
		t.Fatal("refusing to write outside the isolated probe cwd")
	}
	data, _ := json.Marshal(map[string]string{"cwd": cwd, "result": "native-child-completed"})
	if err := os.WriteFile(filepath.Join(cwd, "terminal-probe.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledWindowsTerminalPreservesWorkspaceArguments(t *testing.T) {
	executable, err := terminalExecutable()
	if err != nil {
		t.Fatal("Windows Terminal execution alias required for this explicit native test: ", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	directory := filepath.Join(root, "Terminal 日本語 & () [x]")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	args, err := terminalArguments(filepath.Join(root, "profile"), directory, "open")
	if err != nil {
		t.Fatal(err)
	}
	args[6] = "Pokegents Terminal integration test"
	args = append(args, self, "-test.run=^TestWindowsTerminalChild$", "-test.v")
	cmd := exec.Command(executable, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Release(); err != nil {
		t.Fatal(err)
	}
	// A single clearly named test window runs only the bounded helper above.
	// Its successful child exits normally; no existing Terminal is terminated.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(directory, "terminal-probe.json"))
		if err == nil {
			var probe map[string]string
			if json.Unmarshal(data, &probe) != nil || probe["result"] != "native-child-completed" || !strings.EqualFold(probe["cwd"], directory) {
				t.Fatalf("Terminal altered cwd/arguments: %s", data)
			}
			t.Log("installed Windows Terminal launched the bounded native helper in the exact Unicode/space/symbol workspace; no agent or user Terminal process was stopped; focus rendering was not asserted")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Windows Terminal did not execute the isolated probe; check its labeled test window for diagnostics")
}
