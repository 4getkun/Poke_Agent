//go:build windows

package desktop

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const AppUserModelID = "4getkun.Pokegents"
const trayTimer = 0x5047

type nativeNotice struct{ id, title, body string }
type trayState struct {
	background, notifications, resident bool
	taskbarCreated                      uint32
	nextID, activeID                    uint32
	active                              nativeNotice
	queue                               []nativeNotice
	since                               time.Time
	revision                            int64
}

func setApplicationIdentity() error {
	id, _ := windows.UTF16PtrFromString(AppUserModelID)
	hr, _, _ := windows.NewLazySystemDLL("shell32.dll").NewProc("SetCurrentProcessExplicitAppUserModelID").Call(uintptr(unsafe.Pointer(id)))
	if int32(hr) < 0 {
		return errors.New("APPLICATION_IDENTITY_UNAVAILABLE")
	}
	return nil
}

func (t *trayState) initialize() {
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	message, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("RegisterWindowMessageW").Call(uintptr(unsafe.Pointer(name)))
	t.taskbarCreated = uint32(message)
	t.nextID = 1
}

func noticeData(h *Host, id uint32) win.NOTIFYICONDATA {
	n := win.NOTIFYICONDATA{HWnd: h.hwnd, UID: id, UCallbackMessage: wmTray}
	n.CbSize = uint32(unsafe.Sizeof(n))
	return n
}

func icon(h *Host, id uint32, add bool) bool {
	n := noticeData(h, id)
	if !add {
		return win.Shell_NotifyIcon(win.NIM_DELETE, &n)
	}
	n.UFlags = win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP
	n.HIcon = win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION))
	copy(n.SzTip[:], boundedUTF16("Pokegents", len(n.SzTip)))
	if !win.Shell_NotifyIcon(win.NIM_ADD, &n) {
		return false
	}
	n.UVersion = 4
	if !win.Shell_NotifyIcon(win.NIM_SETVERSION, &n) {
		win.Shell_NotifyIcon(win.NIM_DELETE, &n)
		return false
	}
	return true
}

// ConfigureDesktop is marshaled to the WebView/window OS thread. Background
// hiding is enabled only after a usable restore/quit tray icon exists.
func (h *Host) ConfigureDesktop(background, notifications bool, revision int64) error {
	return h.desktopCall(func() error {
		t := &h.tray
		if revision < t.revision {
			return nil
		}
		t.revision = revision
		if !notifications && t.notifications {
			t.clearNotices(h)
		}
		t.notifications = notifications
		if background && !t.resident {
			if !icon(h, 1, true) {
				return errors.New("TRAY_UNAVAILABLE")
			}
			t.resident = true
		} else if !background && t.resident {
			// Never strand a hidden window when the user disables residency.
			win.PostMessage(h.hwnd, wmActivate, 0, 0)
			icon(h, 1, false)
			t.resident = false
		}
		t.background = background
		return nil
	})
}

func (h *Host) Notify(id, title, body string) error {
	if id == "" || len(id) > 128 {
		return errors.New("INVALID_NOTIFICATION")
	}
	return h.desktopCall(func() error {
		if !h.tray.notifications {
			return nil
		}
		if len(h.tray.queue) >= 100 {
			return errors.New("NOTIFICATION_QUEUE_FULL")
		}
		h.tray.queue = append(h.tray.queue, nativeNotice{id, title, body})
		return h.tray.next(h)
	})
}

func (h *Host) desktopCall(fn func() error) error {
	done := make(chan error, 1)
	if err := h.Dispatch(func() { done <- fn() }); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return errors.New("DESKTOP_TIMEOUT")
	}
}

func (t *trayState) next(h *Host) error {
	if t.activeID != 0 || len(t.queue) == 0 || !t.notifications {
		return nil
	}
	// IDs are never reused in this process. A delayed Shell callback from an
	// expired notification therefore cannot open a different target.
	if t.nextID >= 65535 {
		t.queue = nil
		return errors.New("NOTIFICATION_ID_LIMIT")
	}
	t.nextID++
	id := t.nextID
	item := t.queue[0]
	t.queue = t.queue[1:]
	if !icon(h, id, true) {
		return errors.New("NOTIFICATION_UNAVAILABLE")
	}
	n := noticeData(h, id)
	n.UFlags = win.NIF_INFO | 0x40              // NIF_REALTIME: stale transient banners may be discarded.
	n.DwInfoFlags = win.NIIF_INFO | 0x10 | 0x80 // NOSOUND | RESPECT_QUIET_TIME.
	copy(n.SzInfoTitle[:], boundedUTF16(item.title, len(n.SzInfoTitle)))
	copy(n.SzInfo[:], boundedUTF16(item.body, len(n.SzInfo)))
	if !win.Shell_NotifyIcon(win.NIM_MODIFY, &n) {
		icon(h, id, false)
		return errors.New("NOTIFICATION_UNAVAILABLE")
	}
	t.activeID, t.active, t.since = id, item, time.Now()
	win.SetTimer(h.hwnd, trayTimer, 1000, 0)
	return nil
}

func (t *trayState) finish(h *Host) {
	if t.activeID != 0 {
		icon(h, t.activeID, false)
	}
	t.activeID = 0
	t.active = nativeNotice{}
	win.KillTimer(h.hwnd, trayTimer)
	_ = t.next(h)
}
func (t *trayState) expire(h *Host) {
	if t.activeID != 0 && time.Since(t.since) > 60*time.Second {
		t.finish(h)
	}
}
func (t *trayState) clearNotices(h *Host) { t.queue = nil; t.finish(h) }
func (t *trayState) destroy(h *Host) {
	t.clearNotices(h)
	if t.resident {
		icon(h, 1, false)
		t.resident = false
	}
}
func (t *trayState) recreate(h *Host) {
	t.activeID = 0
	t.queue = nil
	t.active = nativeNotice{}
	win.KillTimer(h.hwnd, trayTimer)
	t.resident = false
	if t.background {
		t.resident = icon(h, 1, true)
		if !t.resident {
			win.PostMessage(h.hwnd, wmActivate, 0, 0)
		}
	}
}

func (t *trayState) callback(h *Host, wparam, lparam uintptr) {
	message, id := uint32(lparam&0xffff), uint32((lparam>>16)&0xffff)
	if id == 1 && t.resident {
		switch message {
		case win.NIN_SELECT, win.NIN_KEYSELECT:
			win.PostMessage(h.hwnd, wmActivate, 0, 0)
		case win.WM_CONTEXTMENU:
			menu := win.CreatePopupMenu()
			if menu == 0 {
				return
			}
			defer win.DestroyMenu(menu)
			if !appendTrayMenu(menu, 1, "開く / Open") || !appendTrayMenu(menu, 2, "停止して終了 / Stop and quit") {
				return
			}
			var p win.POINT
			win.GetCursorPos(&p)
			win.SetForegroundWindow(h.hwnd)
			choice := win.TrackPopupMenuEx(menu, win.TPM_RETURNCMD|win.TPM_RIGHTBUTTON, p.X, p.Y, h.hwnd, nil)
			if choice == 1 {
				win.PostMessage(h.hwnd, wmActivate, 0, 0)
			} else if choice == 2 {
				win.PostMessage(h.hwnd, wmQuit, 0, 0)
			}
			win.PostMessage(h.hwnd, win.WM_NULL, 0, 0)
		}
		return
	}
	if id == 0 || id != t.activeID {
		return
	}
	switch message {
	case win.NIN_BALLOONUSERCLICK, win.NIN_SELECT, win.NIN_KEYSELECT:
		h.sendNotificationActivation(t.active.id)
		t.finish(h)
	case win.NIN_BALLOONHIDE, win.NIN_BALLOONTIMEOUT:
		t.finish(h)
	}
}

func appendTrayMenu(menu win.HMENU, id uintptr, label string) bool {
	text, _ := windows.UTF16PtrFromString(label)
	result, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("AppendMenuW").Call(uintptr(menu), win.MF_STRING, id, uintptr(unsafe.Pointer(text)))
	return result != 0
}

func (h *Host) sendNotificationActivation(id string) {
	h.pendingNotification = id
	win.PostMessage(h.hwnd, wmActivate, 0, 0)
	if !h.ready || h.chromium == nil {
		return
	}
	view, err := h.chromium.GetController().GetCoreWebView2()
	if err != nil {
		return
	}
	defer view.Release()
	source, err := view.GetSource()
	if err != nil || source != h.options.Origin+"/" {
		return
	}
	message, _ := json.Marshal(map[string]string{"type": "notification.activate", "notification_id": id})
	if view.PostWebMessageAsString(string(message)) == nil {
		h.pendingNotification = ""
	}
}

// ActivateNotification forwards only an opaque saved ID to the trusted UI.
// The UI refreshes its authoritative snapshot before resolving the target.
func (h *Host) ActivateNotification(id string) error {
	if id == "" || len(id) > 128 {
		return errors.New("INVALID_NOTIFICATION")
	}
	return h.desktopCall(func() error { h.sendNotificationActivation(id); return nil })
}

func boundedUTF16(value string, capacity int) []uint16 {
	encoded := utf16.Encode([]rune(value))
	if len(encoded) >= capacity {
		encoded = encoded[:capacity-1]
		if len(encoded) > 0 && encoded[len(encoded)-1] >= 0xd800 && encoded[len(encoded)-1] <= 0xdbff {
			encoded = encoded[:len(encoded)-1]
		}
	}
	return append(encoded, 0)
}
