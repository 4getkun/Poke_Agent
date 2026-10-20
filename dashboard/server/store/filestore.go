package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NewFileStore creates a Store backed by files in the given data directory.
func NewFileStore(dataDir string) *Store {
	return &Store{
		Running:  &FileRunningStore{dir: filepath.Join(dataDir, "running")},
		Status:   &FileStatusStore{dir: filepath.Join(dataDir, "status")},
		Profiles: &FileProfileStore{dir: filepath.Join(dataDir, "profiles")},
		Config:   &FileConfigStore{path: filepath.Join(dataDir, "config.json")},
		Messages: &FileMessageStore{dir: filepath.Join(dataDir, "messages"), dataDir: dataDir},
		Activity: &FileActivityStore{
			activityDir: filepath.Join(dataDir, "activity"),
			lastReadDir: filepath.Join(dataDir, "activity-lastread"),
		},
		Metadata:  &FileMetadataStore{dir: dataDir},
		Projects:  &FileProjectStore{dir: filepath.Join(dataDir, "projects")},
		Roles:     &FileRoleStore{dir: filepath.Join(dataDir, "roles")},
		Ephemeral: &FileEphemeralStore{dir: filepath.Join(dataDir, "ephemeral")},
		Agents:    &FileAgentIdentityStore{dir: filepath.Join(dataDir, "agents")},
	}
}

// --- helpers ---

// atomicWrite writes data to a temp file then renames for crash safety.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".srv-tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ============================================================================
// FileRunningStore
// ============================================================================

type FileRunningStore struct {
	mu  sync.Mutex
	dir string
}

// findRunningFile finds a running file by pokegent_id or session_id.
// Tries filename glob first (fast), then falls back to content scan.
func (s *FileRunningStore) findRunningFile(id string) (string, error) {
	// Try filename pattern first (works for both old session_id and new pokegent_id naming)
	pattern := filepath.Join(s.dir, "*-"+id+".json")
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		return matches[0], nil
	}
	// Fallback: scan file contents for pokegent_id or session_id match
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", fmt.Errorf("running session %s not found", id)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rf struct {
			SessionID  string `json:"session_id"`
			PokegentID string `json:"pokegent_id"`
		}
		if json.Unmarshal(data, &rf) != nil {
			continue
		}
		if rf.PokegentID == id || rf.SessionID == id {
			return path, nil
		}
	}
	return "", fmt.Errorf("running session %s not found", id)
}

func (s *FileRunningStore) Get(sessionID string) (*RunningSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.findRunningFile(sessionID)
	if err != nil {
		return nil, err
	}
	return s.readFile(path)
}

func (s *FileRunningStore) GetByPokegentID(pokegentID string) (*RunningSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rs, err := s.readFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		if rs.PokegentID == pokegentID {
			return rs, nil
		}
	}
	return nil, fmt.Errorf("running session with pokegent_id %s not found", pokegentID)
}

func (s *FileRunningStore) List() ([]RunningSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil // empty is OK
	}
	var result []RunningSession
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rs, err := s.readFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		result = append(result, *rs)
	}
	return result, nil
}

func (s *FileRunningStore) Create(rs RunningSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	// Use pokegent_id for filename if available (stable, never renamed), fall back to session_id
	fileKey := rs.PokegentID
	if fileKey == "" {
		fileKey = rs.SessionID
	}
	path := filepath.Join(s.dir, rs.Profile+"-"+fileKey+".json")
	return atomicWrite(path, data, 0644)
}

func (s *FileRunningStore) Update(sessionID string, fn func(*RunningSession)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.findRunningFile(sessionID)
	if err != nil {
		return err
	}
	rs, err := s.readFile(path)
	if err != nil {
		return err
	}
	fn(rs)
	data, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	// Files are now keyed by pokegent_id — no rename needed on session_id change.
	// Just update content in place.
	return atomicWrite(path, data, 0644)
}

func (s *FileRunningStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Try finding by pokegent_id or session_id
	path, err := s.findRunningFile(sessionID)
	if err != nil {
		// Also try the legacy filename pattern as last resort
		pattern := filepath.Join(s.dir, "*-"+sessionID+".json")
		matches, _ := filepath.Glob(pattern)
		for _, f := range matches {
			os.Remove(f)
		}
		return nil
	}
	os.Remove(path)
	return nil
}

func (s *FileRunningStore) Watch() <-chan FileEvent {
	// Implemented by store/watcher.go (UI Specialist owns)
	return nil
}

func (s *FileRunningStore) readFile(path string) (*RunningSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs RunningSession
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, err
	}
	return &rs, nil
}

// ============================================================================
// FileStatusStore
// ============================================================================

type FileStatusStore struct {
	mu  sync.Mutex
	dir string
}

func (s *FileStatusStore) Get(sessionID string) (*StatusFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf StatusFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return &sf, nil
}

func (s *FileStatusStore) Upsert(sf StatusFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	key := sf.FileKey
	if key == "" {
		key = sf.SessionID
	}
	path := filepath.Join(s.dir, key+".json")
	return atomicWrite(path, data, 0644)
}

func (s *FileStatusStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.Remove(filepath.Join(s.dir, sessionID+".json"))
}

func (s *FileStatusStore) List() ([]StatusFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil
	}
	var result []StatusFile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var sf StatusFile
		if json.Unmarshal(data, &sf) == nil && sf.SessionID != "" {
			sf.FileKey = strings.TrimSuffix(e.Name(), ".json")
			result = append(result, sf)
		}
	}
	return result, nil
}

func (s *FileStatusStore) Watch() <-chan FileEvent {
	return nil // implemented by watcher
}

// ============================================================================
// FileProfileStore
// ============================================================================

type FileProfileStore struct {
	dir string
}

func (s *FileProfileStore) Get(name string) (*Profile, error) {
	path := filepath.Join(s.dir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	p.Name = name
	return &p, nil
}

func (s *FileProfileStore) List() ([]Profile, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil
	}
	var result []Profile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var p Profile
		if json.Unmarshal(data, &p) == nil {
			p.Name = name
			result = append(result, p)
		}
	}
	return result, nil
}

// ============================================================================
// FileConfigStore
// ============================================================================

type FileConfigStore struct {
	path string
}

func (s *FileConfigStore) Get() (*AppConfig, error) {
	cfg := &AppConfig{
		Port:                7834,
		DefaultProfile:      "personal",
		SkipPermissions:     false,
		ITermRestoreProfile: "Default",
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return cfg, nil // defaults if no config
	}
	json.Unmarshal(data, cfg) // partial parse OK — defaults fill gaps
	return cfg, nil
}

// ============================================================================
// FileMessageStore
// ============================================================================

type FileMessageStore struct {
	mu      sync.Mutex
	dir     string
	dataDir string
}

func (s *FileMessageStore) Send(from, fromName, to, toName, content string) (*Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := Message{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		From:      from,
		FromName:  fromName,
		To:        to,
		ToName:    toName,
		Content:   content,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Delivered: false,
	}
	mailbox := filepath.Join(s.dir, to)
	os.MkdirAll(mailbox, 0755)
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return &msg, atomicWrite(filepath.Join(mailbox, msg.ID+".json"), data, 0644)
}

func (s *FileMessageStore) GetUndelivered(sessionID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages, err := s.readMailbox(sessionID)
	if err != nil {
		return nil, err
	}
	var undelivered []Message
	for _, m := range messages {
		if !m.Delivered {
			undelivered = append(undelivered, m)
		}
	}
	return undelivered, nil
}

func (s *FileMessageStore) MarkDelivered(msgIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idSet := make(map[string]bool, len(msgIDs))
	for _, id := range msgIDs {
		idSet[id] = true
	}
	// Scan all mailboxes for matching IDs
	entries, _ := os.ReadDir(s.dir)
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		mailbox := filepath.Join(s.dir, d.Name())
		files, _ := os.ReadDir(mailbox)
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".json") || f.Name() == "_msg_budget" {
				continue
			}
			msgID := strings.TrimSuffix(f.Name(), ".json")
			if !idSet[msgID] {
				continue
			}
			path := filepath.Join(mailbox, f.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var msg Message
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			msg.Delivered = true
			if newData, err := json.Marshal(msg); err == nil {
				atomicWrite(path, newData, 0644)
			}
		}
	}
	return nil
}

func (s *FileMessageStore) Consume(sessionID string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages, err := s.readMailbox(sessionID)
	if err != nil {
		return nil, err
	}
	// Delete consumed files
	mailbox := filepath.Join(s.dir, sessionID)
	for _, msg := range messages {
		os.Remove(filepath.Join(mailbox, msg.ID+".json"))
	}
	return messages, nil
}

func (s *FileMessageStore) GetBudget(sessionID string) (int, error) {
	path := filepath.Join(s.dir, sessionID, "_msg_budget")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n, nil
}

func (s *FileMessageStore) ResetBudget(sessionID string) error {
	dir := filepath.Join(s.dir, sessionID)
	os.MkdirAll(dir, 0755)
	return os.WriteFile(filepath.Join(dir, "_msg_budget"), []byte("0"), 0644)
}

func (s *FileMessageStore) GetHistory() ([]Message, error) {
	path := filepath.Join(s.dataDir, "messages", "_history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var history []Message
	json.Unmarshal(data, &history)
	return history, nil
}

func (s *FileMessageStore) AppendHistory(msg Message) error {
	history, _ := s.GetHistory()
	history = append(history, msg)
	if len(history) > 200 {
		history = history[len(history)-200:]
	}
	data, _ := json.Marshal(history)
	return os.WriteFile(filepath.Join(s.dataDir, "messages", "_history.json"), data, 0644)
}

func (s *FileMessageStore) GetConnections() ([]Connection, error) {
	history, _ := s.GetHistory()
	type pairKey struct{ a, b string }
	pairs := make(map[pairKey]*Connection)
	for _, msg := range history {
		a, b := msg.From, msg.To
		an, bn := msg.FromName, msg.ToName
		if a > b {
			a, b = b, a
			an, bn = bn, an
		}
		key := pairKey{a, b}
		if conn, ok := pairs[key]; ok {
			conn.MessageCount++
			if msg.Timestamp > conn.LastMessage {
				conn.LastMessage = msg.Timestamp
			}
		} else {
			pairs[key] = &Connection{
				AgentA: a, AgentB: b,
				AgentAName: an, AgentBName: bn,
				MessageCount: 1, LastMessage: msg.Timestamp,
			}
		}
	}
	result := make([]Connection, 0, len(pairs))
	for _, c := range pairs {
		result = append(result, *c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastMessage > result[j].LastMessage
	})
	return result, nil
}

func (s *FileMessageStore) readMailbox(sessionID string) ([]Message, error) {
	mailbox := filepath.Join(s.dir, sessionID)
	entries, err := os.ReadDir(mailbox)
	if err != nil {
		return nil, nil
	}
	var messages []Message
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "_msg_budget" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(mailbox, e.Name()))
		if err != nil {
			continue
		}
		var msg Message
		if json.Unmarshal(data, &msg) == nil {
			messages = append(messages, msg)
		}
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp < messages[j].Timestamp
	})
	return messages, nil
}

// ============================================================================
// FileActivityStore
// ============================================================================

type FileActivityStore struct {
	mu          sync.Mutex
	activityDir string
	lastReadDir string
}

func (s *FileActivityStore) Append(projectHash string, entry ActivityEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	os.MkdirAll(s.activityDir, 0755)
	path := filepath.Join(s.activityDir, projectHash+".log")
	line := fmt.Sprintf("[%s] [%s] [%s] %s — %s\n",
		entry.Timestamp, entry.SessionID, entry.AgentName, entry.Files, entry.Summary)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)

	// Rotate if over 500 lines
	if info, _ := f.Stat(); info != nil && info.Size() > 50000 { // rough estimate
		s.rotate(path)
	}
	return err
}

func (s *FileActivityStore) GetSince(projectHash string, afterLine int) ([]ActivityEntry, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.activityDir, projectHash+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, nil
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	totalLines := len(lines)
	if afterLine >= totalLines {
		return nil, totalLines, nil
	}
	var entries []ActivityEntry
	for _, line := range lines[afterLine:] {
		entry := ActivityEntry{Raw: line}
		// Parse: [timestamp] [session_id] [agent_name] files — summary
		if parts := parseActivityLine(line); parts != nil {
			entry.Timestamp = parts[0]
			entry.SessionID = parts[1]
			entry.AgentName = parts[2]
			entry.Files = parts[3]
			entry.Summary = parts[4]
		}
		entries = append(entries, entry)
	}
	return entries, totalLines, nil
}

func (s *FileActivityStore) GetLastReadLine(projectHash, sessionID string) (int, error) {
	os.MkdirAll(s.lastReadDir, 0755)
	path := filepath.Join(s.lastReadDir, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, nil
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n, nil
}

func (s *FileActivityStore) SetLastReadLine(projectHash, sessionID string, line int) error {
	os.MkdirAll(s.lastReadDir, 0755)
	return os.WriteFile(filepath.Join(s.lastReadDir, sessionID), []byte(strconv.Itoa(line)), 0644)
}

// parseActivityLine extracts fields from a log line:
// [timestamp] [session_id] [agent_name] files — summary
// Returns [timestamp, session_id, agent_name, files, summary] or nil.
func parseActivityLine(line string) []string {
	// Extract bracketed fields
	result := make([]string, 5)
	remaining := line
	for i := 0; i < 3; i++ {
		start := strings.Index(remaining, "[")
		end := strings.Index(remaining, "]")
		if start == -1 || end == -1 || end <= start {
			return nil
		}
		result[i] = remaining[start+1 : end]
		remaining = remaining[end+1:]
	}
	remaining = strings.TrimSpace(remaining)
	// Split files — summary
	if idx := strings.Index(remaining, " — "); idx >= 0 {
		result[3] = strings.TrimSpace(remaining[:idx])
		result[4] = strings.TrimSpace(remaining[idx+len(" — "):])
	} else {
		result[3] = remaining
	}
	return result
}

func (s *FileActivityStore) rotate(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) <= 500 {
		return
	}
	kept := strings.Join(lines[len(lines)-200:], "\n")
	os.WriteFile(path, []byte(kept), 0644)
}

// ============================================================================
// FileMetadataStore
// ============================================================================

// FileMetadataStore manages small JSON metadata files in the data directory.
// Used for name-overrides.json, session-id-map.json, agent-order.json, etc.
type FileMetadataStore struct {
	mu  sync.Mutex
	dir string
}

func (s *FileMetadataStore) LoadJSON(filename string, dest any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // file doesn't exist yet — not an error
	}
	return json.Unmarshal(data, dest)
}

func (s *FileMetadataStore) SaveJSON(filename string, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, filename), bytes, 0644)
}

// ============================================================================
// FileProjectStore
// ============================================================================

type FileProjectStore struct {
	dir string
}

func (s *FileProjectStore) Get(name string) (*ProjectConfig, error) {
	path := filepath.Join(s.dir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p ProjectConfig
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	p.Name = name
	return &p, nil
}

func (s *FileProjectStore) List() ([]ProjectConfig, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil // empty is OK (dir may not exist yet)
	}
	var result []ProjectConfig
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var p ProjectConfig
		if json.Unmarshal(data, &p) == nil {
			p.Name = name
			result = append(result, p)
		}
	}
	return result, nil
}

// ============================================================================
// FileRoleStore
// ============================================================================

type FileRoleStore struct {
	dir string
}

func (s *FileRoleStore) Get(name string) (*RoleConfig, error) {
	path := filepath.Join(s.dir, name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r RoleConfig
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	r.Name = name
	return &r, nil
}

func (s *FileRoleStore) List() ([]RoleConfig, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, nil // empty is OK
	}
	var result []RoleConfig
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var r RoleConfig
		if json.Unmarshal(data, &r) == nil {
			r.Name = name
			result = append(result, r)
		}
	}
	return result, nil
}
