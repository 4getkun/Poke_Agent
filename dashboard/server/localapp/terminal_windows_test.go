//go:build windows

package localapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pokegents/dashboard/server/processhost"
	d "pokegents/dashboard/server/taskdomain"
)

type fakeTerminalNative struct {
	directory, mode string
	calls           int
}

func (f *fakeTerminalNative) PickDirectory() (string, error) { return "", nil }
func (f *fakeTerminalNative) OpenURL(string) error           { return nil }
func (f *fakeTerminalNative) OpenTerminal(directory, mode string) error {
	f.directory, f.mode = directory, mode
	f.calls++
	return nil
}

func TestTerminalRequiresUIAuthenticationAndRegisteredWorkspaceIdentity(t *testing.T) {
	server := newHTTPTestServer(t)
	server.Service.Capabilities.TerminalAvailable = true
	bridge := &fakeTerminalNative{}
	server.Native = bridge
	root := t.TempDir()
	folder := filepath.Join(root, "日本語 project & [x]")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	project, err := processhost.Workspace(folder)
	if err != nil {
		t.Fatal(err)
	}
	_, err = server.Service.Store.Update(context.Background(), "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects[project.ID] = project
		return true, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"project_id": project.ID, "mode": "open"})
	response := localRequest(server, http.MethodPost, "/api/native/terminal", body, false, server.Origin)
	if response.Code == 200 || bridge.calls != 0 {
		t.Fatal("unauthenticated terminal request accepted")
	}
	for _, bad := range []string{`{"project_id":"missing","mode":"open"}`, `{"project_id":"` + project.ID + `","mode":"shell"}`} {
		response = localRequest(server, http.MethodPost, "/api/native/terminal", []byte(bad), true, server.Origin)
		if response.Code == 200 || bridge.calls != 0 {
			t.Fatal("unregistered project/arbitrary mode accepted")
		}
	}
	response = localRequest(server, http.MethodPost, "/api/native/terminal", body, true, server.Origin)
	if response.Code != 200 || bridge.calls != 1 || bridge.directory != project.ResolvedPath || bridge.mode != "open" {
		t.Fatalf("registered request failed: %d %s", response.Code, response.Body.String())
	}
	// Alter only this empty, isolated test directory. A new folder at the same
	// text path must not inherit the registered volume/file identity.
	if err := os.Rename(folder, filepath.Join(root, "retained-old-directory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	response = localRequest(server, http.MethodPost, "/api/native/terminal", body, true, server.Origin)
	if response.Code == 200 || bridge.calls != 1 || !strings.Contains(response.Body.String(), "WORKSPACE_CHANGED") {
		t.Fatal("changed workspace identity accepted")
	}
}
