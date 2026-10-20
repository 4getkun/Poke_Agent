//go:build windows

package history

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"pokegents/dashboard/server/sqlstore"
	d "pokegents/dashboard/server/taskdomain"
)

type checkpoint struct {
	Version  int               `json:"version"`
	Messages map[string]string `json:"messages"`
}

func (m *Manager) Import(ctx context.Context, project *d.Project, request ImportRequest) (Report, error) {
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return Report{}, err
	}
	defer end()
	if len(request.RequestID) < 8 || len(request.RequestID) > 128 || len(request.PreviewSHA256) != 64 {
		return Report{}, d.Fail("INVALID_REQUEST")
	}
	if _, err := hex.DecodeString(request.PreviewSHA256); err != nil {
		return Report{}, d.Fail("INVALID_REQUEST")
	}
	payload, _ := json.Marshal(struct {
		Action string        `json:"action"`
		Data   ImportRequest `json:"data"`
	}{"history.import", request})
	// Recover a committed receipt before contacting a possibly changed source.
	var previous string
	var saved []byte
	err = m.Store.DB().QueryRowContext(ctx, `SELECT hash,result FROM request_dedup WHERE id=?`, request.RequestID).Scan(&previous, &saved)
	if err == nil {
		h := sha256.Sum256(payload)
		if previous != hex.EncodeToString(h[:]) {
			return Report{}, d.Fail("REQUEST_ID_REUSED")
		}
		var report Report
		err = json.Unmarshal(saved, &report)
		return report, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Report{}, err
	}
	preview, err := m.read(ctx, project, request.ThreadID)
	if err != nil {
		return Report{}, err
	}
	if preview.SHA256 != request.PreviewSHA256 {
		return Report{}, d.Fail("HISTORY_SOURCE_CHANGED")
	}
	if len(preview.Messages) == 0 {
		return Report{}, d.Fail("HISTORY_EMPTY")
	}
	sourceID := digest([]string{"codex", strings.ToLower(m.config.Home), request.TaskID, request.ThreadID})
	result, err := m.Store.Update(ctx, request.RequestID, payload, func(state *d.State, tx *sql.Tx) (any, []d.Event, error) {
		task := state.Tasks[request.TaskID]
		if task == nil || task.Spec.ProjectID != project.ID {
			return nil, nil, d.Fail("INVALID_PROJECT")
		}
		if task.Version != request.ExpectedVersion {
			return nil, nil, d.Fail("STALE_VERSION")
		}
		if (task.State != "BACKLOG" && task.State != "READY") || task.Block != "" {
			return nil, nil, d.Fail("HISTORY_BUSY")
		}
		for _, run := range state.Runs {
			if run.TaskID == task.ID && !run.WorkspaceQuiet {
				return nil, nil, d.Fail("HISTORY_BUSY")
			}
		}
		current := state.Projects[project.ID]
		if current == nil || current.Archived || current.Identity != project.Identity {
			return nil, nil, d.Fail("WORKSPACE_CHANGED")
		}
		old := checkpoint{Version: 1, Messages: map[string]string{}}
		var data []byte
		err := tx.QueryRowContext(ctx, `SELECT payload FROM import_checkpoints WHERE id=?`, sourceID).Scan(&data)
		if err == nil {
			if json.Unmarshal(data, &old) != nil || old.Version != 1 || old.Messages == nil {
				return nil, nil, d.Fail("UNSUPPORTED_HISTORY_FORMAT")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, nil, err
		}
		latest := checkpoint{Version: 1, Messages: map[string]string{}}
		for _, message := range preview.Messages {
			latest.Messages[message.Key] = digest(message)
		}
		for key, hash := range old.Messages {
			if latest.Messages[key] != hash {
				return nil, nil, d.Fail("HISTORY_SOURCE_CHANGED")
			}
		}
		report := Report{TaskID: task.ID, SourceID: sourceID, Skipped: preview.Skipped, Partial: preview.Partial}
		for _, message := range preview.Messages {
			if _, ok := old.Messages[message.Key]; ok {
				report.Duplicates++
				continue
			}
			if err := sqlstore.AddMessage(ctx, tx, d.Message{ID: digest([]string{sourceID, message.Key}), TaskID: task.ID, Role: message.Role, Body: message.Body, At: message.At}); err != nil {
				return nil, nil, err
			}
			report.Imported++
		}
		provenance, _ := json.Marshal(map[string]any{"provider": "codex", "session_id": request.ThreadID, "importer_version": ImporterVersion, "home_path": m.config.Home, "task_id": task.ID, "project_id": project.ID, "preview_sha256": preview.SHA256, "skipped": preview.Skipped, "imported_at": d.Now()})
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_sources(id,hash,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET hash=excluded.hash,payload=excluded.payload`, sourceID, digest([]string{sourceID, preview.SHA256}), string(provenance)); err != nil {
			return nil, nil, err
		}
		data, _ = json.Marshal(latest)
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_checkpoints(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, sourceID, string(data)); err != nil {
			return nil, nil, err
		}
		if report.Imported > 0 {
			task.Version++
		}
		return report, []d.Event{{Type: "history.imported", TaskID: task.ID, Payload: report}}, nil
	})
	if err != nil {
		return Report{}, err
	}
	var report Report
	err = json.Unmarshal(result, &report)
	return report, err
}
