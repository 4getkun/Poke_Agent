//go:build windows && integration

package localapp

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/history"
	"pokegents/dashboard/web"
)

func TestRealWebViewHistoryDrawerShowsUnavailableWithoutListing(t *testing.T) {
	testServer := newHTTPTestServer(t)
	manager := history.New(testServer.Service.Store, history.Config{
		Executable: filepath.Join(t.TempDir(), "missing-codex.exe"),
		Home:       t.TempDir(),
	})
	testServer.Service.History = manager
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	built := web.Assets()
	html, err := fs.ReadFile(built, "windows.html")
	if err != nil {
		t.Fatal(err)
	}
	htmlText := strings.Replace(string(html), "<head>", `<head><script src="/__history-render-probe.js"></script>`, 1)
	if htmlText == string(html) {
		t.Fatal("embedded windows.html has no head")
	}
	probeJS := `
let clicked=false, sent=false; const errors=[];
window.addEventListener('error',e=>{if(errors.length<3)errors.push(String(e.message).slice(0,160));void inspect()});
window.addEventListener('unhandledrejection',e=>{if(errors.length<3)errors.push(String(e.reason).slice(0,160));void inspect()});
async function inspect(){
  if(sent)return;
  const app=document.querySelector('.win-app');
  if(!app)return;
  if(!clicked){const button=[...app.querySelectorAll('.win-tool-button')].find(e=>e.textContent.includes('履歴の取り込み'));if(!button)return;clicked=true;button.click();return;}
  const drawer=[...app.querySelectorAll('.win-drawer-content')].find(e=>e.textContent.includes('Codexセッション履歴の取り込み'));
  if(!errors.length&&(!drawer||!drawer.textContent.includes('Codex履歴を利用できません')||!drawer.textContent.includes('missing-codex.exe')||!drawer.textContent.includes('履歴ホーム:')))return;
  sent=true;
  await fetch('/__history-render-health',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({drawer:!!drawer,text:drawer?.textContent??'',errors,clicked})});
}
new MutationObserver(inspect).observe(document.documentElement,{childList:true,subtree:true});
void inspect();`
	testServer.assets = renderProbeAssets{FS: built, probe: fstest.MapFS{
		"windows.html":              {Data: []byte(htmlText)},
		"__history-render-probe.js": {Data: []byte(probeJS)},
	}}
	type health struct {
		Drawer  bool
		Text    string
		Errors  []string
		Clicked bool
	}
	result := make(chan health, 1)
	var host *desktop.Host
	var once sync.Once
	var requestsMu sync.Mutex
	var requests []string
	testServer.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		requestsMu.Unlock()
		if r.URL.Path != "/__history-render-health" {
			testServer.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost || r.Host != strings.TrimPrefix(testServer.Origin, "http://") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var status health
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&status); err != nil {
			t.Errorf("history health decode failed: %v", err)
			http.Error(w, "invalid probe", http.StatusBadRequest)
			return
		}
		once.Do(func() { result <- status; host.Close() })
		w.WriteHeader(http.StatusNoContent)
	})
	go testServer.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = testServer.Close(ctx)
	}()
	host = desktop.New(desktop.Options{Origin: testServer.Origin, Token: testServer.Token, DataPath: filepath.Join(t.TempDir(), "WebView2"), Hidden: true, OnError: func(err error) { t.Error(err) }})
	timer := time.AfterFunc(30*time.Second, host.Close)
	defer timer.Stop()
	if err := host.Run(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if !status.Drawer || !status.Clicked || !strings.Contains(status.Text, "missing-codex.exe") || !strings.Contains(status.Text, "履歴ホーム:") || !strings.Contains(status.Text, "Codex履歴を利用できません") || len(status.Errors) != 0 {
			t.Fatalf("history drawer DOM check failed: %+v", status)
		}
		requestsMu.Lock()
		defer requestsMu.Unlock()
		for _, request := range requests {
			if strings.HasPrefix(request, "POST /api/history/") {
				t.Fatalf("history listing ran before explicit user action: %s", request)
			}
		}
	default:
		requestsMu.Lock()
		t.Logf("native resource requests: %v", requests)
		requestsMu.Unlock()
		t.Fatal("history drawer never reported its unavailable state")
	}
}
