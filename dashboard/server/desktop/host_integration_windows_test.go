//go:build windows && integration

package desktop

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealWebViewBootstrapAndClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	token := "temporary-test-token-in-memory"
	var checked atomic.Bool
	h := New(Options{Origin: origin, Token: token, DataPath: filepath.Join(t.TempDir(), "WebView2"), Hidden: true, OnError: func(err error) { t.Error(err) }})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; frame-src 'none'; object-src 'none'; sandbox allow-scripts allow-same-origin")
		fmt.Fprint(w, `<!doctype html><html lang="ja"><meta charset="utf-8"><title>Pokegents WebView2試験</title><p>初期化と終了の実機試験</p><script src="/bootstrap.js"></script></html>`)
	})
	mux.HandleFunc("GET /bootstrap.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		fmt.Fprint(w, `window.chrome.webview.addEventListener('message',async e=>{let m=typeof e.data==='string'?JSON.parse(e.data):e.data;if(m.type==='bootstrap'){await fetch('/api/check',{headers:{'X-Pokegents-Token':m.token}})}});window.chrome.webview.postMessage(JSON.stringify({type:'bootstrap',version:1}));`)
	})
	mux.HandleFunc("GET /api/check", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Pokegents-Token") != token {
			http.Error(w, "unauthorized", 401)
			return
		}
		checked.Store(true)
		w.WriteHeader(204)
		go func() { time.Sleep(100 * time.Millisecond); h.Close() }()
	})
	server := &http.Server{Handler: mux}
	defer server.Close()
	go server.Serve(listener)
	timer := time.AfterFunc(20*time.Second, func() { h.Close() })
	defer timer.Stop()
	if err = h.Run(nil); err != nil {
		t.Fatal(err)
	}
	if !checked.Load() {
		t.Fatal("trusted top frame bootstrap/fetch was not verified")
	}
	t.Log("real WebView2: initialize, trusted bootstrap, token-authenticated fetch and close succeeded")
}
