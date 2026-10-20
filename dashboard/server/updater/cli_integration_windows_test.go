//go:build windows && integration

package updater

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"pokegents/dashboard/server/processhost"
	"strings"
	"testing"
	"time"
)

func TestBuiltUpdateCLIGates(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_UPDATE_EXE")
	if exe == "" {
		t.Skip("set POKEGENTS_TEST_UPDATE_EXE to the unsigned built executable")
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		p, err := processhost.Start(exe, args, filepath.Dir(exe), processhost.Environment())
		if err != nil {
			t.Fatal(err)
		}
		stderr := make(chan string, 1)
		go func() { b, _ := io.ReadAll(io.LimitReader(p.Stderr, 16384)); stderr <- string(b) }()
		go io.Copy(io.Discard, p.Stdout)
		err = p.Wait(ctx)
		stop, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		if closeErr := p.Close(stop); closeErr != nil {
			t.Fatal(closeErr)
		}
		return <-stderr, err
	}
	profile := filepath.Join(t.TempDir(), "profile")
	output, err := run("update", "--action", "stage", "--source", "https://example.invalid/release-manifest.json", "--data-dir", profile)
	if err == nil || !strings.Contains(output, "UPDATE_SIGNATURE_INVALID") {
		t.Fatalf("unsigned stage: %v %q", err, output)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("unsigned stage changed profile")
	}
	if output, err := run("--verify", "--data-dir", profile); err != nil {
		t.Fatalf("initialize %v %q", err, output)
	}
	database := filepath.Join(profile, "db", "pokegents.db")
	before, err := hashFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, pendingName), []byte(`{"state":"installing"}`), 0600); err != nil {
		t.Fatal(err)
	}
	output, err = run("--verify", "--data-dir", profile)
	if err == nil || !strings.Contains(output, "UPDATE_INTERRUPTED") {
		t.Fatalf("pending startup: %v %q", err, output)
	}
	after, err := hashFile(database)
	if err != nil || before != after {
		t.Fatal("pending startup changed database")
	}
	t.Log("built executable: unsigned stage rejected before profile/network use; interrupted update blocks startup and preserves database")
}
