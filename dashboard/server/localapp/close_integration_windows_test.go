//go:build windows && integration

package localapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"pokegents/dashboard/server/desktop"
	"pokegents/dashboard/server/runtimeadapter"
	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

func TestRealWebViewCloseStopsActiveCodex(t *testing.T) {
	exe := os.Getenv("POKEGENTS_TEST_CODEX_EXE")
	if exe == "" {
		t.Fatal("explicit pinned native Codex exe required")
	}
	root := t.TempDir()
	store, err := sqlstore.Open(filepath.Join(root, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Runtime is enabled only in the isolated acceptance fixture.
	service := NewService(store, Capabilities{RuntimePath: exe, RuntimeVersion: runtimeadapter.SupportedVersion, ChatEnabled: true})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	invoke := func(action, id string, version int64, data any, out any) error {
		body, err := json.Marshal(data)
		if err != nil {
			return err
		}
		result, err := service.Execute(context.Background(), Command{RequestID: d.ID(), Action: action, TaskID: id, ExpectedVersion: version, Data: body})
		if err == nil && out != nil {
			err = json.Unmarshal(result, out)
		}
		return err
	}
	var project d.Project
	if err := invoke("project.create", "", 0, map[string]any{"path": root, "name": "終了試験"}, &project); err != nil {
		t.Fatal(err)
	}
	spec := d.Spec{ProjectID: project.ID, Objective: "公開の数字の説明", AllowedScope: "読み取り専用、ツールなし", OutOfScope: "ファイルや外部操作", ApprovalProfile: "read-only", Criteria: []d.Criterion{{ID: "close", Procedure: "実行中のウィンドウ終了", Expected: "中断して終了", Required: true}}}
	var task d.Task
	if err := invoke("task.create", "", 0, map[string]any{"spec": spec}, &task); err != nil {
		t.Fatal(err)
	}
	if err := invoke("task.ready", task.ID, task.Version, map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	assets := fstest.MapFS{
		"windows.html": {Data: []byte(`<!doctype html><meta charset="utf-8"><title>終了試験</title><p>実行中の終了処理を検証しています。</p><script src="/bootstrap.js"></script>`)},
		"bootstrap.js": {Data: []byte(`window.chrome.webview.postMessage(JSON.stringify({type:'bootstrap',version:1}));`)},
	}
	server, err := NewServer(service, assets)
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		server.Close(ctx)
	}()
	var host *desktop.Host
	var once sync.Once
	worker := make(chan error, 1)
	activeAtClose, canCloseCalled := false, false
	var closeErr error
	var elapsed time.Duration
	var runtimeHandle windows.Handle
	host = desktop.New(desktop.Options{
		Origin: server.Origin, Token: server.Token, DataPath: filepath.Join(root, "WebView2"), Hidden: true,
		OnError: func(err error) { t.Error(err) },
		CanClose: func() bool {
			canCloseCalled = true
			activeAtClose = service.Active()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			began := time.Now()
			closeErr = service.Close(ctx)
			elapsed = time.Since(began)
			return true // close the test window even if its acceptance assertion fails
		},
		OnReady: func() {
			once.Do(func() {
				go func() {
					defer host.Close()
					state, err := store.Snapshot(context.Background())
					if err == nil {
						err = invoke("task.start", task.ID, state.Tasks[task.ID].Version, map[string]any{"input": "ツールは使わず、1から10000までの数をそれぞれ日本語の一文で説明してください。省略しないでください。"}, nil)
					}
					if err != nil {
						worker <- err
						return
					}
					deadline := time.Now().Add(60 * time.Second)
					for time.Now().Before(deadline) {
						page, err := store.Messages(context.Background(), task.ID, "", "", 0, 100)
						if err != nil {
							worker <- err
							return
						}
						for _, message := range page.Items {
							if message.Role == "assistant" && message.Body != "" && service.Active() {
								runtimeHandle, err = activeRuntimeHandle(exe)
								worker <- err
								return
							}
						}
						time.Sleep(20 * time.Millisecond)
					}
					worker <- fmt.Errorf("no active native stream before close deadline")
				}()
			})
		},
	})
	timer := time.AfterFunc(75*time.Second, host.Close)
	defer timer.Stop()
	if err := host.Run(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-worker:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("native bootstrap/active stream was not verified")
	}
	if runtimeHandle == 0 {
		t.Fatal("no live runtime process handle captured")
	}
	defer windows.CloseHandle(runtimeHandle)
	if !canCloseCalled || !activeAtClose || closeErr != nil || service.Active() || elapsed > 5*time.Second {
		t.Fatalf("close called=%v activeBefore=%v activeAfter=%v error=%v elapsed=%s", canCloseCalled, activeAtClose, service.Active(), closeErr, elapsed)
	}
	status, err := windows.WaitForSingleObject(runtimeHandle, 0)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("owned runtime survived native WM_CLOSE: status=%d error=%v", status, err)
	}
	state, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	run := state.Runs[state.Tasks[task.ID].CurrentRunID]
	if run == nil || !run.Terminal() || !run.WorkspaceQuiet || run.State == "succeeded" {
		t.Fatalf("active run did not stop/quiesce: %#v", run)
	}
	t.Logf("real WebView2 WM_CLOSE during native Codex stream: stopped owned runtime, terminal=%s quiet=%v, close latency=%s", run.State, run.WorkspaceQuiet, elapsed.Round(time.Millisecond))
}

func activeRuntimeHandle(executable string) (windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ParentProcessID != uint32(os.Getpid()) {
			continue
		}
		h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			continue
		}
		var path [32768]uint16
		size := uint32(len(path))
		if windows.QueryFullProcessImageName(h, 0, &path[0], &size) == nil && filepath.Clean(windows.UTF16ToString(path[:size])) == filepath.Clean(executable) {
			return h, nil
		}
		windows.CloseHandle(h)
	}
	return 0, fmt.Errorf("native runtime live handle not found")
}
