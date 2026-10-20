//go:build windows

package localapp

import (
	"net/http"
	"pokegents/dashboard/server/history"
	d "pokegents/dashboard/server/taskdomain"
)

func (s *Server) serveHistory(w http.ResponseWriter, r *http.Request) {
	manager := s.Service.History
	if r.Method == "GET" && r.URL.Path == "/api/history/info" {
		if manager == nil {
			writeJSON(w, history.Info{Reason: "HISTORY_UNAVAILABLE"})
		} else {
			writeJSON(w, manager.Info())
		}
		return
	}
	if manager == nil {
		writeError(w, d.Fail("HISTORY_UNAVAILABLE"))
		return
	}
	if r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var input struct {
		ProjectID string `json:"project_id"`
		ThreadID  string `json:"thread_id"`
		Cursor    string `json:"cursor"`
		Archived  bool   `json:"archived"`
	}
	var request history.ImportRequest
	if r.URL.Path == "/api/history/import" {
		if err := readJSON(w, r, &request); err != nil {
			writeError(w, err)
			return
		}
	} else {
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, err)
			return
		}
	}
	state, err := s.Service.Store.Snapshot(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if r.URL.Path == "/api/history/import" {
		task := state.Tasks[request.TaskID]
		if task == nil {
			writeError(w, d.Fail("NOT_FOUND"))
			return
		}
		input.ProjectID = task.Spec.ProjectID
	}
	project := state.Projects[input.ProjectID]
	if project == nil || project.Archived {
		writeError(w, d.Fail("INVALID_PROJECT"))
		return
	}
	var result any
	switch r.URL.Path {
	case "/api/history/list":
		result, err = manager.List(r.Context(), project, input.Cursor, input.Archived)
	case "/api/history/preview":
		result, err = manager.Preview(r.Context(), project, input.ThreadID)
	case "/api/history/import":
		result, err = manager.Import(r.Context(), project, request)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, result)
}
