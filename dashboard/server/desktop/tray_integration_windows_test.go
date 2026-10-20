//go:build windows && integration

package desktop

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

func TestRealTrayResidenceAndNotificationActivation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	var once sync.Once
	var closeCalls atomic.Int32
	activated := make(chan string, 2)
	result := make(chan error, 1)
	var h *Host
	h = New(Options{Origin: origin, Token: "synthetic-in-memory", DataPath: filepath.Join(t.TempDir(), "WebView2"), Hidden: true,
		CanClose: func() bool { closeCalls.Add(1); return true }, OnReady: func() {
			once.Do(func() {
				go func() {
					if err := h.ConfigureDesktop(true, true, 1); err != nil {
						result <- err
						h.Close()
						return
					}
					if err := h.ConfigureDesktop(false, false, 0); err != nil {
						result <- err
						h.Close()
						return
					}
					// Native Shell registration is real. Callback injection is deliberate:
					// this test does not claim a physical user click or visible OS banner.
					err := h.desktopCall(func() error {
						if !h.tray.resident || !h.tray.background {
							return errors.New("tray not registered")
						}
						win.SendMessage(h.hwnd, win.WM_CLOSE, 0, 0)
						valid, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("IsWindow").Call(uintptr(h.hwnd))
						if closeCalls.Load() != 0 || valid == 0 {
							return errors.New("opt-in close did not retain window")
						}
						h.tray.queue = append(h.tray.queue, nativeNotice{"saved-notification-id", "Pokegents", "完了\n合成タスク"})
						if err := h.tray.next(h); err != nil {
							return err
						}
						id := h.tray.activeID
						if id == 0 {
							return errors.New("notification icon not registered")
						}
						h.tray.callback(h, 0, uintptr(win.NIN_BALLOONUSERCLICK)|(uintptr(id)<<16))
						// Late callbacks must not activate another target.
						h.tray.callback(h, 0, uintptr(win.NIN_BALLOONUSERCLICK)|(uintptr(id)<<16))
						return nil
					})
					if err == nil {
						select {
						case id := <-activated:
							if id != "saved-notification-id" {
								err = fmt.Errorf("wrong target %q", id)
							}
						case <-time.After(5 * time.Second):
							err = errors.New("trusted activation bridge did not arrive")
						}
					}
					if err == nil {
						err = h.ConfigureDesktop(false, false, 2)
					}
					result <- err
					h.Close()
				}()
			})
		}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Pokegents hidden integration test</title><script src="/bridge.js"></script>`)
	})
	mux.HandleFunc("GET /bridge.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, `window.chrome.webview.addEventListener('message',e=>{const m=JSON.parse(e.data);if(m.type==='notification.activate')fetch('/activated',{method:'POST',body:m.notification_id})});window.chrome.webview.postMessage(JSON.stringify({type:'bootstrap',version:1}));`)
	})
	mux.HandleFunc("POST /activated", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 129))
		activated <- string(b)
		w.WriteHeader(204)
	})
	server := &http.Server{Handler: mux}
	defer server.Close()
	go server.Serve(listener)
	timer := time.AfterFunc(25*time.Second, func() { h.Close() })
	defer timer.Stop()
	if err := h.Run(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("native tray test did not finish")
	}
	if closeCalls.Load() != 1 {
		t.Fatalf("explicit quit must close once, got %d", closeCalls.Load())
	}
	if h.tray.resident || h.tray.activeID != 0 {
		t.Fatal("native icons retained after quit")
	}
	select {
	case id := <-activated:
		t.Fatalf("stale activation delivered %q", id)
	default:
	}
	t.Log("real Shell icon registration, opt-in close, explicit quit cleanup, trusted WebView notification-ID activation and stale-callback refusal passed; callback injected, no physical click/banner claim")
}
