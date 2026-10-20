// Package sqlstore is the authoritative SQLite store for the Windows app.
package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-sqlite3"
	"golang.org/x/text/unicode/norm"
	d "pokegents/dashboard/server/taskdomain"
)

type Store struct {
	db     *sql.DB
	writer sync.Mutex
}

const migrationTableSQL = `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,applied_at TEXT NOT NULL)`

type entity struct{ name, field, extra string }

var entities = []entity{
	{"projects", "Projects", ``}, {"roles", "Roles", ``},
	{"tasks", "Tasks", `, state TEXT GENERATED ALWAYS AS (json_extract(payload,'$.state')) STORED CHECK(state IN ('BACKLOG','READY','DOING','REVIEW','DONE','CANCELLED')), version INTEGER GENERATED ALWAYS AS (json_extract(payload,'$.version')) STORED CHECK(version>0), project_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.spec.project_id')) STORED REFERENCES projects(id)`},
	{"task_spec_versions", "SpecHistory", ``},
	{"runs", "Runs", `, task_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.task_id')) STORED REFERENCES tasks(id), state TEXT GENERATED ALWAYS AS (json_extract(payload,'$.state')) STORED CHECK(state IN ('queued','starting','running','waiting_approval','cancelling','cancelled','succeeded','failed','interrupted'))`},
	{"approvals", "Approvals", `, run_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.run_id')) STORED REFERENCES runs(id), state TEXT GENERATED ALWAYS AS (json_extract(payload,'$.state')) STORED CHECK(state IN ('pending','accept','decline','expired'))`},
	{"review_submissions", "Submissions", `, task_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.task_id')) STORED REFERENCES tasks(id), run_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.run_id')) STORED REFERENCES runs(id)`},
	{"review_decisions", "Decisions", `, submission_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.submission_id')) STORED REFERENCES review_submissions(id)`},
	{"effect_records", "Effects", `, run_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.run_id')) STORED REFERENCES runs(id), outcome TEXT GENERATED ALWAYS AS (json_extract(payload,'$.outcome')) STORED CHECK(outcome IN ('pending','succeeded','failed','unknown'))`},
	{"context_packets", "Contexts", `, task_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.task_id')) STORED REFERENCES tasks(id)`},
	{"notifications", "Notifications", `, task_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.task_id')) STORED REFERENCES tasks(id)`},
	{"recovery_records", "Recovery", `, task_id TEXT GENERATED ALWAYS AS (json_extract(payload,'$.task_id')) STORED REFERENCES tasks(id)`},
}

func schema() string {
	var b strings.Builder
	for _, e := range entities {
		fmt.Fprintf(&b, "CREATE TABLE %s(id TEXT PRIMARY KEY NOT NULL,payload TEXT NOT NULL CHECK(json_valid(payload))%s);\n", e.name, e.extra)
	}
	b.WriteString(`CREATE TABLE settings(id TEXT PRIMARY KEY,payload TEXT NOT NULL CHECK(json_valid(payload)));
CREATE TABLE messages(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,task_id TEXT NOT NULL REFERENCES tasks(id),run_id TEXT REFERENCES runs(id),role TEXT NOT NULL CHECK(role IN ('user','assistant','tool','system')),body TEXT NOT NULL,normalized TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE INDEX messages_task_seq ON messages(task_id,seq);
CREATE VIRTUAL TABLE message_fts USING fts5(normalized,content='messages',content_rowid='seq',tokenize='trigram');
CREATE TRIGGER messages_ai AFTER INSERT ON messages BEGIN INSERT INTO message_fts(rowid,normalized) VALUES(new.seq,new.normalized); END;
CREATE TRIGGER messages_ad AFTER DELETE ON messages BEGIN INSERT INTO message_fts(message_fts,rowid,normalized) VALUES('delete',old.seq,old.normalized); END;
CREATE TRIGGER messages_au AFTER UPDATE ON messages BEGIN INSERT INTO message_fts(message_fts,rowid,normalized) VALUES('delete',old.seq,old.normalized); INSERT INTO message_fts(rowid,normalized) VALUES(new.seq,new.normalized); END;
CREATE TABLE audit_events(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,payload TEXT NOT NULL CHECK(json_valid(payload)));
CREATE TABLE request_dedup(id TEXT PRIMARY KEY,hash TEXT NOT NULL,result TEXT NOT NULL CHECK(json_valid(result)));
CREATE TABLE import_sources(id TEXT PRIMARY KEY,hash TEXT NOT NULL UNIQUE,payload TEXT NOT NULL CHECK(json_valid(payload)));
CREATE TABLE import_checkpoints(id TEXT PRIMARY KEY REFERENCES import_sources(id),payload TEXT NOT NULL CHECK(json_valid(payload)));
CREATE TABLE runtime_instances(id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id),generation INTEGER NOT NULL CHECK(generation>0),payload TEXT NOT NULL CHECK(json_valid(payload)));
CREATE TABLE workspace_ownership(identity TEXT PRIMARY KEY,run_id TEXT NOT NULL UNIQUE REFERENCES runs(id));
CREATE TABLE task_blocks(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),payload TEXT NOT NULL CHECK(json_valid(payload)));
`)
	// Domain entities are flushed after the callback. Deferred foreign keys
	// allow a new run and its first message to commit atomically.
	sql := b.String()
	for _, table := range []string{"projects", "tasks", "runs", "review_submissions", "import_sources"} {
		sql = strings.ReplaceAll(sql, "REFERENCES "+table+"(id)", "REFERENCES "+table+"(id) DEFERRABLE INITIALLY DEFERRED")
	}
	return sql
}

func Open(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	dsn := u.String() + "?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_synchronous=FULL&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err = s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, migrationTableSQL); err != nil {
		return err
	}
	var version int
	var checksum string
	err = tx.QueryRowContext(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &checksum)
	hash := sha256.Sum256([]byte(schema()))
	expected := hex.EncodeToString(hash[:])
	if err == nil {
		if version != 1 || checksum != expected {
			return errors.New("UNSUPPORTED_SCHEMA")
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return s.probe(ctx)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = tx.ExecContext(ctx, schema()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations VALUES(1,?,?)`, expected, d.Now()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.probe(ctx)
}
func (s *Store) probe(ctx context.Context) error {
	var count int
	return s.db.QueryRowContext(ctx, `SELECT count(*) FROM message_fts WHERE message_fts MATCH '"fts_probe"'`).Scan(&count)
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db } // Internal integration/tests only, never a UI bridge.
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func load(ctx context.Context, q queryer) (*d.State, error) {
	state := d.NewState()
	v := reflect.ValueOf(state).Elem()
	for _, e := range entities {
		rows, err := q.QueryContext(ctx, "SELECT id,payload FROM "+e.name)
		if err != nil {
			return nil, err
		}
		field := v.FieldByName(e.field)
		for rows.Next() {
			var id string
			var b []byte
			if err = rows.Scan(&id, &b); err != nil {
				rows.Close()
				return nil, err
			}
			value := reflect.New(field.Type().Elem().Elem())
			if err = json.Unmarshal(b, value.Interface()); err != nil {
				rows.Close()
				return nil, err
			}
			field.SetMapIndex(reflect.ValueOf(id), value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return state, nil
}
func (s *Store) Snapshot(ctx context.Context) (*d.State, error) {
	state, _, _, err := s.View(ctx)
	return state, err
}

// View returns state and its event watermark from the same snapshot. Reading
// max(seq) after committing the snapshot could otherwise skip unseen changes.
func (s *Store) View(ctx context.Context) (*d.State, int64, json.RawMessage, error) {
	state, seq, settings, _, err := s.view(ctx, false)
	return state, seq, settings, err
}

// MessagePreview is a bounded excerpt from the newest message for a task.
type MessagePreview struct {
	Body      string `json:"body"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
}

// ViewBoard returns the regular snapshot and one bounded latest-message preview
// per task from the same SQLite read transaction.
func (s *Store) ViewBoard(ctx context.Context) (*d.State, int64, json.RawMessage, map[string]MessagePreview, error) {
	return s.view(ctx, true)
}

func (s *Store) view(ctx context.Context, includePreviews bool) (*d.State, int64, json.RawMessage, map[string]MessagePreview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, nil, nil, translate(err)
	}
	defer tx.Rollback()
	state, err := load(ctx, tx)
	if err != nil {
		return nil, 0, nil, nil, err
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM audit_events`).Scan(&seq); err != nil {
		return nil, 0, nil, nil, err
	}
	settings := []byte(`{"locale":"ja-JP","board_order":[]}`)
	var saved []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM settings WHERE id='preferences'`).Scan(&saved)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil, nil, err
	}
	if err == nil {
		var preferences map[string]any
		if err = json.Unmarshal(saved, &preferences); err != nil {
			return nil, 0, nil, nil, err
		}
		if _, ok := preferences["board_order"]; !ok {
			preferences["board_order"] = []string{}
		}
		settings, err = json.Marshal(preferences)
		if err != nil {
			return nil, 0, nil, nil, err
		}
	}
	previews := map[string]MessagePreview{}
	if includePreviews {
		rows, queryErr := tx.QueryContext(ctx, `SELECT m.task_id,substr(m.body,1,280),m.role,m.created_at
			FROM tasks t JOIN messages m ON m.seq=(SELECT max(latest.seq) FROM messages latest WHERE latest.task_id=t.id)`)
		if queryErr != nil {
			return nil, 0, nil, nil, queryErr
		}
		for rows.Next() {
			var taskID string
			var preview MessagePreview
			if err = rows.Scan(&taskID, &preview.Body, &preview.Role, &preview.CreatedAt); err != nil {
				rows.Close()
				return nil, 0, nil, nil, err
			}
			previews[taskID] = preview
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, 0, nil, nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, nil, nil, translate(err)
	}
	return state, seq, json.RawMessage(settings), previews, nil
}

// Update serializes mutations. The callback never waits for runtime or UI I/O.
// A dedup hit returns the original result without running the callback again.
func (s *Store) Update(ctx context.Context, requestID string, payload []byte, fn func(*d.State, *sql.Tx) (any, []d.Event, error)) (json.RawMessage, error) {
	s.writer.Lock()
	defer s.writer.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, translate(err)
	}
	defer tx.Rollback()
	h := sha256.Sum256(payload)
	hash := hex.EncodeToString(h[:])
	if requestID != "" {
		var previous string
		var result []byte
		err = tx.QueryRowContext(ctx, `SELECT hash,result FROM request_dedup WHERE id=?`, requestID).Scan(&previous, &result)
		if err == nil {
			if previous != hash {
				return nil, d.Fail("REQUEST_ID_REUSED")
			}
			return result, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, translate(err)
		}
	}
	state, err := load(ctx, tx)
	if err != nil {
		return nil, err
	}
	before := reflect.ValueOf(state).Elem()
	original := map[string]map[string]string{}
	for _, e := range entities {
		original[e.name] = map[string]string{}
		iter := before.FieldByName(e.field).MapRange()
		for iter.Next() {
			b, _ := json.Marshal(iter.Value().Interface())
			original[e.name][iter.Key().String()] = string(b)
		}
	}
	result, events, err := fn(state, tx)
	if err != nil {
		return nil, err
	}
	value := reflect.ValueOf(state).Elem()
	for _, e := range entities {
		iter := value.FieldByName(e.field).MapRange()
		for iter.Next() {
			id := iter.Key().String()
			b, err := json.Marshal(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			if original[e.name][id] == string(b) {
				continue
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+e.name+"(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", id, string(b)); err != nil {
				return nil, translate(err)
			}
		}
	}
	// Rebuild the small ownership table from authoritative run state.
	if _, err = tx.ExecContext(ctx, `DELETE FROM workspace_ownership`); err != nil {
		return nil, err
	}
	for _, r := range state.Runs {
		if !r.WorkspaceQuiet {
			p := state.Projects[r.Snapshot.ProjectID]
			if p == nil {
				return nil, d.Fail("INVALID_PROJECT")
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_ownership VALUES(?,?)`, p.Identity, r.ID); err != nil {
				return nil, translate(err)
			}
		}
	}
	for _, event := range events {
		event.SchemaVersion = 1
		event.ID = d.ID()
		event.At = d.Now()
		b, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,payload) VALUES(?,?)`, event.ID, string(b)); err != nil {
			return nil, err
		}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if requestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO request_dedup VALUES(?,?,?)`, requestID, hash, string(b)); err != nil {
			return nil, translate(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, translate(err)
	}
	return b, nil
}
func translate(err error) error {
	var se sqlite3.Error
	if errors.As(err, &se) && (se.Code == sqlite3.ErrBusy || se.Code == sqlite3.ErrLocked) {
		return d.Fail("STORE_BUSY")
	}
	return err
}

func Normalize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}, norm.NFC.String(s))
}
func AddMessage(ctx context.Context, tx *sql.Tx, m d.Message) error {
	var run any
	if m.RunID != "" {
		run = m.RunID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO messages(id,task_id,run_id,role,body,normalized,created_at) VALUES(?,?,?,?,?,?,?)`, m.ID, m.TaskID, run, m.Role, m.Body, Normalize(m.Body), m.At)
	return err
}

type Page struct {
	Items       []d.Message `json:"items"`
	NextCursor  int64       `json:"next_cursor"`
	Interrupted bool        `json:"interrupted,omitempty"`
}

func (s *Store) Messages(ctx context.Context, taskID, query, projectID string, cursor int64, limit int) (Page, error) {
	page := Page{Items: []d.Message{}}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	where := `m.seq>?`
	args := []any{cursor}
	if taskID != "" {
		where += ` AND m.task_id=?`
		args = append(args, taskID)
	}
	if projectID != "" {
		where += ` AND m.task_id IN(SELECT id FROM tasks WHERE project_id=?)`
		args = append(args, projectID)
	}
	if query != "" {
		n := Normalize(query)
		// A quoted FTS5 phrase treats punctuation literally. Double embedded
		// quotes to escape them; NUL is the one byte we leave on the instr path.
		if len([]rune(n)) >= 3 && !strings.ContainsRune(n, '\x00') {
			where += ` AND m.seq IN(SELECT rowid FROM message_fts WHERE message_fts MATCH ?)`
			args = append(args, `"`+strings.ReplaceAll(n, `"`, `""`)+`"`)
		}
		where += ` AND instr(m.normalized,?)>0`
		args = append(args, n)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT m.seq,m.id,m.task_id,coalesce(m.run_id,''),m.role,m.body,m.created_at FROM messages m WHERE `+where+` ORDER BY m.seq LIMIT ?`, args...)
	if err != nil {
		return page, translate(err)
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var m d.Message
		var seq int64
		if err = rows.Scan(&seq, &m.ID, &m.TaskID, &m.RunID, &m.Role, &m.Body, &m.At); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			page.NextCursor = last
			break
		}
		page.Items = append(page.Items, m)
		last = seq
	}
	if err = rows.Err(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			page.Interrupted = true
			return page, nil
		}
		return page, translate(err)
	}
	return page, nil
}
func (s *Store) Events(ctx context.Context, after int64) ([]d.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq,payload FROM audit_events WHERE seq>? ORDER BY seq LIMIT 100`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []d.Event{}
	for rows.Next() {
		var seq int64
		var b []byte
		if err = rows.Scan(&seq, &b); err != nil {
			return nil, err
		}
		var e d.Event
		if err = json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		e.Seq = seq
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) LastSeq(ctx context.Context) int64 {
	var seq int64
	s.db.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM audit_events`).Scan(&seq)
	return seq
}
func (s *Store) Backup(ctx context.Context, path string) error {
	s.writer.Lock()
	defer s.writer.Unlock()
	if !filepath.IsAbs(path) {
		return errors.New("absolute backup path required")
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
