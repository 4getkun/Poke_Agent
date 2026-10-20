//go:build windows

package server

import "golang.org/x/sys/windows"

func isProcessAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > uint64(^uint32(0)) {
		return false
	}

	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	result, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && result == uint32(windows.WAIT_TIMEOUT)
}
