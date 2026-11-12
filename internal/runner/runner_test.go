//go:build integration

package runner

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/store"
	"gopkg.in/yaml.v3"
)

const pw = "runner-test-pass"

// setupRepo creates a fixture tree and a restic repository backing it up.
// It returns the source root and repository path.
func setupRepo(t *testing.T) (src, repoPath string) {
	t.Helper()
	base := t.TempDir()
	src = filepath.Join(base, "src")
	data := filepath.Join(src, "srv", "app", "data")
	os.MkdirAll(data, 0o755)
	for i := 0; i < 6; i++ {
		os.WriteFile(filepath.Join(data, string(rune('a'+i))+".pdf"), make([]byte, 100+i), 0o644)
	}
	repoPath = filepath.Join(base, "repo")
	env := append(os.Environ(), "RESTIC_PASSWORD="+pw, "RESTIC_REPOSITORY="+repoPath)
	run := func(args ...string) {
		cmd := exec.Command("restic", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("backup", "--tag", "docs", "--host", "location1", data)
	return src, repoPath
}

func testConfig(t *testing.T, repoPath, src, workDir, stateDir string, checksYAML string) *config.Config {
	t.Helper()
	passFile := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(passFile, []byte(pw+"\n"), 0o600)
	cfg := &config.Config{
		Version:  1,
		StateDir: stateDir,
		WorkDir:  workDir,
		Binaries: config.Binaries{Restic: "restic", Borg: "borg"},
		Retention: config.Retention{RunsPerDrill: 50},
		Repositories: map[string]config.Repository{
			"r": {Type: "restic", Repository: repoPath, PasswordFile: passFile},
		},
		Drills: map[string]config.Drill{
			"d": {
				Repository: "r",
				Snapshot:   config.Snapshot{Select: "latest", Tags: []string{"docs"}, MaxAge: config.Duration(24 * time.Hour)},
				Restore:    config.Restore{Paths: []string{filepath.Join(src, "srv", "app", "data")}},
				Timeout:    config.Duration(2 * time.Minute),
			},
		},
		Redactor: redact.New(),
	}
	// Parse checks from YAML snippet. %DATA% expands to the restored relative
	// data path (the source path minus its leading slash).
	d := cfg.Drills["d"]
	relData := strings.TrimPrefix(filepath.Join(src, "srv", "app", "data"), "/")
	snippet := strings.ReplaceAll(checksYAML, "%DATA%", relData)
	d.Checks = parseChecks(t, snippet)
	cfg.Drills["d"] = d
	return cfg
}

func newRunner(t *testing.T, cfg *config.Config) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := New(cfg, st, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), 1)
	return r, st
}

func parseChecks(t *testing.T, snippet string) []config.Check {
	t.Helper()
	var out []config.Check
	if err := yaml.Unmarshal([]byte(snippet), &out); err != nil {
		t.Fatalf("parse checks: %v", err)
	}
	return out
}

func TestRunPass(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
  min_count: 6
`)
	r, st := newRunner(t, cfg)
	run, err := r.Run(context.Background(), "d", RunOptions{Trigger: store.TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusPass {
		t.Fatalf("expected pass, got %s (%s: %s)", run.Status, run.ErrorKind, run.ErrorMessage)
	}
	if run.RestoredFiles != 6 {
		t.Fatalf("restored files = %d, want 6", run.RestoredFiles)
	}
	// Sandbox must be cleaned up.
	if _, err := os.Stat(filepath.Join(cfg.WorkDir, run.ID)); !os.IsNotExist(err) {
		t.Fatalf("sandbox not cleaned up: %v", err)
	}
	results, _ := st.ListCheckResults(run.ID)
	// snapshot_age (implicit) + files_exist
	if len(results) != 2 {
		t.Fatalf("expected 2 check results, got %d", len(results))
	}
}

func TestRunMissingFilesFails(t *testing.T) {
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
  min_count: 100
`)
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status != store.StatusFail {
		t.Fatalf("expected fail for missing files, got %s", run.Status)
	}
}

func TestRunOldSnapshotFails(t *testing.T) {
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
`)
	// Force max_age to 1 nanosecond so the snapshot is always too old.
	d := cfg.Drills["d"]
	d.Snapshot.MaxAge = config.Duration(1)
	cfg.Drills["d"] = d
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status != store.StatusFail {
		t.Fatalf("expected fail for old snapshot, got %s", run.Status)
	}
}

func TestRunWrongPasswordErrors(t *testing.T) {
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
`)
	// Point at a wrong password file.
	bad := filepath.Join(t.TempDir(), "bad")
	os.WriteFile(bad, []byte("wrong\n"), 0o600)
	repoCfg := cfg.Repositories["r"]
	repoCfg.PasswordFile = bad
	cfg.Repositories["r"] = repoCfg
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status != store.StatusError {
		t.Fatalf("expected error for wrong password, got %s", run.Status)
	}
	if run.ErrorKind != "auth" {
		t.Fatalf("expected auth error kind, got %q", run.ErrorKind)
	}
}

func TestRunUnreachableRepoErrors(t *testing.T) {
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
`)
	repoCfg := cfg.Repositories["r"]
	repoCfg.Repository = filepath.Join(base, "does-not-exist")
	cfg.Repositories["r"] = repoCfg
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status != store.StatusError {
		t.Fatalf("expected error for unreachable repo, got %s", run.Status)
	}
}

func TestRunCorruptRepoNeverPass(t *testing.T) {
	src, repo := setupRepo(t)
	// Corrupt a pack file to break the repository. restic marks packs 0400,
	// so make it writable first; otherwise the corruption silently no-ops.
	packs, _ := filepath.Glob(filepath.Join(repo, "data", "*", "*"))
	if len(packs) == 0 {
		t.Skip("no pack files found")
	}
	if err := os.Chmod(packs[0], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packs[0], []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
`)
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status == store.StatusPass {
		t.Fatalf("corrupt repository must never pass, got pass")
	}
}

func TestRunSkippedWhenActive(t *testing.T) {
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
`)
	r, _ := newRunner(t, cfg)
	// Hold the drill lock to simulate an active run.
	lock := r.drillLock("d")
	lock.Lock()
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	lock.Unlock()
	if run.Status != store.StatusSkipped {
		t.Fatalf("expected skipped, got %s", run.Status)
	}
}

func TestKeepOnFailure(t *testing.T) {
	src, repo := setupRepo(t)
	base := t.TempDir()
	cfg := testConfig(t, repo, src, filepath.Join(base, "work"), filepath.Join(base, "state"), `
- type: files_exist
  paths: ['%DATA%/**/*.pdf']
  min_count: 100
`)
	d := cfg.Drills["d"]
	keep := true
	d.KeepOnFailure = &keep
	cfg.Drills["d"] = d
	r, _ := newRunner(t, cfg)
	run, _ := r.Run(context.Background(), "d", RunOptions{})
	if run.Status != store.StatusFail {
		t.Fatalf("expected fail, got %s", run.Status)
	}
	if run.KeptPath == "" {
		t.Fatal("expected kept sandbox path on failure")
	}
	if _, err := os.Stat(run.KeptPath); err != nil {
		t.Fatalf("kept sandbox missing: %v", err)
	}
}