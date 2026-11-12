package checks

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
	_ "modernc.org/sqlite"
)

// makeSQLite creates a real SQLite database with a users table.
func makeSQLite(t *testing.T, dir, rel string, rows int) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		if _, err := db.Exec(`INSERT INTO users (name) VALUES (?)`, "user"); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestSQLiteIntegrityPass(t *testing.T) {
	dir := t.TempDir()
	makeSQLite(t, dir, "srv/app/db.sqlite3", 3)
	c := &Context{RestoreDir: dir}
	ch, _ := Get("sqlite_integrity")
	cfg := config.Check{Type: "sqlite_integrity", Path: "srv/app/db.sqlite3"}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestSQLiteIntegrityTruncatedFails(t *testing.T) {
	dir := t.TempDir()
	p := makeSQLite(t, dir, "srv/app/db.sqlite3", 3)
	// Truncate the file to simulate a broken backup.
	if err := os.Truncate(p, 100); err != nil {
		t.Fatal(err)
	}
	c := &Context{RestoreDir: dir}
	ch, _ := Get("sqlite_integrity")
	cfg := config.Check{Type: "sqlite_integrity", Path: "srv/app/db.sqlite3"}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for truncated db, got %+v", r)
	}
}

func TestSQLiteIntegrityMissingFails(t *testing.T) {
	c := &Context{RestoreDir: t.TempDir()}
	ch, _ := Get("sqlite_integrity")
	cfg := config.Check{Type: "sqlite_integrity", Path: "nope.sqlite3"}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for missing db, got %+v", r)
	}
}

func TestSQLiteQuery(t *testing.T) {
	dir := t.TempDir()
	makeSQLite(t, dir, "db.sqlite3", 5)
	c := &Context{RestoreDir: dir}
	ch, _ := Get("sqlite_query")
	min := 1.0
	cfg := config.Check{Type: "sqlite_query", Path: "db.sqlite3", Query: "SELECT count(*) FROM users", Expect: config.Expect{Min: &min}}
	// Note: Expect built in Go has no keys map; min/max still apply.
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
	min = 100.0
	cfg.Expect.Min = &min
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for count below min, got %+v", r)
	}
}

func TestSQLiteQueryTruncatedFails(t *testing.T) {
	dir := t.TempDir()
	p := makeSQLite(t, dir, "db.sqlite3", 5)
	os.Truncate(p, 50)
	c := &Context{RestoreDir: dir}
	ch, _ := Get("sqlite_query")
	cfg := config.Check{Type: "sqlite_query", Path: "db.sqlite3", Query: "SELECT count(*) FROM users"}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for truncated db, got %+v", r)
	}
}

func TestPostgresRestoreNoRuntimeErrors(t *testing.T) {
	c := &Context{RestoreDir: t.TempDir(), Runtime: nil}
	ch, _ := Get("postgres_restore")
	cfg := config.Check{Type: "postgres_restore", Dump: "db.dump"}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Error {
		t.Fatalf("expected error without runtime, got %+v", r)
	}
}