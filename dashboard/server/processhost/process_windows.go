//go:build windows

// Package processhost owns runtime processes and all inherited pipe handles.
package processhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const jobListAttribute = 0x0002000d // PROC_THREAD_ATTRIBUTE_JOB_LIST, Windows SDK.

type Process struct {
	Stdin, Stdout, Stderr *os.File
	PID                   uint32
	job                   windows.Handle
	done                  chan struct{}
	once                  sync.Once
	err                   error
	closeErr              error
}

// Environment deliberately excludes inherited credentials and app tokens.
func Environment() []string {
	var out []string
	for _, key := range []string{"SystemRoot", "SystemDrive", "WINDIR", "COMSPEC", "PATH", "PATHEXT", "TEMP", "TMP", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "APPDATA", "LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMDATA"} {
		if value, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// Start associates the process with its non-inheritable Job during creation.
// There is no running or suspended process outside the Job to recover later.
func Start(executable string, args []string, cwd string, env []string) (_ *Process, err error) {
	return start(executable, args, cwd, env, "")
}

func StartOwned(executable string, args []string, cwd string, env []string, owner Ownership) (*Process, error) {
	created, err := creationTime(windows.CurrentProcess())
	var session uint32
	sessionErr := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session)
	if err != nil || sessionErr != nil || owner.Session != session || owner.Version != 1 || owner.PID != uint32(os.Getpid()) || owner.Created != created || !validJobName(owner.JobName) {
		return nil, errors.New("INVALID_RUNTIME_OWNER")
	}
	return start(executable, args, cwd, env, owner.JobName)
}

func start(executable string, args []string, cwd string, env []string, jobName string) (_ *Process, err error) {
	if !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Ext(executable), ".exe") {
		return nil, errors.New("RUNTIME_NOT_FOUND: absolute native exe required")
	}
	if !filepath.IsAbs(cwd) {
		return nil, errors.New("absolute working directory required")
	}
	app, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, err
	}
	dir, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return nil, err
	}
	argv := append([]string{executable}, args...)
	for i := range argv {
		if strings.ContainsRune(argv[i], 0) {
			return nil, errors.New("NUL in argument")
		}
		argv[i] = windows.EscapeArg(argv[i])
	}
	command, err := windows.UTF16PtrFromString(strings.Join(argv, " "))
	if err != nil {
		return nil, err
	}
	job, err := newJob(jobName)
	if err != nil {
		return nil, err
	}
	var files []*os.File
	defer func() {
		if err != nil {
			windows.CloseHandle(job)
			for _, f := range files {
				f.Close()
			}
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	files = append(files, stdinR, stdinW)
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	files = append(files, stdoutR, stdoutW)
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	files = append(files, stderrR, stderrW)
	handles := []windows.Handle{windows.Handle(stdinR.Fd()), windows.Handle(stdoutW.Fd()), windows.Handle(stderrW.Fd())}
	for _, h := range handles {
		if err = windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return nil, err
		}
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	jobs := []windows.Handle{job}
	if err = attrs.Update(jobListAttribute, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(job)); err != nil {
		return nil, err
	}
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		return nil, err
	}
	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = handles[0], handles[1], handles[2]
	si.ProcThreadAttributeList = attrs.List()
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	var environment []uint16
	for _, value := range env {
		if strings.ContainsRune(value, 0) {
			return nil, errors.New("NUL in environment")
		}
		environment = append(environment, utf16.Encode([]rune(value))...)
		environment = append(environment, 0)
	}
	environment = append(environment, 0)
	if len(environment) == 1 {
		environment = append(environment, 0)
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(app, command, nil, nil, true, windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, &environment[0], dir, &si.StartupInfo, &pi)
	runtime.KeepAlive(jobs)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(environment)
	if err != nil {
		return nil, err
	}
	windows.CloseHandle(pi.Thread)
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	p := &Process{Stdin: stdinW, Stdout: stdoutR, Stderr: stderrR, PID: pi.ProcessId, job: job, done: make(chan struct{})}
	go func() {
		_, waitErr := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		var code uint32
		if waitErr == nil {
			waitErr = windows.GetExitCodeProcess(pi.Process, &code)
		}
		if waitErr == nil && code != 0 {
			waitErr = fmt.Errorf("runtime exit code %d", code)
		}
		windows.CloseHandle(pi.Process)
		p.err = waitErr
		close(p.done)
	}()
	return p, nil
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close is idempotent. Never uses persisted PIDs or global process-name kills.
func (p *Process) Close(ctx context.Context) error {
	p.once.Do(func() {
		p.Stdin.Close()
		p.closeErr = windows.TerminateJobObject(p.job, 1)
		if err := p.Wait(ctx); errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			p.closeErr = err
		}
		for p.closeErr == nil {
			var accounting struct {
				TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
				TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
			}
			if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
				p.closeErr = err
				break
			}
			if accounting.ActiveProcesses == 0 {
				break
			}
			select {
			case <-ctx.Done():
				p.closeErr = ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := windows.CloseHandle(p.job); p.closeErr == nil {
			p.closeErr = err
		}
		p.Stdout.Close()
		p.Stderr.Close()
	})
	return p.closeErr
}
