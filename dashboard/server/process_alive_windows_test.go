//go:build windows

package server

import (
	"os"
	"testing"
)

func TestIsProcessAlive(t *testing.T) {
	if pid := os.Getpid(); !isProcessAlive(pid) {
		t.Errorf("isProcessAlive(%d) = false for current process", pid)
	}
	if isProcessAlive(1 << 30) {
		t.Errorf("isProcessAlive(%d) = true for nonexistent process", 1<<30)
	}
}
