//go:build windows

package processhost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/taskdomain"
)

// Workspace resolves junctions and compares volume/file IDs, including every
// ancestor. Text case folding is never the authority for directory identity.
func Workspace(path string) (*taskdomain.Project, error) {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) {
		return nil, taskdomain.Fail("UNSUPPORTED_WORKSPACE")
	}
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, taskdomain.Fail("UNSUPPORTED_WORKSPACE")
	}
	root, _ := windows.UTF16PtrFromString(volume + `\`)
	if windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return nil, taskdomain.Fail("UNSUPPORTED_WORKSPACE")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, taskdomain.Fail("INVALID_PROJECT")
	}
	identity, final, err := directoryIdentity(resolved)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(final, `\\?\UNC\`) || strings.HasPrefix(final, `\\`) && !strings.HasPrefix(final, `\\?\`) {
		return nil, taskdomain.Fail("UNSUPPORTED_WORKSPACE")
	}
	final = strings.TrimPrefix(final, `\\?\`)
	ancestors := []string{}
	for p := filepath.Dir(final); ; p = filepath.Dir(p) {
		id, _, e := directoryIdentity(p)
		if e != nil {
			return nil, e
		}
		ancestors = append(ancestors, id)
		if filepath.Dir(p) == p {
			break
		}
	}
	return &taskdomain.Project{ID: taskdomain.ID(), Name: filepath.Base(final), DisplayPath: path, ResolvedPath: final, Identity: identity, Ancestors: ancestors}, nil
}
func directoryIdentity(path string) (string, string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", "", taskdomain.Fail("INVALID_PROJECT")
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", "", err
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(h)
	var data windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &data); err != nil {
		return "", "", err
	}
	buf := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || n >= uint32(len(buf)) {
		return "", "", taskdomain.Fail("WORKSPACE_IDENTITY_UNKNOWN")
	}
	return fmt.Sprintf("%08x:%08x%08x", data.VolumeSerialNumber, data.FileIndexHigh, data.FileIndexLow), windows.UTF16ToString(buf[:n]), nil
}
