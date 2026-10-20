//go:build windows

package processhost

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Ownership is persisted before a runtime can start. Created is a string so a
// JSON client cannot round the Windows 64-bit FILETIME value.
type Ownership struct {
	Version int    `json:"version"`
	PID     uint32 `json:"pid"`
	Created string `json:"created"`
	Session uint32 `json:"session"`
	JobName string `json:"job_name"`
}

const jobPrefix = `Local\Pokegents.Job.`

var createNamedJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("CreateJobObjectW")
var openNamedJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenJobObjectW")

func creationTime(h windows.Handle) (string, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", err
	}
	return fmt.Sprint(uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)), nil
}

func NewOwnership() (Ownership, error) {
	owner := Ownership{Version: 1, PID: uint32(os.Getpid())}
	var err error
	owner.Created, err = creationTime(windows.CurrentProcess())
	if err != nil {
		return Ownership{}, err
	}
	if err = windows.ProcessIdToSessionId(owner.PID, &owner.Session); err != nil {
		return Ownership{}, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return Ownership{}, err
	}
	owner.JobName = jobPrefix + hex.EncodeToString(nonce[:])
	return owner, nil
}

func validJobName(name string) bool {
	nonce, ok := strings.CutPrefix(name, jobPrefix)
	if !ok || len(nonce) != 32 || strings.ToLower(nonce) != nonce {
		return false
	}
	_, err := hex.DecodeString(nonce)
	return err == nil
}

func ownerSecurity() (*windows.SECURITY_DESCRIPTOR, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	// READ_CONTROL plus ASSIGN/SET_ATTRIBUTES/QUERY/TERMINATE. Specific rights
	// avoid generic-right expansion when comparing the queried descriptor.
	return windows.SecurityDescriptorFromString("D:P(A;;0x2000f;;;" + user.User.Sid.String() + ")")
}

func newJob(name string) (windows.Handle, error) {
	if name == "" {
		return windows.CreateJobObject(nil, nil)
	}
	if !validJobName(name) {
		return 0, errors.New("INVALID_JOB_IDENTITY")
	}
	sd, err := ownerSecurity()
	if err != nil {
		return 0, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	ptr, _ := windows.UTF16PtrFromString(name)
	h, _, callErr := createNamedJob.Call(uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(ptr)))
	if h == 0 {
		return 0, callErr
	}
	if errors.Is(callErr, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(windows.Handle(h))
		return 0, errors.New("JOB_IDENTITY_COLLISION")
	}
	return windows.Handle(h), nil
}

// VerifyReleased is read-only. It never terminates a process found by PID. A
// reused PID is distinguished by creation time; unknown/inaccessible ownership
// and other logon sessions remain blocked. Opening the Job only after its owner
// has exited cannot keep that owner's last Job handle alive during a crash.
func VerifyReleased(owner Ownership) error {
	oldCreated, parseErr := strconv.ParseUint(owner.Created, 10, 64)
	if owner.Version != 1 || owner.PID == 0 || parseErr != nil || oldCreated == 0 || fmt.Sprint(oldCreated) != owner.Created || !validJobName(owner.JobName) {
		return errors.New("OWNERSHIP_UNVERIFIED")
	}
	var session uint32
	if windows.ProcessIdToSessionId(uint32(os.Getpid()), &session) != nil || session != owner.Session {
		return errors.New("OWNERSHIP_SESSION_CHANGED")
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, owner.PID)
	if err == nil {
		defer windows.CloseHandle(process)
		created, err := creationTime(process)
		if err != nil {
			return errors.New("OWNERSHIP_UNVERIFIED")
		}
		if created == owner.Created {
			status, err := windows.WaitForSingleObject(process, 0)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				return errors.New("OWNERSHIP_STILL_ACTIVE")
			}
		} else if observed, err := strconv.ParseUint(created, 10, 64); err != nil || observed <= oldCreated {
			return errors.New("OWNERSHIP_UNVERIFIED")
		}
	} else if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return errors.New("OWNERSHIP_UNVERIFIED")
	}
	ptr, _ := windows.UTF16PtrFromString(owner.JobName)
	// QUERY | READ_CONTROL, non-inheritable; no terminate/assign access.
	raw, _, callErr := openNamedJob.Call(0x0004|windows.READ_CONTROL, 0, uintptr(unsafe.Pointer(ptr)))
	if raw == 0 {
		if errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return errors.New("OWNERSHIP_UNVERIFIED")
	}
	job := windows.Handle(raw)
	defer windows.CloseHandle(job)
	expected, err := ownerSecurity()
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(job, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd.String() != expected.String() {
		return errors.New("OWNERSHIP_ACL_MISMATCH")
	}
	var accounting struct {
		TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
		TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
	}
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil || accounting.ActiveProcesses != 0 {
		return errors.New("OWNERSHIP_STILL_ACTIVE")
	}
	return nil
}
