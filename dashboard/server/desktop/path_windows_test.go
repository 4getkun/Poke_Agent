//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareProfileUnicodeAndSpecialPathCanBeReopened(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "日本語 path &()[]", "profile")
	if err := PrepareProfile(profile); err != nil {
		t.Fatalf("PrepareProfile(%q): %v", profile, err)
	}
	if err := checkProfileACL(profile); err != nil {
		t.Fatalf("profile ACL at %q: %v", profile, err)
	}
	if err := PrepareProfile(profile); err != nil {
		t.Fatalf("reopen owned profile at %q: %v", profile, err)
	}
	if _, err := os.Stat(filepath.Join(profile, ".pokegents-profile")); err != nil {
		t.Fatalf("owned profile marker: %v", err)
	}
}

func TestProfileLockKeepsIdentityAcrossCreationAndCaseAliases(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "日本語 Profile &()[]")
	first, primary, err := TryLockProfile(profile)
	if err != nil || !primary {
		t.Fatalf("first profile lock: primary=%v err=%v", primary, err)
	}
	defer first.Close()
	if err := PrepareProfile(profile); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{profile, strings.ToUpper(profile), strings.ToLower(profile)} {
		instance, primary, err := TryLockProfile(spelling)
		if instance != nil {
			instance.Close()
		}
		if err != nil || primary {
			t.Fatalf("profile alias acquired a second lock: %s primary=%v err=%v", spelling, primary, err)
		}
	}
}
