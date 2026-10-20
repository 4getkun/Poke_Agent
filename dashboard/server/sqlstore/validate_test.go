package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotRejectsAlteredSchemaWithUnchangedChecksum(t *testing.T) {
	ctx := context.Background()
	for _, alter := range []string{"DROP TRIGGER messages_ai", "CREATE TRIGGER unexpected AFTER INSERT ON tasks BEGIN DELETE FROM notifications; END", "CREATE TABLE sqlitex_extra(x TEXT)"} {
		t.Run(alter, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.db")
			backup := filepath.Join(root, "snapshot.db")
			s, err := Open(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Backup(ctx, backup); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if err := ValidateSnapshot(ctx, backup); err != nil {
				t.Fatalf("valid backup rejected: %v", err)
			}
			raw, err := sql.Open("sqlite3", backup)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(alter); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			before, err := os.ReadFile(backup)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSnapshot(ctx, backup); err == nil || err.Error() != "INVALID_DATABASE_SCHEMA" {
				t.Fatalf("altered schema accepted: %v", err)
			}
			after, err := os.ReadFile(backup)
			if err != nil {
				t.Fatal(err)
			}
			if sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("validation modified selected snapshot")
			}
		})
	}
}
