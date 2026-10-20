//go:build windows

package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/sqlstore"
)

func TestVersionAndManifestValidation(t *testing.T) {
	for _, tc := range []struct {
		value string
		ok    bool
	}{{"1.2.3", true}, {"01.2.3", false}, {"1.2.3-dev", false}, {"65536.0.0", false}} {
		_, err := version(tc.value)
		if (err == nil) != tc.ok {
			t.Errorf("version(%q) err=%v", tc.value, err)
		}
	}
	if !newer("1.2.4", "1.2.3") || newer("1.2.3", "1.2.3") || newer("1.2.2", "1.2.3") {
		t.Fatal("version ordering")
	}
	base := Manifest{ExecutableSHA256: hex.EncodeToString(make([]byte, 32)), Schema: 1, Product: "Pokegents", Target: "windows/amd64", Installer: Artifact{Version: "1.2.4", URL: "https://updates.example/installer.exe", SHA256: string(make([]byte, 0)), Size: 1}, Rollback: &Artifact{Version: "1.2.3", URL: "https://updates.example/rollback.exe", SHA256: hex.EncodeToString(make([]byte, 32)), Size: 1}}
	base.Installer.SHA256 = hex.EncodeToString(make([]byte, 32))
	if err := validate(base, "1.2.3", "https://updates.example/manifest.json"); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	cases := []struct {
		name   string
		change func(*Manifest)
		source string
	}{
		{"product", func(m *Manifest) { m.Product = "Other" }, "https://updates.example/manifest.json"},
		{"target", func(m *Manifest) { m.Target = "linux/amd64" }, "https://updates.example/manifest.json"},
		{"not newer", func(m *Manifest) { m.Installer.Version = "1.2.3" }, "https://updates.example/manifest.json"},
		{"http", func(m *Manifest) { m.Installer.URL = "http://updates.example/a" }, "https://updates.example/manifest.json"},
		{"query", func(m *Manifest) { m.Installer.URL = "https://updates.example/a?x=1" }, "https://updates.example/manifest.json"},
		{"origin", func(m *Manifest) { m.Installer.URL = "https://elsewhere.example/a" }, "https://updates.example/manifest.json"},
		{"size", func(m *Manifest) { m.Installer.Size = 0 }, "https://updates.example/manifest.json"},
		{"oversize", func(m *Manifest) { m.Installer.Size = maxInstaller + 1 }, "https://updates.example/manifest.json"},
		{"rollback version", func(m *Manifest) { m.Rollback.Version = "1.2.2" }, "https://updates.example/manifest.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			tc.change(&m)
			if err := validate(m, "1.2.3", tc.source); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
}

func TestFetchTLSRedirectLimitAndStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/large":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "123456")
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("UPDATE_REDIRECT_REFUSED") }
	for _, tc := range []struct {
		path  string
		limit int64
		want  string
	}{{"/ok", 10, ""}, {"/redirect", 10, "UPDATE_DOWNLOAD_FAILED"}, {"/large", 5, "UPDATE_DOWNLOAD_LIMIT"}, {"/missing", 10, "UPDATE_DOWNLOAD_FAILED"}} {
		t.Run(tc.path, func(t *testing.T) {
			var dst strings.Builder
			err := fetch(context.Background(), client, server.URL+tc.path, tc.limit, &dst)
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Fatalf("fetch err=%v, want %s", err, tc.want)
			}
		})
	}
}

func TestCheckFileVerifiesSizeHashAndSignatureIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "candidate.exe")
	data := []byte("fixture executable")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	a := Artifact{Version: "1.2.4", Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
	pin := hex.EncodeToString(make([]byte, 32))
	good := func(context.Context, string) (signature, error) {
		return signature{Pin: pin, Product: "Pokegents", Version: "1.2.4"}, nil
	}
	if err := checkFile(context.Background(), path, a, pin, good); err != nil {
		t.Fatal(err)
	}
	a.Size++
	if err := checkFile(context.Background(), path, a, pin, good); err == nil {
		t.Fatal("size mismatch accepted")
	}
	a.Size--
	a.SHA256 = hex.EncodeToString(make([]byte, 32))
	if err := checkFile(context.Background(), path, a, pin, good); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	a.SHA256 = hex.EncodeToString(h[:])
	bad := func(context.Context, string) (signature, error) {
		return signature{Pin: pin, Product: "Other", Version: "1.2.4"}, nil
	}
	if err := checkFile(context.Background(), path, a, pin, bad); err == nil {
		t.Fatal("signature identity mismatch accepted")
	}
}

func TestCheckPendingStates(t *testing.T) {
	for _, tc := range []struct {
		state string
		valid bool
	}{{"", true}, {"installing", false}, {"rolling_back", false}, {"failed", false}, {"completed", true}, {"rolled_back", true}} {
		t.Run(tc.state, func(t *testing.T) {
			dir := t.TempDir()
			if tc.state != "" {
				if err := os.WriteFile(filepath.Join(dir, pendingName), []byte(`{"state":"`+tc.state+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckPending(dir)
			if (err == nil) != tc.valid {
				t.Fatalf("CheckPending=%v", err)
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pendingName), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if CheckPending(dir) == nil {
		t.Fatal("invalid marker accepted")
	}
}

func TestVerifySignatureRejectsUnsignedExecutable(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "artifacts", "Pokegents.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	if _, err := verifySignature(context.Background(), path); err == nil {
		t.Fatal("unsigned executable accepted")
	}
}

func TestVerifySignatureAcceptsAndDetectsTamperedSDKTool(t *testing.T) {
	// Hosted Windows runners and local SDKs can have different installed versions.
	// Still require a real SDK fixture: absence is a failure, never a skipped check.
	sdkRoot := os.Getenv("ProgramFiles(x86)")
	if sdkRoot == "" {
		t.Fatal("Windows SDK fixture requires ProgramFiles(x86)")
	}
	fixtures, err := filepath.Glob(filepath.Join(sdkRoot, "Windows Kits", "10", "bin", "10.*", "x64", "signtool.exe"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("SDK signed fixture unavailable: %v", err)
	}
	signed := fixtures[len(fixtures)-1]
	sig, err := verifySignature(context.Background(), signed)
	if err != nil || !hexHash(sig.Pin) || sig.Product == "" || sig.Version == "" {
		t.Fatalf("verify signed SDK tool: signature=%+v err=%v", sig, err)
	}
	copyPath := filepath.Join(t.TempDir(), "signtool-tampered.exe")
	contents, err := os.ReadFile(signed)
	if err != nil {
		t.Fatal(err)
	}
	contents[len(contents)/2] ^= 0xff
	if err := os.WriteFile(copyPath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifySignature(context.Background(), copyPath); err == nil {
		t.Fatal("tampered signed executable accepted")
	}
}

func TestApplySuccessAndRollbackRecovery(t *testing.T) {
	const current = "1.2.3"
	pin := hex.EncodeToString(make([]byte, 32))
	for _, scenario := range []string{"success", "health-fail", "rollback-fail", "recover", "profile-lock"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			profile := filepath.Join(root, "profile")
			if err := desktop.PrepareProfile(profile); err != nil {
				t.Fatal(err)
			}
			db, err := sqlstore.Open(filepath.Join(profile, "db", "pokegents.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			install := filepath.Join(root, "install")
			if err := os.Mkdir(install, 0700); err != nil {
				t.Fatal(err)
			}
			installed := filepath.Join(install, "Pokegents.exe")
			old := []byte("old signed binary")
			next := []byte("new signed binary")
			if err := os.WriteFile(installed, old, 0600); err != nil {
				t.Fatal(err)
			}
			oh := sha256.Sum256(old)
			folder := filepath.Join(profile, "updates", "stage-fixture")
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			for name := range map[string]bool{"installer.exe": true, "rollback.exe": true} {
				if err := os.WriteFile(filepath.Join(folder, name), []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			installerBytes, _ := os.ReadFile(filepath.Join(folder, "installer.exe"))
			rollbackBytes, _ := os.ReadFile(filepath.Join(folder, "rollback.exe"))
			ih := sha256.Sum256(installerBytes)
			rh := sha256.Sum256(rollbackBytes)
			nextHash := sha256.Sum256(next)
			m := Manifest{ExecutableSHA256: hex.EncodeToString(nextHash[:]), Schema: 1, Product: "Pokegents", Target: "windows/amd64", Installer: Artifact{Version: "1.2.4", URL: "https://updates.example/i.exe", SHA256: hex.EncodeToString(ih[:]), Size: int64(len(installerBytes))}, Rollback: &Artifact{Version: current, URL: "https://updates.example/r.exe", SHA256: hex.EncodeToString(rh[:]), Size: int64(len(rollbackBytes))}}
			st := Stage{Manifest: m, Pin: pin, CurrentVersion: current, CurrentHash: hex.EncodeToString(oh[:])}
			if err := writeNew(filepath.Join(folder, "stage.json"), st); err != nil {
				t.Fatal(err)
			}
			if scenario == "recover" {
				if err := os.WriteFile(filepath.Join(folder, "pre-update.db"), []byte("database backup fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			verify := func(_ context.Context, path string) (signature, error) {
				b, e := os.ReadFile(path)
				if e != nil {
					return signature{}, e
				}
				if string(b) == "installer.exe" {
					return signature{pin, "Pokegents", "1.2.4"}, nil
				}
				if string(b) == "rollback.exe" || string(b) == string(old) {
					return signature{pin, "Pokegents", current}, nil
				}
				if string(b) == string(next) {
					return signature{pin, "Pokegents", "1.2.4"}, nil
				}
				return signature{}, errors.New("unexpected executable")
			}
			run := func(_ context.Context, path string, _ []string) error {
				if filepath.Base(path) == "installer.exe" {
					return os.WriteFile(installed, next, 0600)
				}
				if scenario == "rollback-fail" {
					return errors.New("rollback fixture failure")
				}
				return os.WriteFile(installed, old, 0600)
			}
			health := func(context.Context, string, string) error {
				if scenario == "health-fail" || scenario == "rollback-fail" {
					return errors.New("health fixture failure")
				}
				return nil
			}
			e := engine{verify: verify, run: run, health: health}
			var held *desktop.Instance
			if scenario == "profile-lock" {
				var primary bool
				held, primary, err = desktop.TryLockProfile(profile)
				if err != nil || !primary {
					t.Fatalf("acquire fixture profile lock: primary=%v err=%v", primary, err)
				}
				defer held.Close()
			}
			if scenario == "recover" {
				_ = journal(profile, Journal{State: "failed", Stage: folder, InstallDir: install, Version: "1.2.4", Previous: current})
				run = func(_ context.Context, path string, _ []string) error { return os.WriteFile(installed, old, 0600) }
				e.run = run
			}
			err = e.apply(context.Background(), profile, folder, install, current, pin, scenario == "recover")
			switch scenario {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "health-fail":
				if err == nil || !strings.Contains(err.Error(), "UPDATE_ROLLED_BACK") {
					t.Fatalf("err=%v", err)
				}
			case "rollback-fail":
				if err == nil || !strings.Contains(err.Error(), "UPDATE_ROLLBACK_FAILED") {
					t.Fatalf("err=%v", err)
				}
				if CheckPending(profile) == nil {
					t.Fatal("failed journal did not gate")
				}
			case "recover":
				if err == nil || !strings.Contains(err.Error(), "UPDATE_ROLLED_BACK") {
					t.Fatalf("err=%v", err)
				}
			case "profile-lock":
				if err == nil || !strings.Contains(err.Error(), "PROFILE_IN_USE") {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}
