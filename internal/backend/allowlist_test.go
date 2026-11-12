package backend_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/backend/borg"
	"github.com/restoredrill/restoredrill/internal/backend/restic"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
)

// TestCheckAllowedRejectsWrites asserts that no write command can pass the
// allowlist for either backend. This is the core read-only guarantee.
func TestCheckAllowedRejectsWrites(t *testing.T) {
	writes := []string{
		"backup", "init", "forget", "prune", "compact", "check", "delete",
		"remove", "create", "rename", "recreate", "export-tar", "import-tar",
		"key", "migrate", "upgrade", "config", "serve", "mount", "copy",
	}
	for _, kind := range []string{"restic", "borg"} {
		for _, w := range writes {
			if err := backend.CheckAllowed(kind, []string{w, "--json"}); err == nil {
				t.Errorf("%s: write command %q passed the allowlist", kind, w)
			}
		}
	}
}

func TestCheckAllowedAcceptsReads(t *testing.T) {
	cases := map[string][]string{
		"restic": {"version", "snapshots", "ls", "stats", "restore"},
		"borg":   {"--version", "list", "info", "extract"},
	}
	for kind, cmds := range cases {
		for _, c := range cmds {
			if err := backend.CheckAllowed(kind, []string{c}); err != nil {
				t.Errorf("%s: read command %q rejected: %v", kind, c, err)
			}
		}
	}
}

// recordingBin writes a fake backend binary that appends each invocation's
// argv to a log file and emits minimal valid JSON so adapters proceed.
func recordingBin(t *testing.T, kind string) (bin string, argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv.log")
	bin = filepath.Join(dir, kind)
	var body string
	if kind == "restic" {
		body = `#!/bin/sh
printf '%s\n' "$*" >> "` + argvLog + `"
case "$1" in
  version) echo '{"message_type":"version","version":"0.19.1"}' ;;
  snapshots) echo '[{"id":"abc123","time":"2026-11-12T10:00:00Z","hostname":"h","tags":["t"],"paths":["/srv"]}]' ;;
  ls) echo '{"message_type":"node","type":"file","path":"/srv/f","size":10}' ;;
  stats) echo '{"total_size":10,"total_file_count":1}' ;;
  restore) echo '{"message_type":"summary","total_bytes":10,"files_restored":1}' ;;
esac
exit 0
`
	} else {
		body = `#!/bin/sh
printf '%s\n' "$*" >> "` + argvLog + `"
case "$1" in
  --version) echo 'borg 1.4.0' ;;
  list)
    if echo "$*" | grep -q -- '--json-lines'; then
      echo '{"type":"-","path":"srv/f","size":10}'
    else
      echo '{"archives":[{"name":"a-1","id":"x","time":"2026-11-12T10:00:00.000000"}]}'
    fi ;;
  info) echo '{"archives":[{"hostname":"h","time":"2026-11-12T10:00:00.000000"}]}' ;;
  extract) echo '{"type":"progress_percent","current":10,"total":10,"finished":true}' ;;
esac
exit 0
`
	}
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvLog
}

// TestAdapterArgvAllowlist drives every adapter method against a recording
// fake binary and asserts each recorded invocation is in the allowlist.
func TestAdapterArgvAllowlist(t *testing.T) {
	ctx := context.Background()
	r := redact.New()
	snap := backend.Snapshot{ID: "abc123"}

	for _, kind := range []string{"restic", "borg"} {
		bin, argvLog := recordingBin(t, kind)
		repo := config.Repository{Type: kind, Repository: "/tmp/repo", PasswordFile: "/tmp/p"}
		var be backend.Backend
		if kind == "restic" {
			be = restic.New(bin, repo, r, nil)
		} else {
			repo.Passcommand = "cat /tmp/p"
			repo.PasswordFile = ""
			be = borg.New(bin, repo, t.TempDir(), r, nil)
		}

		if _, err := be.Version(ctx); err != nil {
			t.Fatalf("%s Version: %v", kind, err)
		}
		if _, err := be.ListSnapshots(ctx, backend.SnapshotFilter{}); err != nil {
			t.Fatalf("%s ListSnapshots: %v", kind, err)
		}
		if _, err := be.EstimateRestoreSize(ctx, snap, []string{"/srv"}); err != nil {
			t.Fatalf("%s EstimateRestoreSize: %v", kind, err)
		}
		if _, err := be.Restore(ctx, snap, []string{"/srv"}, t.TempDir(), nil); err != nil {
			t.Fatalf("%s Restore: %v", kind, err)
		}
		if d, ok := be.(backend.SnapshotDetailer); ok {
			if _, err := d.SnapshotDetails(ctx, snap); err != nil {
				t.Fatalf("%s SnapshotDetails: %v", kind, err)
			}
		}

		data, err := os.ReadFile(argvLog)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) == 0 {
			t.Fatalf("%s: no invocations recorded", kind)
		}
		for _, line := range lines {
			args := strings.Fields(line)
			if err := backend.CheckAllowed(kind, args); err != nil {
				t.Errorf("%s: invocation not allowed: %q -> %v", kind, line, err)
			}
		}
	}
}

// TestSecretsNeverInArgv asserts unlock material is passed via environment,
// never as command-line arguments.
func TestSecretsNeverInArgv(t *testing.T) {
	ctx := context.Background()
	r := redact.New()
	for _, kind := range []string{"restic", "borg"} {
		bin, argvLog := recordingBin(t, kind)
		repo := config.Repository{Type: kind, Repository: "/tmp/repo"}
		if kind == "restic" {
			repo.PasswordFile = "/tmp/secret-file"
			repo.PasswordCommand = "cat /tmp/secret-file"
		} else {
			repo.Passcommand = "cat /tmp/secret-file"
		}
		var be backend.Backend
		if kind == "restic" {
			be = restic.New(bin, repo, r, nil)
		} else {
			be = borg.New(bin, repo, t.TempDir(), r, nil)
		}
		_, _ = be.ListSnapshots(ctx, backend.SnapshotFilter{})
		data, _ := os.ReadFile(argvLog)
		if strings.Contains(string(data), "secret-file") && strings.Contains(string(data), "--password") {
			t.Errorf("%s: secret material appeared in argv: %s", kind, data)
		}
		// The literal flag forms must never be present.
		for _, bad := range []string{"--password ", "--passphrase ", "--password=", "--passphrase="} {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s: argv contains %q: %s", kind, bad, data)
			}
		}
	}
}