package sqlstore

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestFailedInitialMigrationRollsBackMetadataAndSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conflicting.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE tasks(sentinel TEXT); INSERT INTO tasks VALUES('keep original')`); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(path); err == nil {
		store.Close()
		t.Fatal("conflicting schema unexpectedly migrated")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('schema_migrations','projects','roles')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration left partial schema, count=%d error=%v", count, err)
	}
	var sentinel string
	if err := db.QueryRow(`SELECT sentinel FROM tasks`).Scan(&sentinel); err != nil || sentinel != "keep original" {
		t.Fatal("failed migration changed pre-existing data")
	}
}
