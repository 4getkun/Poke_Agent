//go:build windows && integration

package localapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/processhost"
	d "pokegents/dashboard/server/taskdomain"
	"pokegents/dashboard/web"
)

type renderProbeAssets struct {
	fs.FS
	probe fstest.MapFS
}

func (a renderProbeAssets) Open(name string) (fs.File, error) {
	if _, ok := a.probe[name]; ok {
		return a.probe.Open(name)
	}
	return a.FS.Open(name)
}

// Bootstrap alone does not establish that the real embedded React UI renders.
// This test observes DOM/layout in the actual WebView2 using a test-only script.
func TestRealWebViewRendersEmbeddedWindowsUI(t *testing.T) {
	testEmbeddedWindowsUI(t, false, false)
}

func TestRealWebViewNotificationOpensSavedTask(t *testing.T) {
	testEmbeddedWindowsUI(t, true, false)
}

func TestRealWebViewTerminalControlsUseRegisteredProject(t *testing.T) {
	testEmbeddedWindowsUI(t, true, true)
}

type terminalUIProbe struct{ requests chan [2]string }

func (p *terminalUIProbe) PickDirectory() (string, error) { return "", nil }
func (p *terminalUIProbe) OpenURL(string) error           { return nil }
func (p *terminalUIProbe) OpenTerminal(path, mode string) error {
	p.requests <- [2]string{path, mode}
	return nil
}

func testEmbeddedWindowsUI(t *testing.T, activation, terminal bool) {
	testServer := newHTTPTestServer(t)
	project := &d.Project{ID: "notification-project", Name: "native synthetic"}
	terminalProbe := &terminalUIProbe{requests: make(chan [2]string, 2)}
	if terminal {
		folder := filepath.Join(t.TempDir(), "日本語 Terminal UI & [x]")
		if err := os.Mkdir(folder, 0700); err != nil {
			t.Fatal(err)
		}
		var err error
		project, err = processhost.Workspace(folder)
		if err != nil {
			t.Fatal(err)
		}
		project.ID = "notification-project"
		testServer.Service.Capabilities.DesktopEnabled = true
		testServer.Service.Capabilities.TerminalAvailable = true
		testServer.Native = terminalProbe
	}
	seedNotification := func() error {
		_, err := testServer.Service.Store.Update(context.Background(), "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
			state.Projects["notification-project"] = project
			state.Tasks["notification-task"] = &d.Task{ID: "notification-task", State: "BACKLOG", Version: 1, Spec: d.Spec{ProjectID: "notification-project", Objective: "通知から開く合成タスク", Version: 1, AllowedScope: "synthetic display only", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "view", Procedure: "open task", Expected: "visible", Required: true}}}}
			state.Notifications["saved-native-notification"] = &d.Notification{ID: "saved-native-notification", TaskID: "notification-task", Kind: "approval", At: d.Now()}
			return true, nil, nil
		})
		return err
	}
	built := web.Assets()
	html, err := fs.ReadFile(built, "windows.html")
	if err != nil {
		t.Fatal(err)
	}
	htmlText := strings.Replace(string(html), "<head>", `<head><script src="/__native-render-probe.js"></script>`, 1)
	if htmlText == string(html) {
		t.Fatal("embedded windows.html has no head")
	}
	probeJS := `
let sent=false; const errors=[];
window.addEventListener('error',e=>{if(errors.length<3)errors.push(String(e.message).slice(0,160));void inspect()});
window.addEventListener('unhandledrejection',e=>{if(errors.length<3)errors.push(String(e.reason).slice(0,160));void inspect()});
async function inspect(){
  const app=document.querySelector('.win-app');
  if(sent)return;
  if(!errors.length&&(!app||!document.querySelector('.win-workspace')||!document.fonts.check('12px "Zen Kaku Gothic New"')))return;
  sent=true;
  const rect=app?.getBoundingClientRect();
  const live=!!document.querySelector('.win-connection--live');
  await fetch('/__native-render-health',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({app:!!app,live,width:rect?.width??0,height:rect?.height??0,buttons:app?.querySelectorAll('button').length??0,title:document.querySelector('.win-brand strong')?.textContent??'',fonts:document.fonts.check('12px "Zen Kaku Gothic New"'),errors})});
}
new MutationObserver(inspect).observe(document.documentElement,{childList:true,subtree:true});
document.fonts.addEventListener('loadingdone',inspect);
void inspect();`
	if activation {
		probeJS = strings.Replace(probeJS, "const app=document.querySelector('.win-app');", "const app=document.querySelector('.win-app'); if(!errors.length && document.querySelector('.win-focus-heading h1')?.textContent!=='通知から開く合成タスク')return;", 1)
	}
	if terminal {
		probeJS = "let terminalRequested=false;\n" + probeJS
		probeJS = strings.Replace(probeJS, "if(sent)return;", `if(sent)return;
if(!errors.length){
  const control=[...document.querySelectorAll('.win-focus-pane button')].find(e=>e.textContent==='Terminalで開く');
  if(!terminalRequested){if(!control)return;terminalRequested=true;control.click();return;}
  if(!document.querySelector('.win-toast')?.textContent?.includes('Terminalに要求を送りました'))return;
}`, 1)
	}
	testServer.assets = renderProbeAssets{FS: built, probe: fstest.MapFS{
		"windows.html":             {Data: []byte(htmlText)},
		"__native-render-probe.js": {Data: []byte(probeJS)},
	}}
	type health struct {
		App, Live, Fonts bool
		Width, Height    float64
		Buttons          int
		Title            string
		Errors           []string
	}
	result := make(chan health, 1)
	var host *desktop.Host
	var once sync.Once
	var requestsMu sync.Mutex
	var requests []string
	var authenticatedSnapshot bool
	var activationOnce sync.Once
	testServer.http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/snapshot" && r.Header.Get("X-Pokegents-Token") == testServer.Token {
			authenticatedSnapshot = true
		}
		requestsMu.Unlock()
		if r.URL.Path != "/__native-render-health" {
			testServer.ServeHTTP(w, r)
			if activation && r.URL.Path == "/api/snapshot" {
				activationOnce.Do(func() {
					go func() {
						if err := seedNotification(); err != nil {
							t.Error(err)
							host.Close()
							return
						}
						if err := host.ActivateNotification("saved-native-notification"); err != nil {
							t.Error(err)
							host.Close()
						}
					}()
				})
			}
			return
		}
		// A test-only, write-only health sink; it exposes no app data or commands.
		if r.Method != "POST" || r.Host != strings.TrimPrefix(testServer.Origin, "http://") {
			http.Error(w, "unauthorized", 401)
			return
		}
		var status health
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&status); err != nil {
			t.Logf("native health decode failed: %v", err)
			http.Error(w, "invalid probe", 400)
			return
		}
		once.Do(func() { result <- status; host.Close() })
		w.WriteHeader(204)
	})
	go testServer.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		testServer.Close(ctx)
	}()
	host = desktop.New(desktop.Options{Origin: testServer.Origin, Token: testServer.Token, DataPath: filepath.Join(t.TempDir(), "WebView2"), Hidden: true, OnError: func(err error) { t.Error(err) }})
	timer := time.AfterFunc(30*time.Second, host.Close)
	defer timer.Stop()
	began := time.Now()
	if err := host.Run(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		requestsMu.Lock()
		authorized := authenticatedSnapshot
		requestsMu.Unlock()
		if !status.App || !authorized || !status.Fonts || status.Width < 640 || status.Height < 400 || status.Buttons < 5 || status.Title != "Pokegents for Windows" || len(status.Errors) != 0 {
			t.Fatalf("embedded UI failed native DOM/layout checks: %+v", status)
		}
		if activation {
			for _, request := range requests {
				if strings.HasPrefix(request, "POST /api/") && !(terminal && request == "POST /api/native/terminal") {
					t.Fatalf("activation sent a command: %s", request)
				}
			}
			t.Log("production React UI refreshed a stale snapshot and opened the saved notification's task; no command executed")
		}
		if terminal {
			select {
			case request := <-terminalProbe.requests:
				if request != ([2]string{project.ResolvedPath, "open"}) {
					t.Fatalf("wrong Terminal request: %v", request)
				}
			default:
				t.Fatal("Terminal UI did not call native boundary")
			}
			select {
			case request := <-terminalProbe.requests:
				t.Fatalf("duplicate Terminal request: %v", request)
			default:
			}
			t.Log("real production React Terminal control sent exactly one authenticated registered-project request; native launcher replaced by recorder in this UI test")
		}
		t.Logf("embedded production UI in real WebView2: connected, %d buttons, %.0fx%.0f layout, adopted font loaded, no JS errors; elapsed=%s", status.Buttons, status.Width, status.Height, time.Since(began).Round(time.Millisecond))
	default:
		requestsMu.Lock()
		t.Logf("native resource requests: %v", requests)
		requestsMu.Unlock()
		t.Fatal("embedded UI never reported native DOM/render readiness")
	}
}
