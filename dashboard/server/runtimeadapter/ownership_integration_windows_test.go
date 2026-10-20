//go:build windows && integration

package runtimeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/processhost"
)

// This helper is deliberately a different process: killing a client object in
// the test process cannot prove kill-on-owner-exit for the inner Job handle.
func TestRealCodexJobFixture(t *testing.T) {
	mode := os.Getenv("POKEGENTS_REAL_JOB_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "independent" {
		fmt.Println("independent-ready")
		bufio.NewReader(os.Stdin).ReadString('\n')
		os.Exit(0)
	}
	if mode == "sandbox-tool" {
		// Even knowing its name must not let a restricted sandbox hold another
		// handle to the owner's Job and defeat kill-on-last-handle-close.
		jobName, _ := windows.UTF16PtrFromString(os.Getenv("POKEGENTS_TEST_PRIVATE_JOB"))
		openJob := windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW")
		if h, _, _ := openJob.Call(0x0004, 0, uintptr(unsafe.Pointer(jobName))); h != 0 {
			windows.CloseHandle(windows.Handle(h))
			fmt.Println("sandbox retained access to supervisor job")
			os.Exit(15)
		}
		cwd, err := os.Getwd()
		if err != nil {
			os.Exit(10)
		}
		writeErr := os.WriteFile(filepath.Join(cwd, "must-not-exist.txt"), []byte("synthetic write probe"), 0600)
		if (writeErr == nil) != (os.Getenv("POKEGENTS_REAL_JOB_POLICY") == "workspaceWrite") {
			os.Exit(11)
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(cwd), "outside-must-not-exist.txt"), []byte("denied"), 0600); err == nil {
			os.Exit(16)
		}
		path, _ := windows.UTF16PtrFromString(filepath.Join(cwd, "held.lock"))
		var lock windows.Handle
		deadline := time.Now().Add(2 * time.Second)
		for {
			lock, err = windows.CreateFile(path, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
			// The supervisor's readiness probe briefly opens this same file.
			// Only that sharing race may be retried; access denial still fails.
			if err != windows.ERROR_SHARING_VIOLATION || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "sandbox fixture exclusive lock: %v\n", err)
			os.Exit(12)
		}
		defer windows.CloseHandle(lock)
		time.Sleep(40 * time.Second)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dir := os.Getenv("POKEGENTS_REAL_JOB_DIR")
	exe := os.Getenv("POKEGENTS_REAL_JOB_CODEX")
	version, err := Probe(ctx, exe, dir)
	if err != nil || version != SupportedVersion {
		fmt.Println("fixture-error: pinned runtime probe")
		os.Exit(2)
	}
	ownerRecord, err := processhost.NewOwnership()
	if err != nil {
		os.Exit(14)
	}
	c, err := StartOwned(ctx, exe, dir, ownerRecord)
	if err != nil {
		fmt.Println("fixture-error: runtime start")
		os.Exit(3)
	}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 4*time.Second)
		defer done()
		c.Close(stop)
	}()
	// All paths are fixed names inside this test's temporary cwd. No prompt,
	// repository data, or credentials are sent to an LLM. The command runs through
	// the real app-server sandbox and holds an exclusive read lock until stopped.
	self, err := os.Executable()
	if err != nil {
		os.Exit(13)
	}
	callDone := make(chan error, 1)
	policy := os.Getenv("POKEGENTS_REAL_JOB_POLICY")
	sandbox := map[string]any{"type": policy, "networkAccess": false}
	if policy == "workspaceWrite" {
		sandbox["writableRoots"] = []string{dir}
		sandbox["excludeTmpdirEnvVar"], sandbox["excludeSlashTmp"] = true, true
	}
	go func() {
		// Preserve the RPC rejection only for this synthetic command fixture.
		key := fmt.Sprint(c.seq.Add(1))
		response := make(chan Frame, 1)
		c.mu.Lock()
		c.pending[key] = response
		c.mu.Unlock()
		defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
		err := c.send(map[string]any{"id": json.Number(key), "method": "command/exec", "params": map[string]any{
			"command": []string{self, "-test.run=^TestRealCodexJobFixture$", "-test.timeout=50s"},
			"env":     map[string]string{"POKEGENTS_REAL_JOB_FIXTURE": "sandbox-tool", "POKEGENTS_TEST_PRIVATE_JOB": ownerRecord.JobName, "POKEGENTS_REAL_JOB_POLICY": policy},
			"cwd":     dir, "sandboxPolicy": sandbox,
			"timeoutMs": 40000,
		}})
		if err != nil {
			callDone <- err
			return
		}
		select {
		case f := <-response:
			if len(f.Error) != 0 {
				callDone <- fmt.Errorf("RPC rejection: %.600s", f.Error)
			} else {
				callDone <- fmt.Errorf("command exited: %.600s", f.Result)
			}
		case <-ctx.Done():
			callDone <- ctx.Err()
		case <-c.done:
			callDone <- fmt.Errorf("runtime disconnected")
		}
	}()
	// Pinned Windows sandbox rejects streaming command/exec. Observe readiness
	// using its exclusive file lock plus a live descendant snapshot instead.
	lockPath, _ := windows.UTF16PtrFromString(filepath.Join(dir, "held.lock"))
	for {
		select {
		case err := <-callDone:
			fmt.Printf("fixture-error: command ended before ready (%v)\n", err)
			os.Exit(4)
		case <-ctx.Done():
			fmt.Println("fixture-error: sandbox startup timed out")
			os.Exit(5)
		case <-time.After(20 * time.Millisecond):
		}
		lock, lockErr := windows.CreateFile(lockPath, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if lockErr == nil {
			windows.CloseHandle(lock)
			continue
		}
		pid := sandboxDescendant(c.process.PID, filepath.Base(self))
		if lockErr == windows.ERROR_SHARING_VIOLATION && pid != 0 {
			identity, _ := json.Marshal(ownerRecord)
			fmt.Printf("ready %d %d %s\n", c.process.PID, pid, identity)
			// Parent either kills this exact owner handle or requests normal close.
			bufio.NewReader(os.Stdin).ReadString('\n')
			stop, done := context.WithTimeout(context.Background(), 4*time.Second)
			closeErr := c.Close(stop)
			done()
			if closeErr != nil {
				fmt.Println("fixture-error: close deadline")
				os.Exit(8)
			}
			fmt.Println("closed")
			return
		}
	}
}

func sandboxDescendant(root uint32, executable string) uint32 {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := map[uint32]uint32{}
	var candidates []uint32
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
		if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), executable) {
			candidates = append(candidates, entry.ProcessID)
		}
	}
	for _, pid := range candidates {
		seen := map[uint32]bool{}
		for ancestor := parents[pid]; ancestor != 0 && !seen[ancestor]; ancestor = parents[ancestor] {
			if ancestor == root {
				return pid
			}
			seen[ancestor] = true
		}
	}
	return 0
}

func TestRealCodexNestedJobShutdown(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_CODEX_EXE")
	if exe == "" {
		t.Fatal("explicit pinned native Codex exe required")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct{ mode, policy string }{{"normal-close", "readOnly"}, {"owner-crash", "readOnly"}, {"normal-close", "workspaceWrite"}, {"owner-crash", "workspaceWrite"}} {
		mode, policy := scenario.mode, scenario.policy
		t.Run(policy+"/"+mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "日本語 workspace & [job]")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "held.lock"), []byte("synthetic lock fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			start := func(kind string) *processhost.Process {
				env := append(processhost.Environment(), "POKEGENTS_REAL_JOB_FIXTURE="+kind, "POKEGENTS_REAL_JOB_DIR="+dir, "POKEGENTS_REAL_JOB_CODEX="+exe, "POKEGENTS_REAL_JOB_POLICY="+policy)
				p, err := processhost.Start(self, []string{"-test.run=^TestRealCodexJobFixture$", "-test.timeout=55s"}, dir, env)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					stop, done := context.WithTimeout(context.Background(), 4*time.Second)
					defer done()
					p.Close(stop)
				})
				go io.Copy(io.Discard, p.Stderr)
				return p
			}
			readLine := func(r *bufio.Reader) string {
				lines := make(chan string, 1)
				go func() { line, _ := r.ReadString('\n'); lines <- strings.TrimSpace(line) }()
				select {
				case line := <-lines:
					return line
				case <-time.After(45 * time.Second):
					t.Fatal("fixture output deadline")
					return ""
				}
			}
			independent := start("independent")
			if readLine(bufio.NewReader(independent.Stdout)) != "independent-ready" {
				t.Fatal("independent fixture not ready")
			}
			owner := start("owner")
			reader := bufio.NewReader(owner.Stdout)
			line := readLine(reader)
			var runtimePID, toolPID uint32
			if _, err := fmt.Sscanf(line, "ready %d %d", &runtimePID, &toolPID); err != nil || runtimePID == 0 || toolPID == 0 {
				t.Fatalf("real sandbox did not become ready: %s", line)
			}
			var ownership processhost.Ownership
			fields := strings.Fields(line)
			if len(fields) != 4 || json.Unmarshal([]byte(fields[3]), &ownership) != nil {
				t.Fatal("missing persisted owner identity")
			}
			if processhost.VerifyReleased(ownership) == nil {
				t.Fatal("live owner accepted as released")
			}
			// Open exact live handles before inducing failure. Retain the outer Job
			// until after assertions so it cannot mask a leaked inner runtime tree.
			var handles []windows.Handle
			for _, pid := range []uint32{owner.PID, runtimePID, toolPID} {
				h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
				if err != nil {
					t.Fatal(err)
				}
				handles = append(handles, h)
				defer windows.CloseHandle(h)
			}
			lockPath, _ := windows.UTF16PtrFromString(filepath.Join(dir, "held.lock"))
			lock, lockErr := windows.CreateFile(lockPath, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
			if lockErr == nil {
				windows.CloseHandle(lock)
				t.Fatal("sandbox command did not hold its lock")
			}
			if lockErr != windows.ERROR_SHARING_VIOLATION {
				t.Fatalf("unexpected lock failure: %v", lockErr)
			}
			_, writeErr := os.Stat(filepath.Join(dir, "must-not-exist.txt"))
			if (writeErr == nil) != (policy == "workspaceWrite") {
				t.Fatal("sandbox did not apply its workspace write policy")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside-must-not-exist.txt")); !os.IsNotExist(err) {
				t.Fatal("sandbox allowed a write outside the workspace")
			}
			began := time.Now()
			if mode == "owner-crash" {
				if err := windows.TerminateProcess(handles[0], 19); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := io.WriteString(owner.Stdin, "close\n"); err != nil {
					t.Fatal(err)
				}
				if line := readLine(reader); line != "closed" {
					t.Fatalf("normal close failed: %s", line)
				}
			}
			for i, h := range handles {
				remaining := 5*time.Second - time.Since(began)
				if remaining < 0 {
					remaining = 0
				}
				status, err := windows.WaitForSingleObject(h, uint32(remaining.Milliseconds()))
				if err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatalf("owned process %d survived %s: status=%d err=%v", i, mode, status, err)
				}
			}
			lock, err = windows.CreateFile(lockPath, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0)
			if err != nil {
				t.Fatalf("sandbox lock survived: %v", err)
			}
			windows.CloseHandle(lock)
			if err := processhost.VerifyReleased(ownership); err != nil {
				t.Fatalf("dead owner and released job not verified: %v", err)
			}
			select {
			case <-independent.Done():
				t.Fatal("unrelated independent process was stopped")
			default:
			}
			t.Logf("pinned Codex %s, real %s sandbox command and Job access denial: %s reclaimed runtime/tool and lock in %s; independent process alive", SupportedVersion, policy, mode, time.Since(began).Round(time.Millisecond))
		})
	}
}
