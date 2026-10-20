//go:build windows

package processhost

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCrashProcessFixture(t *testing.T) {
	mode := os.Getenv("POKEGENTS_CRASH_FIXTURE")
	if mode == "" {
		return
	}
	dir := os.Getenv("POKEGENTS_CRASH_DIR")
	exe, _ := os.Executable()
	if mode == "owner" {
		fmt.Println("starting")
		p, err := Start(exe, []string{"-test.run=^TestCrashProcessFixture$"}, dir, append(Environment(), "POKEGENTS_CRASH_FIXTURE=root", "POKEGENTS_CRASH_DIR="+dir))
		if err != nil {
			fmt.Println("error")
			os.Exit(2)
		}
		line, err := bufio.NewReader(p.Stdout).ReadString('\n')
		if err != nil {
			os.Exit(3)
		}
		fmt.Print(line)
		bufio.NewReader(os.Stdin).ReadString('\n')
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		p.Close(ctx)
		os.Exit(0)
	}
	lock, err := os.OpenFile(filepath.Join(dir, mode+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		os.Exit(4)
	}
	defer lock.Close()
	if err = os.WriteFile(filepath.Join(dir, mode+".pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(5)
	}
	if mode == "root" {
		cmd := exec.Command(exe, "-test.run=^TestCrashProcessFixture$")
		cmd.Env = append(Environment(), "POKEGENTS_CRASH_FIXTURE=grand", "POKEGENTS_CRASH_DIR="+dir)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err = cmd.Start(); err != nil {
			os.Exit(6)
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, e := os.Stat(filepath.Join(dir, "grand.pid")); e == nil {
				fmt.Println("ready")
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestOwnerCrashReclaimsChildrenAndGrandchildren(t *testing.T) {
	exe, _ := os.Executable()
	base := t.TempDir()
	for i := 0; i < 100; i++ {
		dir := filepath.Join(base, strconv.Itoa(i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=^TestCrashProcessFixture$")
		cmd.Env = append(Environment(), "POKEGENTS_CRASH_FIXTURE=owner", "POKEGENTS_CRASH_DIR="+dir)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		// Keep the owner's shutdown input open until after the forced kill.
		// A nil stdin is /dev/null: ReadString immediately saw EOF and could
		// gracefully close the Job before the test injected an owner crash.
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stdin.Close() })
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stdout)
		line, err := reader.ReadString('\n')
		if err != nil || line != "starting\n" {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("owner startup %q %v", line, err)
		}
		if i%2 == 0 {
			line, err = reader.ReadString('\n')
			if err != nil || line != "ready\n" {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("descendant startup %q %v", line, err)
			}
		} else {
			time.Sleep(time.Duration(i%7) * time.Millisecond)
		}
		if err = cmd.Process.Kill(); err != nil {
			t.Fatalf("iteration=%d owner crash injection failed: %v", i, err)
		}
		cmd.Wait()
		stdin.Close()
		stdout.Close()
		deadline := time.Now().Add(5 * time.Second)
		for _, name := range []string{"root", "grand"} {
			pidData, e := os.ReadFile(filepath.Join(dir, name+".pid"))
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				t.Fatal(e)
			}
			pid, e := strconv.ParseUint(string(pidData), 10, 32)
			if e != nil {
				t.Fatal(e)
			}
			h, e := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if e == nil {
				remaining := time.Until(deadline)
				if remaining < 0 {
					remaining = 0
				}
				status, waitErr := windows.WaitForSingleObject(h, uint32(remaining.Milliseconds()))
				windows.CloseHandle(h)
				if waitErr != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatalf("iteration=%d %s survived owner crash", i, name)
				}
			}
			path := filepath.Join(dir, name+".lock")
			for {
				e = os.Remove(path)
				if e == nil || os.IsNotExist(e) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("iteration=%d locked %s: %v", i, name, e)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	t.Log("100 owner crashes: 50 established child/grandchild trees, 50 creation-time fault injections; no surviving fixture processes or locks")
}
