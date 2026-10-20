//go:build windows

package localapp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"pokegents/dashboard/server/processhost"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	d "pokegents/dashboard/server/taskdomain"
)

type Native interface {
	PickDirectory() (string, error)
	OpenURL(string) error
}
type Server struct {
	Service       *Service
	Origin, Token string
	Native        Native
	listener      net.Listener
	http          *http.Server
	assets        fs.FS
	mcp           *mcpManager
}

func NewServer(service *Service, assets fs.FS) (*Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		listener.Close()
		return nil, err
	}
	s := &Server{Service: service, Origin: "http://" + listener.Addr().String(), Token: hex.EncodeToString(b[:]), listener: listener, assets: assets}
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	return s, nil
}
func (s *Server) Start() error                    { return s.http.Serve(s.listener) }
func (s *Server) Close(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; font-src 'self'; connect-src 'self'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; sandbox allow-scripts allow-same-origin allow-forms")
	if r.Host != strings.TrimPrefix(s.Origin, "http://") {
		http.Error(w, "Forbidden host", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.Origin {
		http.Error(w, "Forbidden origin", 403)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/mcp/tools/") {
		s.serveMCPTool(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Pokegents-Token")), []byte(s.Token)) != 1 {
			writeError(w, d.Fail("AUTH_REQUIRED"))
			return
		}
		s.api(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" || name == "." {
		name = "windows.html"
	}
	if !fs.ValidPath(name) || strings.HasPrefix(name, "api/") {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(s.assets, name)
	if err != nil {
		if !strings.HasPrefix(name, "assets/") && !strings.Contains(path.Base(name), ".") {
			b, err = fs.ReadFile(s.assets, "windows.html")
		}
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	var contentType string
	switch strings.ToLower(path.Ext(name)) {
	case ".ttf":
		contentType = "font/ttf"
	case ".woff2":
		contentType = "font/woff2"
	default:
		contentType = mime.TypeByExtension(path.Ext(name))
	}
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	if r.Method != "HEAD" {
		w.Write(b)
	}
}
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/history/"):
		s.serveHistory(w, r)
	case (r.Method == "GET" || r.Method == "POST") && r.URL.Path == "/api/mcp/settings":
		s.serveMCPSettings(w, r)
	case r.Method == "GET" && r.URL.Path == "/api/capabilities":
		writeJSON(w, s.Service.Capabilities)
	case r.Method == "GET" && r.URL.Path == "/api/snapshot":
		state, seq, settings, previews, err := s.Service.Store.ViewBoard(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		out := map[string]any{"event_seq": seq, "settings": settings, "previews": previews}
		v := reflect.ValueOf(state).Elem()
		for _, field := range []string{"Projects", "Roles", "Tasks", "Runs", "Approvals", "Submissions", "Decisions", "Effects", "Contexts", "Notifications", "Recovery"} {
			value := v.FieldByName(field)
			keys := value.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
			items := make([]any, 0, len(keys))
			for _, key := range keys {
				items = append(items, value.MapIndex(key).Interface())
			}
			name := strings.ToLower(field)
			if field == "Decisions" {
				name = "review_decisions"
			}
			out[name] = items
		}
		writeJSON(w, out)
	case r.Method == "GET" && (r.URL.Path == "/api/messages" || r.URL.Path == "/api/search"):
		cursor, err := parseCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, err)
			return
		}
		query := ""
		if r.URL.Path == "/api/search" {
			query = r.URL.Query().Get("q")
			if strings.TrimSpace(query) == "" || len(query) > 4096 {
				writeError(w, d.Fail("INVALID_QUERY"))
				return
			}
		}
		page, err := s.Service.Store.Messages(ctx, r.URL.Query().Get("task_id"), query, r.URL.Query().Get("project_id"), cursor, 50)
		if err != nil {
			writeError(w, err)
			return
		}
		var next any
		if page.NextCursor > 0 {
			next = strconv.FormatInt(page.NextCursor, 10)
		}
		writeJSON(w, map[string]any{"items": page.Items, "next_cursor": next, "interrupted": page.Interrupted})
	case r.Method == "GET" && r.URL.Path == "/api/events":
		s.stream(w, r)
	case r.Method == "POST" && r.URL.Path == "/api/commands":
		var c Command
		if err := readJSON(w, r, &c); err != nil {
			writeError(w, err)
			return
		}
		result, err := s.Service.Execute(ctx, c)
		if err != nil {
			writeError(w, err)
			return
		}
		if c.Action == "settings.desktop" {
			if bridge, ok := s.Native.(DesktopBridge); ok {
				_, revision, raw, readErr := s.Service.Store.View(ctx)
				var prefs struct {
					Desktop DesktopPreferences `json:"desktop"`
				}
				if readErr != nil || json.Unmarshal(raw, &prefs) != nil {
					writeError(w, d.Fail("INVALID_REQUEST"))
					return
				}
				if err := bridge.ConfigureDesktop(prefs.Desktop.Background, prefs.Desktop.Notifications, revision); err != nil {
					writeError(w, d.Fail("DESKTOP_UNAVAILABLE"))
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(result)
	case r.Method == "POST" && r.URL.Path == "/api/native/terminal":
		var data struct {
			ProjectID string `json:"project_id"`
			Mode      string `json:"mode"`
		}
		if err := readJSON(w, r, &data); err != nil {
			writeError(w, err)
			return
		}
		bridge, ok := s.Native.(interface{ OpenTerminal(string, string) error })
		if !ok || !s.Service.Capabilities.TerminalAvailable {
			writeError(w, d.Fail("TERMINAL_UNAVAILABLE"))
			return
		}
		if data.Mode != "open" && data.Mode != "focus" {
			writeError(w, d.Fail("INVALID_REQUEST"))
			return
		}
		state, err := s.Service.Store.Snapshot(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		project := state.Projects[data.ProjectID]
		if project == nil || project.Archived {
			writeError(w, d.Fail("INVALID_PROJECT"))
			return
		}
		current, err := processhost.Workspace(project.DisplayPath)
		if err != nil || current.Identity != project.Identity {
			writeError(w, d.Fail("WORKSPACE_CHANGED"))
			return
		}
		if err := bridge.OpenTerminal(current.ResolvedPath, data.Mode); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"requested": true})
	case r.Method == "POST" && r.URL.Path == "/api/native/pick-directory":
		var empty struct{}
		if err := readJSON(w, r, &empty); err != nil {
			writeError(w, err)
			return
		}
		if s.Native == nil {
			writeError(w, d.Fail("NATIVE_UNAVAILABLE"))
			return
		}
		selected, err := s.Native.PickDirectory()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, map[string]string{"path": selected})
	case r.Method == "POST" && r.URL.Path == "/api/native/open-url":
		var data struct {
			URL string `json:"url"`
		}
		if err := readJSON(w, r, &data); err != nil {
			writeError(w, err)
			return
		}
		if s.Native == nil {
			writeError(w, d.Fail("NATIVE_UNAVAILABLE"))
			return
		}
		if err := s.Native.OpenURL(data.URL); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, true)
	default:
		http.NotFound(w, r)
	}
}
func readJSON(w http.ResponseWriter, r *http.Request, out any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return d.Fail("INVALID_CONTENT_TYPE")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return d.Fail("INVALID_REQUEST")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return d.Fail("INVALID_REQUEST")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	var appError *d.Error
	if !errors.As(err, &appError) {
		appError = &d.Error{Code: "INTERNAL_ERROR", MessageKey: "error.INTERNAL_ERROR", CorrelationID: d.ID()}
	}
	status := 400
	switch appError.Code {
	case "AUTH_REQUIRED":
		status = 401
	case "NOT_FOUND":
		status = 404
	case "STALE_VERSION", "REQUEST_ID_REUSED", "WORKSPACE_BUSY", "STALE_RUNTIME", "APPROVAL_EXPIRED":
		status = 409
	case "INTERNAL_ERROR":
		status = 500
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(appError)
}
func parseCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, d.Fail("INVALID_CURSOR")
	}
	return value, nil
}
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	after, err := parseCursor(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, err)
		return
	}
	if after > s.Service.Store.LastSeq(r.Context()) {
		writeError(w, d.Fail("SNAPSHOT_REQUIRED"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, d.Fail("STREAM_UNAVAILABLE"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		events, err := s.Service.Store.Events(r.Context(), after)
		if err != nil {
			return
		}
		for _, event := range events {
			b, err := json.Marshal(event)
			if err != nil {
				return
			}
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.Seq, b); err != nil {
				return
			}
			after = event.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		case <-heartbeat.C:
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Second))
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
