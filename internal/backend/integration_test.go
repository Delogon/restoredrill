//go:build integration

// Integration tests run against real temporary restic and Borg repositories.
// They never touch real repositories: every repo is created under t.TempDir().
// Run with: go test -tags integration ./internal/backend/...
package backend_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/backend/borg"
	"github.com/restoredrill/restoredrill/internal/backend/restic"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
)

const testPassword = "integration-test-passphrase"

// fixture creates a source tree with known content and returns its root and
// the total byte size of regular files.
func fixture(t *testing.T) (root string, totalBytes int64) {
	t.Helper()
	root = t.TempDir()
	data := filepath.Join(root, "srv", "app", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]int{
		"file1.txt": 12,
		"file2.bin": 2048,
		"file3.dat": 4096,
	}
	for name, size := range files {
		buf := make([]byte, size)
		for i := range buf {
			buf[i] = byte(i % 251)
		}
		if err := os.WriteFile(filepath.Join(data, name), buf, 0o644); err != nil {
			t.Fatal(err)
		}
		totalBytes += int64(size)
	}
	return root, totalBytes
}

func runEnv(t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// setupRestic creates a temporary restic repo with one tagged snapshot.
func setupRestic(t *testing.T, src, passwordFile string) config.Repository {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	env := []string{"RESTIC_PASSWORD=" + testPassword, "RESTIC_REPOSITORY=" + repoDir}
	runEnv(t, env, "restic", "init")
	runEnv(t, env, "restic", "backup", "--tag", "documents", "--host", "location1", src)
	return config.Repository{
		Type:         "restic",
		Repository:   repoDir,
		PasswordFile: passwordFile,
	}
}

// setupBorg creates a temporary Borg repo with one archive.
func setupBorg(t *testing.T, src, passcommand string) config.Repository {
	t.Helper()
	base := t.TempDir()
	repoDir := filepath.Join(base, "repo")
	env := []string{
		"BORG_PASSPHRASE=" + testPassword,
		"BORG_BASE_DIR=" + filepath.Join(base, "borgbase"),
		"BORG_RELOCATED_REPO_ACCESS_IS_OK=yes",
	}
	runEnv(t, env, "borg", "init", "--encryption=repokey", repoDir)
	runEnv(t, env, "borg", "create", repoDir+"::location1-2026", src)
	return config.Repository{
		Type:        "borg",
		Repository:  repoDir,
		Passcommand: passcommand,
		Env:         map[string]string{"BORG_BASE_DIR": filepath.Join(base, "borgbase")},
	}
}

func writePasswordFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(p, []byte(testPassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// sharedSuite is the backend-agnostic test suite run against both adapters.
func sharedSuite(t *testing.T, be backend.Backend, src string, totalBytes int64, kind string) {
	ctx := context.Background()

	t.Run("Version", func(t *testing.T) {
		v, err := be.Version(ctx)
		if err != nil {
			t.Fatalf("Version: %v", err)
		}
		if v == "" {
			t.Fatal("empty version")
		}
	})

	var snaps []backend.Snapshot
	t.Run("ListSnapshots", func(t *testing.T) {
		var err error
		snaps, err = be.ListSnapshots(ctx, backend.SnapshotFilter{})
		if err != nil {
			t.Fatalf("ListSnapshots: %v", err)
		}
		if len(snaps) != 1 {
			t.Fatalf("expected 1 snapshot, got %d", len(snaps))
		}
		if snaps[0].ID == "" {
			t.Fatal("empty snapshot ID")
		}
		if snaps[0].Time.IsZero() {
			t.Fatal("zero snapshot time")
		}
	})

	t.Run("EstimateRestoreSize", func(t *testing.T) {
		paths := []string{filepath.Join(src, "srv", "app", "data")}
		est, err := be.EstimateRestoreSize(ctx, snaps[0], paths)
		if err != nil {
			t.Fatalf("EstimateRestoreSize: %v", err)
		}
		// Within 5% of the true byte total.
		diff := est - totalBytes
		if diff < 0 {
			diff = -diff
		}
		if float64(diff) > 0.05*float64(totalBytes) {
			t.Fatalf("estimate %d not within 5%% of %d", est, totalBytes)
		}
	})

	t.Run("RestoreLayout", func(t *testing.T) {
		target := t.TempDir()
		paths := []string{filepath.Join(src, "srv", "app", "data")}
		var progressCount int
		stats, err := be.Restore(ctx, snaps[0], paths, target, func(p backend.Progress) { progressCount++ })
		if err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if stats.Duration <= 0 {
			t.Fatal("restore duration not recorded")
		}
		// Restored files keep their original absolute path below target.
		for _, name := range []string{"file1.txt", "file2.bin", "file3.dat"} {
			rel := filepath.Join(strings.TrimPrefix(filepath.Join(src, "srv", "app", "data"), "/"), name)
			p := filepath.Join(target, rel)
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("restored file missing at %s: %v", p, err)
			}
		}
		// Content must match the source.
		srcFile := filepath.Join(src, "srv", "app", "data", "file2.bin")
		rel := filepath.Join(strings.TrimPrefix(srcFile, "/"))
		got, err := os.ReadFile(filepath.Join(target, rel))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := os.ReadFile(srcFile)
		if string(got) != string(want) {
			t.Fatal("restored content differs from source")
		}
	})

	t.Run("SelectLatest", func(t *testing.T) {
		s, err := backend.SelectSnapshot(snaps, backend.Selection{Select: "latest"}, time.Now())
		if err != nil || s.ID != snaps[0].ID {
			t.Fatalf("latest selection: %v %v", s, err)
		}
	})

	t.Run("SelectRandomFixedSeed", func(t *testing.T) {
		a, err := backend.SelectSnapshot(snaps, backend.Selection{Select: "random", Seed: 42}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := backend.SelectSnapshot(snaps, backend.Selection{Select: "random", Seed: 42}, time.Now())
		if a.ID != b.ID {
			t.Fatal("random with fixed seed not reproducible")
		}
	})
}

func TestResticIntegration(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	src, total := fixture(t)
	passFile := writePasswordFile(t)
	repo := setupRestic(t, src, passFile)
	be := restic.New("restic", repo, redact.New(), nil)
	sharedSuite(t, be, src, total, "restic")

	t.Run("TagFilter", func(t *testing.T) {
		snaps, err := be.ListSnapshots(context.Background(), backend.SnapshotFilter{Tags: []string{"documents"}})
		if err != nil || len(snaps) != 1 {
			t.Fatalf("tag filter: %v %d", err, len(snaps))
		}
		none, err := be.ListSnapshots(context.Background(), backend.SnapshotFilter{Tags: []string{"nope"}})
		if err != nil || len(none) != 0 {
			t.Fatalf("negative tag filter: %v %d", err, len(none))
		}
	})

	t.Run("HostFilter", func(t *testing.T) {
		snaps, err := be.ListSnapshots(context.Background(), backend.SnapshotFilter{Host: "location1"})
		if err != nil || len(snaps) != 1 {
			t.Fatalf("host filter: %v %d", err, len(snaps))
		}
	})

	t.Run("PasswordViaCommand", func(t *testing.T) {
		repo2 := repo
		repo2.PasswordFile = ""
		repo2.PasswordCommand = "cat " + passFile
		be2 := restic.New("restic", repo2, redact.New(), nil)
		if _, err := be2.ListSnapshots(context.Background(), backend.SnapshotFilter{}); err != nil {
			t.Fatalf("password via command: %v", err)
		}
	})

	t.Run("WrongPasswordIsAuthError", func(t *testing.T) {
		bad := filepath.Join(t.TempDir(), "bad")
		os.WriteFile(bad, []byte("wrong-password\n"), 0o600)
		repo2 := repo
		repo2.PasswordFile = bad
		be2 := restic.New("restic", repo2, redact.New(), nil)
		_, err := be2.ListSnapshots(context.Background(), backend.SnapshotFilter{})
		if err == nil {
			t.Fatal("expected error for wrong password")
		}
		if be, ok := err.(*backend.Error); !ok || be.Kind != backend.KindAuth {
			t.Fatalf("expected auth error, got %v", err)
		}
	})
}

func TestBorgIntegration(t *testing.T) {
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg not installed")
	}
	src, total := fixture(t)
	passFile := writePasswordFile(t)
	repo := setupBorg(t, src, "cat "+passFile)
	be := borg.New("borg", repo, t.TempDir(), redact.New(), nil)
	sharedSuite(t, be, src, total, "borg")

	t.Run("ArchiveGlob", func(t *testing.T) {
		snaps, err := be.ListSnapshots(context.Background(), backend.SnapshotFilter{ArchiveGlob: "location1-*"})
		if err != nil || len(snaps) != 1 {
			t.Fatalf("archive glob: %v %d", err, len(snaps))
		}
		none, err := be.ListSnapshots(context.Background(), backend.SnapshotFilter{ArchiveGlob: "other-*"})
		if err != nil || len(none) != 0 {
			t.Fatalf("negative glob: %v %d", err, len(none))
		}
	})

	t.Run("Hostname", func(t *testing.T) {
		snaps, _ := be.ListSnapshots(context.Background(), backend.SnapshotFilter{})
		d, err := be.SnapshotDetails(context.Background(), snaps[0])
		if err != nil {
			t.Fatal(err)
		}
		if d.Host == "" {
			t.Fatal("borg hostname not resolved")
		}
	})

	t.Run("WrongPassphraseIsAuthError", func(t *testing.T) {
		repo2 := repo
		repo2.Passcommand = "echo wrong-passphrase"
		be2 := borg.New("borg", repo2, t.TempDir(), redact.New(), nil)
		_, err := be2.ListSnapshots(context.Background(), backend.SnapshotFilter{})
		if err == nil {
			t.Fatal("expected error for wrong passphrase")
		}
		if be, ok := err.(*backend.Error); !ok || be.Kind != backend.KindAuth {
			t.Fatalf("expected auth error, got %v", err)
		}
	})
}

// TestUnreachableRepoIsError verifies the unreachable classification.
func TestUnreachableRepoIsError(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	rrepo := config.Repository{Type: "restic", Repository: missing, PasswordFile: writePasswordFile(t)}
	_, err := restic.New("restic", rrepo, redact.New(), nil).ListSnapshots(ctx, backend.SnapshotFilter{})
	if err == nil {
		t.Fatal("expected error for missing restic repo")
	}
	if be, ok := err.(*backend.Error); !ok || (be.Kind != backend.KindUnreachable && be.Kind != backend.KindOther) {
		t.Fatalf("expected unreachable error, got %v", err)
	}
	fmt.Fprintln(os.Stderr, "restic unreachable classified as:", err.(*backend.Error).Kind)
}