package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleDevDiagnostics(w http.ResponseWriter, r *http.Request) {
	setup := s.collectSetupStatus(r.Context())
	agents := s.state.GetAgents()
	chatSessions := s.chatMgr.All()
	writeJSON(w, map[string]any{
		"now":             time.Now().Format(time.RFC3339),
		"data_dir":        s.dataDir,
		"port":            s.port,
		"setup":           setup,
		"agents":          len(agents),
		"chat_sessions":   len(chatSessions),
		"backend_default": s.backendStore.DefaultID(),
		"backend_count":   len(s.backendStore.List()),
	})
}

func (s *Server) handleDevLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 2000 {
			limit = n
		}
	}
	name := r.URL.Query().Get("file")
	files := s.devLogFiles()
	if name == "" {
		writeJSON(w, map[string]any{"files": files})
		return
	}
	path := ""
	for _, f := range files {
		if f["name"] == name {
			path, _ = f["path"].(string)
			break
		}
	}
	if path == "" {
		http.Error(w, "unknown log file", http.StatusNotFound)
		return
	}
	lines, err := tailLines(path, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"file": name, "path": path, "lines": lines})
}

func (s *Server) devLogFiles() []map[string]any {
	var out []map[string]any
	addGlob := func(label, pattern string) {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			out = append(out, map[string]any{
				"name":     label + "/" + filepath.Base(path),
				"path":     path,
				"size":     info.Size(),
				"modified": info.ModTime().Format(time.RFC3339),
			})
		}
	}
	addGlob("usage", filepath.Join(s.dataDir, "usage", "*.jsonl"))
	addGlob("logs", filepath.Join(s.dataDir, "logs", "*.log"))
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i]["modified"].(string)
		aj, _ := out[j]["modified"].(string)
		return ai > aj
	})
	return out
}

func (s *Server) handleDevACPConnections(w http.ResponseWriter, _ *http.Request) {
	sessions := s.chatMgr.All()
	out := make([]map[string]any, 0, len(sessions))
	for _, sess := range sessions {
		sess.wsMu.RLock()
		wsCount := len(sess.wsClients)
		sess.wsMu.RUnlock()
		sess.tasksMu.Lock()
		taskCount := len(sess.activeTasks)
		sess.tasksMu.Unlock()
		sess.smMu.Lock()
		state := sess.smState
		busySince := sess.smBusySince
		sess.smMu.Unlock()
		out = append(out, map[string]any{
			"pokegent_id":   sess.PokegentID,
			"acp_id":        sess.ACPID,
			"profile":       sess.Profile,
			"cwd":           sess.Cwd,
			"agent_backend": sess.AgentBackend,
			"created":       sess.Created.Format(time.RFC3339),
			"last_updated":  time.UnixMilli(sess.lastUpdated.Load()).Format(time.RFC3339),
			"state":         state,
			"busy_since":    formatOptionalTime(busySince),
			"ws_clients":    wsCount,
			"active_tasks":  taskCount,
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleDevACPClose(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}
	if s.chatMgr.Get(pgid) == nil {
		http.Error(w, "ACP session not found", http.StatusNotFound)
		return
	}
	s.chatMgr.Close(pgid)
	writeJSON(w, map[string]any{"ok": true, "pokegent_id": pgid})
}

func (s *Server) handleDevACPForceIdle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pgid := s.resolveToPokegentID(id)
	if pgid == "" {
		pgid = id
	}
	sess := s.chatMgr.Get(pgid)
	if sess == nil {
		http.Error(w, "ACP session not found", http.StatusNotFound)
		return
	}
	sess.ForceIdle()
	writeJSON(w, map[string]any{"ok": true, "pokegent_id": pgid})
}

func tailLines(path string, limit int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	lines := make([]string, 0, limit)
	s := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	s.Buffer(buf, 1024*1024)
	for s.Scan() {
		line := s.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if json.Valid([]byte(line)) {
			var v any
			if json.Unmarshal([]byte(line), &v) == nil {
				pretty, _ := json.Marshal(v)
				line = string(pretty)
			}
		}
		if len(lines) == limit {
			copy(lines, lines[1:])
			lines[len(lines)-1] = line
		} else {
			lines = append(lines, line)
		}
	}
	return lines, s.Err()
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
