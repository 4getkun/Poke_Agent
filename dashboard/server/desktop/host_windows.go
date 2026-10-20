//go:build windows

// Package desktop hosts only the trusted loopback UI in WebView2.
package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const wmDispatch = win.WM_APP + 1
const wmActivate = win.WM_APP + 2
const wmQuit = win.WM_APP + 3
const wmTray = win.WM_APP + 4

type Options struct {
	Origin, Token, DataPath string
	CanClose                func() bool
	OnReady                 func()
	OnError                 func(error)
	OnSessionEnd            func() bool
	Hidden                  bool // Automated native tests; normal application windows remain visible.
}
type Host struct {
	hwnd                win.HWND
	chromium            *edge.Chromium
	options             Options
	dispatch            chan func()
	handlers            []*comHandler
	mu                  sync.Mutex
	closed              bool
	tray                trayState // Owned by the window's OS thread.
	ready               bool
	pendingNotification string
}
type Instance struct {
	mutex, event windows.Handle
	name         string
}

// UserData uses the OS known-folder API; there is no guessed HOME fallback.
func UserData() (string, error) {
	p, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "Pokegents"), nil
}
func userSecurity() (*windows.SecurityAttributes, string, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, "", err
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid + ")")
	if err != nil {
		return nil, "", err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, sid, nil
}
func LockProfile(profile string) (*Instance, bool, error) {
	return lockProfile(profile, true)
}

// TryLockProfile is for offline maintenance; it never raises an existing window.
func TryLockProfile(profile string) (*Instance, bool, error) {
	return lockProfile(profile, false)
}

func lockProfile(profile string, activate bool) (*Instance, bool, error) {
	identity, err := profileLockIdentity(profile)
	if err != nil {
		return nil, false, err
	}
	sa, sid, err := userSecurity()
	if err != nil {
		return nil, false, err
	}
	hash := sha256.Sum256([]byte(sid + "|" + identity))
	name := "Local\\Pokegents-" + hex.EncodeToString(hash[:12])
	n, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateMutex(sa, false, n)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, false, err
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		if !activate {
			return nil, false, nil
		}
		en, _ := windows.UTF16PtrFromString(name + "-show")
		for i := 0; i < 20; i++ {
			event, e := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, en)
			if e == nil {
				windows.SetEvent(event)
				windows.CloseHandle(event)
				return nil, false, nil
			}
			time.Sleep(25 * time.Millisecond)
		}
		return nil, false, errors.New("primary instance is initializing")
	}
	en, _ := windows.UTF16PtrFromString(name + "-show")
	event, err := windows.CreateEvent(sa, 0, 0, en)
	if err != nil {
		windows.CloseHandle(h)
		return nil, false, err
	}
	return &Instance{mutex: h, event: event, name: name}, true, nil
}
func (i *Instance) Close() { windows.CloseHandle(i.event); windows.CloseHandle(i.mutex) }

func New(options Options) *Host { return &Host{options: options, dispatch: make(chan func(), 32)} }
func (h *Host) Dispatch(fn func()) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errors.New("window closed")
	}
	select {
	case h.dispatch <- fn:
		win.PostMessage(h.hwnd, wmDispatch, 0, 0)
		return nil
	default:
		return errors.New("UI queue full")
	}
}
func (h *Host) Close() { h.Dispatch(func() { win.PostMessage(h.hwnd, wmQuit, 0, 0) }) }
func (h *Host) Run(instance *Instance) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(1) {
		return err
	}
	defer windows.CoUninitialize()
	if err := setApplicationIdentity(); err != nil {
		return err
	}
	if _, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString(""); err != nil {
		win.MessageBox(0, windows.StringToUTF16Ptr("WebView2 Runtime が見つかりません。Microsoft の公式サイトからインストールしてください。"), windows.StringToUTF16Ptr("Pokegents"), win.MB_OK|win.MB_ICONERROR)
		return err
	}
	class := windows.StringToUTF16Ptr("PokegentsWindowsHost")
	callback := syscall.NewCallback(func(hwnd win.HWND, msg uint32, wparam, lparam uintptr) uintptr {
		if msg == h.tray.taskbarCreated && msg != 0 {
			h.tray.recreate(h)
			return 0
		}
		switch msg {
		case win.WM_SIZE:
			if h.chromium != nil && h.chromium.GetController() != nil {
				h.chromium.Resize()
			}
			return 0
		case wmDispatch:
			select {
			case fn := <-h.dispatch:
				fn()
			default:
			}
			return 0
		case wmActivate:
			if h.options.Hidden {
				return 0
			}
			win.ShowWindow(hwnd, win.SW_RESTORE)
			if !win.SetForegroundWindow(hwnd) {
				fi := struct {
					Size                  uint32
					Window                win.HWND
					Flags, Count, Timeout uint32
				}{Window: hwnd, Flags: 2, Count: 3}
				fi.Size = uint32(unsafe.Sizeof(fi))
				windows.NewLazySystemDLL("user32.dll").NewProc("FlashWindowEx").Call(uintptr(unsafe.Pointer(&fi)))
			}
			return 0
		case wmTray:
			h.tray.callback(h, wparam, lparam)
			return 0
		case win.WM_TIMER:
			if wparam == trayTimer {
				h.tray.expire(h)
				return 0
			}
		case win.WM_QUERYENDSESSION:
			if h.options.OnSessionEnd != nil && !h.options.OnSessionEnd() {
				return 0
			}
			return 1
		case win.WM_ENDSESSION:
			if wparam != 0 {
				win.DestroyWindow(hwnd)
			}
			return 0
		case win.WM_CLOSE, wmQuit:
			if msg == win.WM_CLOSE && h.tray.background && h.tray.resident {
				win.ShowWindow(hwnd, win.SW_HIDE)
				return 0
			}
			if h.options.CanClose != nil && !h.options.CanClose() {
				return 0
			}
			win.DestroyWindow(hwnd)
			return 0
		case win.WM_DESTROY:
			h.tray.destroy(h)
			win.PostQuitMessage(0)
			return 0
		}
		return win.DefWindowProc(hwnd, msg, wparam, lparam)
	})
	wc := win.WNDCLASSEX{CbSize: uint32(unsafe.Sizeof(win.WNDCLASSEX{})), LpfnWndProc: callback, HInstance: win.GetModuleHandle(nil), HCursor: win.LoadCursor(0, win.MAKEINTRESOURCE(win.IDC_ARROW)), LpszClassName: class}
	if win.RegisterClassEx(&wc) == 0 {
		return errors.New("window registration failed")
	}
	// lxn/win's wrapper omits the required HINSTANCE argument.
	defer func() {
		windows.NewLazySystemDLL("user32.dll").NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(class)), uintptr(wc.HInstance))
	}()
	h.hwnd = win.CreateWindowEx(0, class, windows.StringToUTF16Ptr("Pokegents for Windows"), win.WS_OVERLAPPEDWINDOW, win.CW_USEDEFAULT, win.CW_USEDEFAULT, 1200, 800, 0, 0, wc.HInstance, nil)
	if h.hwnd == 0 {
		return errors.New("window creation failed")
	}
	h.tray.initialize()
	c := edge.NewChromium()
	h.chromium = c
	c.DataPath = h.options.DataPath
	c.SetGlobalPermission(edge.CoreWebView2PermissionStateDeny)
	c.SetErrorCallback(func(err error) {
		if h.options.OnError != nil {
			h.options.OnError(err)
		}
		win.PostMessage(h.hwnd, wmQuit, 0, 0)
	})
	c.MessageCallback = func(message string, sender *edge.ICoreWebView2, args *edge.ICoreWebView2WebMessageReceivedEventArgs) {
		if len(message) > 1024 {
			return
		}
		source, err := args.GetSource()
		if err != nil || source != h.options.Origin+"/" {
			return
		}
		top, err := sender.GetSource()
		if err != nil || top != source {
			return
		}
		var request struct {
			Type    string `json:"type"`
			Version int    `json:"version"`
		}
		if json.Unmarshal([]byte(message), &request) != nil || request.Type != "bootstrap" || request.Version != 1 {
			return
		}
		b, _ := json.Marshal(map[string]any{"type": "bootstrap", "token": h.options.Token})
		if sender.PostWebMessageAsString(string(b)) != nil {
			return
		}
		h.ready = true
		if h.pendingNotification != "" {
			h.sendNotificationActivation(h.pendingNotification)
		}
		if h.options.OnReady != nil {
			h.options.OnReady()
		}
	}
	if !c.Embed(uintptr(h.hwnd)) {
		return errors.New("WebView2 initialization failed")
	}
	view, err := c.GetController().GetCoreWebView2()
	if err != nil {
		return err
	}
	var browserPID uint32
	comCall(uintptr(unsafe.Pointer(view)), 37, uintptr(unsafe.Pointer(&browserPID)))
	exit, err := watchWebViewExit(uintptr(unsafe.Pointer(c.Environment())), browserPID)
	if err != nil {
		return err
	}
	defer exit.close()
	// SDK vtable slots are fixed by ICoreWebView2. Keep handlers alive through
	// controller shutdown; no generic host objects or eval bridge are exposed.
	navigation := newHandler(func(_ uintptr, args uintptr) uintptr {
		uri, e := comString(args, 3)
		if e != nil || !h.local(uri) {
			comCall(args, 8, 1)
		}
		return 0
	})
	frame := newHandler(func(_ uintptr, args uintptr) uintptr { comCall(args, 8, 1); return 0 })
	popup := newHandler(func(_ uintptr, args uintptr) uintptr { comCall(args, 6, 1); return 0 })
	h.handlers = []*comHandler{navigation, frame, popup}
	for _, pair := range []struct {
		slot    int
		handler *comHandler
	}{{7, navigation}, {17, frame}, {44, popup}} {
		var token int64
		if hr := comCall(uintptr(unsafe.Pointer(view)), pair.slot, uintptr(unsafe.Pointer(pair.handler)), uintptr(unsafe.Pointer(&token))); int32(hr) < 0 {
			return errors.New("WebView2 navigation boundary unavailable")
		}
	}
	c.AddWebResourceRequestedFilter("*", edge.COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL)
	c.WebResourceRequestedCallback = func(request *edge.ICoreWebView2WebResourceRequest, args *edge.ICoreWebView2WebResourceRequestedEventArgs) {
		uri, err := request.GetUri()
		if err != nil || !h.local(uri) {
			response, e := c.Environment().CreateWebResourceResponse([]byte("Blocked"), 403, "Forbidden", "Content-Type: text/plain\r\n")
			if e == nil {
				args.PutResponse(response)
			}
		}
	}
	settings, err := c.GetSettings()
	if err != nil {
		return err
	}
	settings.PutAreDevToolsEnabled(false)
	settings.PutAreDefaultContextMenusEnabled(false)
	comCall(uintptr(unsafe.Pointer(settings)), 2)
	c.Navigate(h.options.Origin + "/")
	if !h.options.Hidden {
		win.ShowWindow(h.hwnd, win.SW_SHOW)
	}
	c.Resize()
	stop := make(chan struct{})
	if instance != nil {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				status, err := windows.WaitForSingleObject(instance.event, 100)
				if err != nil {
					return
				}
				if status == windows.WAIT_OBJECT_0 {
					win.PostMessage(h.hwnd, wmActivate, 0, 0)
				}
			}
		}()
	}
	defer close(stop)
	var msg win.MSG
	for {
		status := win.GetMessage(&msg, 0, 0, 0)
		if status == 0 {
			break
		}
		if status == -1 {
			return errors.New("window message loop failed")
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	c.ShuttingDown()
	comCall(uintptr(unsafe.Pointer(c.GetController())), 24)
	// v1.0.23 Chromium holds two view references (GetCoreWebView2 + AddRef),
	// one controller reference and one environment reference. Release those
	// and the getter reference acquired above after Close has detached events.
	view.Release()
	view.Release()
	view.Release()
	if controller3 := c.GetController().GetICoreWebView2Controller3(); controller3 != nil {
		// v1.0.23's initialization queries Controller3 without releasing it.
		// Balance that pinned-library reference and the query immediately above.
		comCall(uintptr(unsafe.Pointer(controller3)), 2)
		comCall(uintptr(unsafe.Pointer(controller3)), 2)
	}
	c.GetController().Release()
	// Keep the environment/event alive until the entire WebView2 process group
	// releases its resources, rather than racing TempDir/profile cleanup.
	exitErr := exit.wait()
	comCall(uintptr(unsafe.Pointer(c.Environment())), 2)
	runtime.KeepAlive(h.handlers)
	return exitErr
}
func (h *Host) local(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	return u.Scheme+"://"+u.Host == h.options.Origin && !strings.ContainsAny(raw, "\r\n")
}
func (h *Host) PickDirectory() (string, error) {
	var selected string
	done := make(chan struct{})
	err := h.Dispatch(func() {
		defer close(done)
		title := windows.StringToUTF16Ptr("作業フォルダーを選択")
		bi := win.BROWSEINFO{HwndOwner: h.hwnd, LpszTitle: title, UlFlags: 0x0001 | 0x0040}
		pidl := win.SHBrowseForFolder(&bi)
		if pidl == 0 {
			return
		}
		defer windows.CoTaskMemFree(unsafe.Pointer(pidl))
		buf := make([]uint16, 32768)
		if win.SHGetPathFromIDList(pidl, &buf[0]) {
			selected = windows.UTF16ToString(buf)
		}
	})
	if err != nil {
		return "", err
	}
	select {
	case <-done:
		return selected, nil
	case <-time.After(30 * time.Second):
		return "", errors.New("dialog timeout")
	}
}

func (h *Host) OpenURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || strings.ContainsAny(raw, "\x00\r\n\t") {
		return errors.New("INVALID_URL")
	}
	done := make(chan error, 1)
	if err = h.Dispatch(func() {
		result := win.ShellExecute(h.hwnd, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(u.String()), nil, nil, win.SW_SHOWNORMAL)
		if !result {
			done <- errors.New("external URL open failed")
		} else {
			done <- nil
		}
	}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return errors.New("external URL open timeout")
	}
}

type comHandler struct {
	vtbl   *[4]uintptr
	invoke func(uintptr, uintptr) uintptr
}

func newHandler(fn func(uintptr, uintptr) uintptr) *comHandler {
	return newHandlerFor(fn, windows.GUID{})
}
func newHandlerFor(fn func(uintptr, uintptr) uintptr, supportedIID windows.GUID) *comHandler {
	h := &comHandler{invoke: fn}
	h.vtbl = &[4]uintptr{
		syscall.NewCallback(func(this, riid, out uintptr) uintptr {
			if out == 0 {
				return 0x80004003
			}
			*(*uintptr)(unsafe.Pointer(out)) = 0
			iid := *(*windows.GUID)(unsafe.Pointer(riid))
			if iid != (windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}) && (supportedIID == (windows.GUID{}) || iid != supportedIID) {
				return 0x80004002
			}
			*(*uintptr)(unsafe.Pointer(out)) = this
			return 0
		}),
		syscall.NewCallback(func(uintptr) uintptr { return 1 }), syscall.NewCallback(func(uintptr) uintptr { return 1 }),
		syscall.NewCallback(func(this, sender, args uintptr) uintptr {
			return (*comHandler)(unsafe.Pointer(this)).invoke(sender, args)
		})}
	return h
}
func comCall(object uintptr, slot int, args ...uintptr) uintptr {
	table := *(*uintptr)(unsafe.Pointer(object))
	method := *(*uintptr)(unsafe.Pointer(table + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	params := append([]uintptr{object}, args...)
	result, _, _ := syscall.SyscallN(method, params...)
	return result
}
func comString(object uintptr, slot int) (string, error) {
	var p *uint16
	hr := comCall(object, slot, uintptr(unsafe.Pointer(&p)))
	if int32(hr) < 0 {
		return "", errors.New("COM string unavailable")
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(p))
	return windows.UTF16PtrToString(p), nil
}
