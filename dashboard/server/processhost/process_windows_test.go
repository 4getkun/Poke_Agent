//go:build windows

package processhost

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestProcessFixture(t *testing.T) {
	if os.Getenv("POKEGENTS_PROCESS_FIXTURE") != "1" {
		return
	}
	fmt.Println("日本語 & % ! ( )")
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

func TestAtomicJobPipesAndCleanup(t *testing.T) {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	env := append(Environment(), "POKEGENTS_PROCESS_FIXTURE=1")
	p, err := Start(exe, []string{"-test.run=^TestProcessFixture$"}, cwd, env)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	defer p.Close(ctx)
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	if err != nil || line != "日本語 & % ! ( )\n" {
		t.Fatalf("output %q: %v", line, err)
	}
	if _, err = p.Stdin.WriteString("exit\n"); err != nil {
		t.Fatal(err)
	}
	if err = p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRejectShellAndRelativeExecutable(t *testing.T) {
	for _, exe := range []string{"codex.exe", `C:\runtime\codex.cmd`, `C:\runtime\codex.ps1`} {
		if _, err := Start(exe, nil, `C:\`, Environment()); err == nil {
			t.Fatal(exe)
		}
	}
}

func TestEnvironmentDoesNotLeakCredentials(t *testing.T) {
	t.Setenv("POKEGENTS_TOKEN", "private")
	t.Setenv("OPENAI_API_KEY", "private")
	for _, v := range Environment() {
		if v == "POKEGENTS_TOKEN=private" || v == "OPENAI_API_KEY=private" {
			t.Fatal("credential inherited")
		}
	}
}
