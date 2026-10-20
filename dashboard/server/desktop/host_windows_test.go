//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProfilePrivateACLAndExistingDirectoryProtection(t *testing.T) {
	base := t.TempDir()
	profile := filepath.Join(base, "profile")
	if err := PrepareProfile(profile); err != nil {
		t.Fatal(err)
	}
	if err := PrepareProfile(profile); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(base, "user-files")
	if err := os.Mkdir(existing, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareProfile(existing); err == nil {
		t.Fatal("adopted unrelated directory")
	}
	b, err := os.ReadFile(filepath.Join(existing, "keep.txt"))
	if err != nil || string(b) != "keep" {
		t.Fatal("changed user file")
	}
}

func TestOriginBoundary(t *testing.T) {
	h := New(Options{Origin: "http://127.0.0.1:7835"})
	for _, uri := range []string{"http://127.0.0.1:7835/", "http://127.0.0.1:7835/api/capabilities"} {
		if !h.local(uri) {
			t.Fatal(uri)
		}
	}
	for _, uri := range []string{"http://127.0.0.1:78350/", "http://evil.test/", "http://127.0.0.1:7835@evil.test/", "http://user:secret@127.0.0.1:7835/", "file:///C:/test", "javascript:alert(1)", "data:text/html,test", "http://127.0.0.1:7835/\n"} {
		if h.local(uri) {
			t.Fatal(uri)
		}
	}
}
func TestTwentyConcurrentProfileLocksHaveOneOwner(t *testing.T) {
	profile := t.TempDir()
	var owners atomic.Int32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var primary *Instance
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			instance, owner, err := LockProfile(profile)
			if err != nil {
				t.Error(err)
				return
			}
			if owner {
				owners.Add(1)
				mu.Lock()
				primary = instance
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if primary != nil {
		defer primary.Close()
	}
	if owners.Load() != 1 {
		t.Fatalf("owners=%d", owners.Load())
	}
}
