//go:build windows

package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// PrepareProfile changes ACLs only on a newly created application directory.
// An arbitrary pre-existing directory must never have its ACL replaced.
func PrepareProfile(path string) error {
	path = filepath.Clean(path)
	if err := CheckDirectLocalPath(path); err != nil {
		return err
	}
	marker := filepath.Join(path, ".pokegents-profile")
	if _, err := os.Stat(path); err == nil {
		if err := rejectProfileReparsePoints(marker); err != nil {
			return err
		}
		b, e := os.ReadFile(marker)
		if e != nil || string(b) != "pokegents-profile-v1\n" {
			return errors.New("PROFILE_NOT_OWNED: select a new application data directory")
		}
		return checkProfileACL(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	if err := rejectProfileReparsePoints(path); err != nil {
		return err
	}
	_, sid, err := userSecurity()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	if err = os.WriteFile(marker, []byte("pokegents-profile-v1\n"), 0600); err != nil {
		return err
	}
	return checkProfileACL(path)
}

// CheckDirectLocalPath rejects UNC/device paths and existing reparse aliases.
func CheckDirectLocalPath(path string) error {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || filepath.Dir(path) == path {
		return errors.New("INVALID_PROFILE")
	}
	return rejectProfileReparsePoints(path)
}

func profileLockIdentity(path string) (string, error) {
	path = filepath.Clean(path)
	if err := CheckDirectLocalPath(path); err != nil {
		return "", err
	}
	var suffix []string
	for {
		ptr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return "", err
		}
		var expanded [32768]uint16
		n, err := windows.GetLongPathName(ptr, &expanded[0], uint32(len(expanded)))
		if err == nil && n > 0 && n < uint32(len(expanded)) {
			result := windows.UTF16ToString(expanded[:n])
			for i := len(suffix) - 1; i >= 0; i-- {
				result = filepath.Join(result, suffix[i])
			}
			return strings.ToLower(filepath.Clean(result)), nil
		}
		if err == nil || (!errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND)) || filepath.Dir(path) == path {
			return "", errors.New("PROFILE_IDENTITY_UNAVAILABLE")
		}
		suffix = append(suffix, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

// Reject aliases before either changing ACLs or deriving profile ownership.
// Otherwise two spellings of one profile can acquire different instance locks.
func rejectProfileReparsePoints(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(name)
		if err != nil && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return err
		}
		if err == nil && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("PROFILE_REPARSE_POINT: use a direct local application data path")
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
func checkProfileACL(path string) error {
	_, sid, err := userSecurity()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	expected, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	actualText, expectedText := sd.String(), expected.String()
	actualStart, expectedStart := strings.Index(actualText, "("), strings.Index(expectedText, "(")
	// Windows may add the auto-inherited (AI) control bit while applying the
	// protected DACL. The complete ACE list must still match the owner-only ACL.
	if actualStart < 0 || expectedStart < 0 || !strings.HasPrefix(actualText, "D:P") || actualText[actualStart:] != expectedText[expectedStart:] {
		return errors.New("PROFILE_ACL_UNSAFE")
	}
	return nil
}
