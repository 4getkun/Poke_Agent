package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
)

// ValidateSnapshot opens an existing standalone backup read-only. It never
// creates a database, applies migrations, or ignores an outstanding WAL.
func ValidateSnapshot(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute snapshot path required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("INVALID_DATABASE_SNAPSHOT")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !os.IsNotExist(err) {
			return errors.New("SNAPSHOT_NOT_STANDALONE")
		}
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	db, err := sql.Open("sqlite3", u.String()+"?mode=ro&_query_only=1&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := validateSnapshotSchema(ctx, db); err != nil {
		return err
	}
	var version int
	var checksum string
	if err := db.QueryRowContext(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &checksum); err != nil {
		return errors.New("INVALID_DATABASE_SCHEMA")
	}
	hash := sha256.Sum256([]byte(schema()))
	if version != 1 || checksum != hex.EncodeToString(hash[:]) {
		return errors.New("UNSUPPORTED_SCHEMA")
	}
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	checks := 0
	for rows.Next() {
		var result string
		if rows.Scan(&result) != nil || result != "ok" {
			rows.Close()
			return errors.New("DATABASE_INTEGRITY_FAILED")
		}
		checks++
	}
	err = rows.Err()
	rows.Close()
	if err != nil || checks != 1 {
		return errors.New("DATABASE_INTEGRITY_FAILED")
	}
	rows, err = db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := rows.Next()
	err = rows.Err()
	rows.Close()
	if invalid || err != nil {
		return errors.New("DATABASE_FOREIGN_KEY_FAILED")
	}
	if _, err := load(ctx, db); err != nil {
		return errors.New("INVALID_DATABASE_PAYLOAD")
	}
	var matches int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM message_fts WHERE message_fts MATCH '"snapshot_probe"'`).Scan(&matches); err != nil {
		return errors.New("DATABASE_SEARCH_UNAVAILABLE")
	}
	return nil
}

// The migration checksum alone is a declaration inside the selected file.
// Compare its actual tables/indexes/triggers/views to a fresh supported schema
// before adopting it, including FTS shadow tables and rejecting extra objects.
func validateSnapshotSchema(ctx context.Context, db *sql.DB) error {
	expected, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return err
	}
	defer expected.Close()
	expected.SetMaxOpenConns(1)
	if _, err = expected.ExecContext(ctx, migrationTableSQL+";"+schema()); err != nil {
		return err
	}
	read := func(database *sql.DB) (map[string][3]string, error) {
		rows, err := database.QueryContext(ctx, `SELECT name,type,tbl_name,sql FROM sqlite_master WHERE name NOT GLOB 'sqlite_*' AND sql IS NOT NULL`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		objects := map[string][3]string{}
		for rows.Next() {
			var name, kind, table, definition string
			if err := rows.Scan(&name, &kind, &table, &definition); err != nil {
				return nil, err
			}
			objects[name] = [3]string{kind, table, definition}
		}
		return objects, rows.Err()
	}
	want, err := read(expected)
	if err != nil {
		return err
	}
	got, err := read(db)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return errors.New("INVALID_DATABASE_SCHEMA")
	}
	return nil
}
