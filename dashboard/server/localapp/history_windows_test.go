//go:build windows

package localapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"pokegents/dashboard/server/history"
	d "pokegents/dashboard/server/taskdomain"
)

func TestHTTPHistoryInfoAndRequestBoundaries(t *testing.T) {
	server := newHTTPTestServer(t)
	manager := history.New(server.Service.Store, history.Config{
		Executable: filepath.Join(t.TempDir(), "missing-codex.exe"),
		Home:       t.TempDir(),
	})
	server.Service.History = manager
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	_, err := server.Service.Store.Update(context.Background(), "", nil, func(state *d.State, _ *sql.Tx) (any, []d.Event, error) {
		state.Projects["history-active"] = &d.Project{ID: "history-active", Name: "Active", ResolvedPath: t.TempDir()}
		state.Projects["history-archived"] = &d.Project{ID: "history-archived", Name: "Archived", ResolvedPath: t.TempDir(), Archived: true}
		return true, nil, nil
	})
	if err != nil {
		t.Fatalf("seed projects: %v", err)
	}

	unauthorized := localRequest(server, http.MethodGet, "/api/history/info", nil, false, server.Origin)
	if unauthorized.Code != http.StatusUnauthorized || errorCode(t, unauthorized) != "AUTH_REQUIRED" {
		t.Fatalf("unauthenticated info: status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	info := localRequest(server, http.MethodGet, "/api/history/info", nil, true, server.Origin)
	if info.Code != http.StatusOK {
		t.Fatalf("history info: status=%d body=%s", info.Code, info.Body.String())
	}
	var infoValue history.Info
	if err := json.Unmarshal(info.Body.Bytes(), &infoValue); err != nil {
		t.Fatalf("decode history info: %v", err)
	}
	if infoValue.Available || infoValue.Executable != manager.Info().Executable || infoValue.Home != manager.Info().Home {
		t.Fatalf("unexpected history info: %+v", infoValue)
	}

	for _, test := range []struct {
		name, body, code string
	}{
		{"missing project", `{"project_id":"missing","thread_id":"x"}`, "INVALID_PROJECT"},
		{"archived project", `{"project_id":"history-archived","thread_id":"x"}`, "INVALID_PROJECT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := localRequest(server, http.MethodPost, "/api/history/preview", []byte(test.body), true, server.Origin)
			if response.Code != http.StatusBadRequest || errorCode(t, response) != test.code {
				t.Fatalf("history preview: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	badInput := localRequest(server, http.MethodPost, "/api/history/list", []byte(`{"project_id":`), true, server.Origin)
	if badInput.Code != http.StatusBadRequest || errorCode(t, badInput) != "INVALID_REQUEST" {
		t.Fatalf("malformed history input: status=%d body=%s", badInput.Code, badInput.Body.String())
	}
}
