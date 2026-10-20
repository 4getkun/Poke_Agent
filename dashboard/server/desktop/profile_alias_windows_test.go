//go:build windows

package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareProfileRejectsJunctionAliasesWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "owned")
	if err := PrepareProfile(owned); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	// Directory junction creation needs no developer-mode symlink privilege.
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/d", "/c", "mklink", "/J", alias, owned)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create isolated junction: %v %s", err, output)
	}
	for _, path := range []string{alias, filepath.Join(alias, "nested")} {
		if err := PrepareProfile(path); err == nil || !strings.Contains(err.Error(), "PROFILE_REPARSE_POINT") {
			t.Fatalf("alias accepted: %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(owned, "nested")); !os.IsNotExist(err) {
		t.Fatal("rejected alias created a directory in its target")
	}
	if err := PrepareProfile(owned); err != nil {
		t.Fatalf("direct owned profile changed: %v", err)
	}
}
