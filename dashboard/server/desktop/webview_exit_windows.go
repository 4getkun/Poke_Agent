//go:build windows

package desktop

import (
	"errors"
	"runtime"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

// ABI verified against Microsoft's NuGet WebView2 SDK 1.0.3537.50 WebView2.h:
// Environment5 inherits slots 0..11; BrowserProcessExited add/remove are 12/13.
// Unlike observing the browser PID alone, this event guarantees the associated
// renderer/helper processes and user data folder resources have been released.
type webviewExit struct {
	environment uintptr
	handler     *comHandler
	token       int64
	exited      bool
}

func watchWebViewExit(environment uintptr, browserPID uint32) (*webviewExit, error) {
	iid := windows.GUID{Data1: 0x319e423d, Data2: 0xe0d7, Data3: 0x4b8d, Data4: [8]byte{0x92, 0x54, 0xae, 0x94, 0x75, 0xde, 0x9b, 0x17}}
	w := &webviewExit{}
	if browserPID == 0 || int32(comCall(environment, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&w.environment)))) < 0 || w.environment == 0 {
		return nil, errors.New("WEBVIEW_LIFECYCLE_UNAVAILABLE")
	}
	handlerIID := windows.GUID{Data1: 0xfa504257, Data2: 0xa216, Data3: 0x4911, Data4: [8]byte{0xa8, 0x60, 0xfe, 0x88, 0x25, 0x71, 0x28, 0x61}}
	w.handler = newHandlerFor(func(_ uintptr, args uintptr) uintptr {
		var pid uint32
		if int32(comCall(args, 4, uintptr(unsafe.Pointer(&pid)))) >= 0 && pid == browserPID {
			w.exited = true
		}
		return 0
	}, handlerIID)
	if int32(comCall(w.environment, 12, uintptr(unsafe.Pointer(w.handler)), uintptr(unsafe.Pointer(&w.token)))) < 0 {
		comCall(w.environment, 2)
		return nil, errors.New("WEBVIEW_LIFECYCLE_UNAVAILABLE")
	}
	return w, nil
}

func (w *webviewExit) close() {
	comCall(w.environment, 13, uintptr(w.token))
	comCall(w.environment, 2)
	runtime.KeepAlive(w.handler)
}

func (w *webviewExit) wait() error {
	deadline := time.Now().Add(10 * time.Second)
	var msg win.MSG
	for !w.exited && time.Now().Before(deadline) {
		for win.PeekMessage(&msg, 0, 0, 0, win.PM_REMOVE) {
			if msg.Message != win.WM_QUIT {
				win.TranslateMessage(&msg)
				win.DispatchMessage(&msg)
			}
		}
		if !w.exited {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !w.exited {
		return errors.New("WEBVIEW_SHUTDOWN_TIMEOUT")
	}
	return nil
}
