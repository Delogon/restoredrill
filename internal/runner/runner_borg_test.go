//go:build integration

package runner

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/store"
	_ "modernc.org/sqlite"
)

// setupBorgRepo creates a fixture containing a SQLite database and backs it up
// into a temporary Borg repository.
func setupBorgRepo(t *testing.T) (src, repoPath, base string) {
	t.Helper()
	base = t.TempDir()
	src = filepath.Join(base, "src")
	data := filepath.Join(src, "srv", "app")
	os.MkdirAll(data, 0o755)
	dbPath := filepath.Join(data, "db.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		db.Exec(`INSERT INTO users (name) VALUES (?)`, "u")
	}
	db.Close()

	repoPath = filepath.Join(base, "repo")
	borgBase := filepath.Join(base, "borgbase")
	env := append(os.Environ(),
		"BORG_PASSPHRASE="+pw,
		"BORG_BASE_DIR="+borgBase,
		"BORG_RELOCATED_REPO_ACCESS_IS_OK=yes",
	)
	run := func(args ...string) {
		cmd := exec.Command("borg", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("borg %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--encryption=repokey", repoPath)
	run("create", repoPath+"::location1-1", data)
	return src, repoPath, base
}

func borgConfig(t *testing.T, repoPath, src, base, checksYAML string) *config.Config {
	t.Helper()
	passFile := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(passFile, []byte(pw+"\n"), 0o600)
	cfg := &config.Config{
		Version:  1,
		StateDir: filepath.Join(base, "state"),
		WorkDir:  filepath.Join(base, "work"),
		Binaries: config.Binaries{Restic: "restic", Borg: "borg"},
		Retention: config.Retention{RunsPerDrill: 50},
		Repositories: map[string]config.Repository{
			"b": {Type: "borg", Repository: repoPath, Passcommand: "cat " + passFile,
				Env: map[string]string{"BORG_BASE_DIR": filepath.Join(base, "borgbase")}},
		},
		Drills: map[string]config.Drill{
			"d": {
				Repository: "b",
				Snapshot:   config.Snapshot{Select: "latest", ArchiveGlob: "location1-*", MaxAge: config.Duration(24 * time.Hour)},
				Restore:    config.Restore{Paths: []string{filepath.Join(src, "srv", "app")}},
				Timeout:    config.Duration(2 * time.Minute),
			},
		},
		Redactor: redact.New(),
	}
	d := cfg.Drills["d"]
	relData := strings.TrimPrefix(filepath.Join(src, "srv", "app"), "/")
	d.Checks = parseChecks(t, strings.ReplaceAll(checksYAML, "%DATA%", relData))
	cfg.Drills["d"] = d
	return cfg
}

func TestRunBorgSQLitePass(t *testing.T) {
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg not installed")
	}
	src, repo, base := setupBorgRepo(t)
	cfg := borgConfig(t, repo, src, base, `
- type: sqlite_integrity
  path: '%DATA%/db.sqlite3'
- type: sqlite_query
  path: '%DATA%/db.sqlite3'
  query: SELECT count(*) FROM users
  expect: { min: 7 }
`)
	r, _ := newRunner(t, cfg)
	run, err := r.Run(context.Background(), "d", RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusPass {
		t.Fatalf("expected pass, got %s (%s: %s)", run.Status, run.ErrorKind, run.ErrorMessage)
	}
	if run.Backend != "borg" {
		t.Fatalf("backend = %q, want borg", run.Backend)
	}
}

func TestRunBorgSQLiteQueryFails(t *testing.T) {
	src, repo, base := setupBorgRepo(t)
	cfg := borgConfig(t, repo, src, base, `
- type: sqlite_query
  path: '%DATA%/db.sqlite3'
  query: SELECT count(*) FROM users
  expect: { min: 1000 }
`)
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status != store.StatusFail {
		t.Fatalf("expected fail for count below min, got %s", run.Status)
	}
}