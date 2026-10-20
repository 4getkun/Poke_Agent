//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package server

import "syscall"

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
