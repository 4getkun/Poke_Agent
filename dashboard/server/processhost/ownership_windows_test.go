//go:build windows

package processhost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestOwnershipRecordFixture(t *testing.T) {
	if os.Getenv("POKEGENTS_OWNER_FIXTURE") != "1" {
		return
	}
	owner, err := NewOwnership()
	if err != nil {
		os.Exit(2)
	}
	data, _ := json.Marshal(owner)
	fmt.Println(string(data))
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(0)
}

func TestOwnerMustExitBeforeEmptyNamedJobCanBeVerified(t *testing.T) {
	exe, _ := os.Executable()
	p, err := Start(exe, []string{"-test.run=^TestOwnershipRecordFixture$"}, t.TempDir(), append(Environment(), "POKEGENTS_OWNER_FIXTURE=1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer p.Close(ctx)
	var owner Ownership
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	if err != nil || json.Unmarshal([]byte(line), &owner) != nil {
		t.Fatal("missing fixture owner")
	}
	if VerifyReleased(owner) == nil {
		t.Fatal("live owner was accepted")
	}
	io.WriteString(p.Stdin, "exit\n")
	if err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := newJob(owner.JobName)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	if _, err := newJob(owner.JobName); err == nil {
		t.Fatal("existing named Job was adopted")
	}
	if err := VerifyReleased(owner); err != nil {
		t.Fatalf("exited exact owner and empty protected Job: %v", err)
	}
	for _, invalid := range []Ownership{{}, {Version: 1, PID: owner.PID, Created: "invalid", JobName: owner.JobName}, {Version: 1, PID: owner.PID, Created: owner.Created, Session: owner.Session + 1, JobName: owner.JobName}} {
		if VerifyReleased(invalid) == nil {
			t.Fatal("invalid or cross-session ownership accepted")
		}
	}
}
