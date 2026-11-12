package checks

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/restoredrill/restoredrill/internal/config"
	_ "modernc.org/sqlite"
)

// openSQLiteReadOnly opens a restored SQLite database strictly read-only so a
// check can never modify the restored data.
func openSQLiteReadOnly(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Force a real connection so open errors surface now.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func resolveDBPath(c *Context, rel string) (string, error) {
	p := filepath.Join(c.RestoreDir, filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("database file %s not found in the restore", rel)
	}
	return p, nil
}

// sqliteIntegrity runs PRAGMA integrity_check and expects "ok".
type sqliteIntegrity struct{}

func init() { Register(sqliteIntegrity{}) }

func (sqliteIntegrity) Type() string  { return "sqlite_integrity" }
func (sqliteIntegrity) Phase() string { return "data" }

func (sqliteIntegrity) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	path, err := resolveDBPath(c, cfg.Path)
	if err != nil {
		return failf("sqlite_integrity: %v", err)
	}
	db, err := openSQLiteReadOnly(path)
	if err != nil {
		return failf("sqlite_integrity: cannot open %s (file may be truncated or corrupt): %v", cfg.Path, err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return failf("sqlite_integrity: integrity_check failed on %s: %v", cfg.Path, err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return failf("sqlite_integrity: cannot read integrity_check result: %v", err)
		}
		if s != "ok" {
			problems = append(problems, s)
		}
	}
	if err := rows.Err(); err != nil {
		return failf("sqlite_integrity: %v", err)
	}
	if len(problems) > 0 {
		return Result{Status: Fail, Message: fmt.Sprintf("sqlite_integrity: %s is corrupt: %v", cfg.Path, problems), Details: map[string]any{"problems": problems}}
	}
	return passf("sqlite_integrity: %s passed integrity_check", cfg.Path)
}