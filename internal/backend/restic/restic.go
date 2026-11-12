// Package restic implements the backend adapter for restic repositories.
// It is strictly read-only: only version, snapshots, ls, stats and restore
// commands are ever built (enforced by backend.CheckAllowed).
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
)

// MinVersion is the minimum supported restic version: the first release with
// JSON output for restore (0.17.0). CI pins a newer version.
const MinVersion = "0.17.0"

// Adapter talks to one restic repository.
type Adapter struct {
	Bin      string
	Repo     config.Repository
	Redactor *redact.Redactor
	Log      func(line string)
}

// New builds an adapter for a repository config.
func New(bin string, repo config.Repository, r *redact.Redactor, log func(string)) *Adapter {
	return &Adapter{Bin: bin, Repo: repo, Redactor: r, Log: log}
}

// Kind implements backend.Backend.
func (a *Adapter) Kind() string { return "restic" }

func (a *Adapter) runner() *backend.CmdRunner {
	env := []string{"RESTIC_REPOSITORY=" + a.Repo.Repository}
	switch {
	case a.Repo.PasswordFile != "":
		env = append(env, "RESTIC_PASSWORD_FILE="+a.Repo.PasswordFile)
	case a.Repo.PasswordCommand != "":
		env = append(env, "RESTIC_PASSWORD_COMMAND="+a.Repo.PasswordCommand)
	case a.Repo.Password != "":
		env = append(env, "RESTIC_PASSWORD="+a.Repo.Password)
	}
	for k, v := range a.Repo.Env {
		env = append(env, k+"="+v)
	}
	return &backend.CmdRunner{Kind: "restic", Bin: a.Bin, Env: env, Redactor: a.Redactor, Log: a.Log}
}

// globalFlags are appended to every command. --no-lock supports read-only
// storage at the documented risk of racing with prune.
func (a *Adapter) globalFlags() []string {
	var f []string
	if a.Repo.NoLock {
		f = append(f, "--no-lock")
	}
	return f
}

// Version returns the restic version string and rejects unsupported versions.
func (a *Adapter) Version(ctx context.Context) (string, error) {
	var version string
	stderr, code, err := a.runner().RunJSONLines(ctx, append([]string{"version", "--json"}, a.globalFlags()...), func(line []byte) error {
		var v struct {
			Version string `json:"version"`
		}
		if e := json.Unmarshal(line, &v); e != nil {
			return nil // tolerant: ignore unknown shapes
		}
		if v.Version != "" {
			version = v.Version
		}
		return nil
	})
	if err != nil {
		return "", classify("version", stderr, code, err)
	}
	if version == "" {
		return "", &backend.Error{Kind: backend.KindVersion, Message: "could not parse restic version output"}
	}
	if compareVersions(version, MinVersion) < 0 {
		return version, &backend.Error{Kind: backend.KindVersion, Message: fmt.Sprintf("restic %s is too old — install restic >= %s (JSON restore output is required)", version, MinVersion)}
	}
	return version, nil
}

// snapshotJSON is the restic snapshots --json shape (unknown fields ignored).
type snapshotJSON struct {
	ID       string   `json:"id"`
	ShortID  string   `json:"short_id"`
	Time     string   `json:"time"`
	Hostname string   `json:"hostname"`
	Tags     []string `json:"tags"`
	Paths    []string `json:"paths"`
}

// ListSnapshots lists snapshots, applying filters client-side for
// deterministic semantics across restic versions.
func (a *Adapter) ListSnapshots(ctx context.Context, f backend.SnapshotFilter) ([]backend.Snapshot, error) {
	var snaps []backend.Snapshot
	args := append([]string{"snapshots", "--json"}, a.globalFlags()...)
	stdout, stderr, code, err := a.runner().RunCapture(ctx, args)
	if err != nil {
		return nil, classify("snapshots", stderr, code, err)
	}
	var arr []snapshotJSON
	if e := json.Unmarshal(bytes.TrimSpace(stdout), &arr); e != nil {
		return nil, &backend.Error{Kind: backend.KindOther, Message: fmt.Sprintf("restic snapshots: cannot parse JSON output: %v", e), Err: e}
	}
	for _, s := range arr {
		if s.ID == "" {
			return nil, &backend.Error{Kind: backend.KindOther, Message: "restic snapshot entry missing required field id"}
		}
		snaps = append(snaps, convertSnapshot(s))
	}
	var out []backend.Snapshot
	for _, s := range snaps {
		if f.Host != "" && s.Host != f.Host {
			continue
		}
		if len(f.Tags) > 0 && !hasAllTags(s.Tags, f.Tags) {
			continue
		}
		if len(f.Paths) > 0 {
			ok := true
			for _, p := range f.Paths {
				if !pathMatches(s.Paths, p) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, s)
	}
	backend.SortSnapshots(out)
	return out, nil
}

func convertSnapshot(s snapshotJSON) backend.Snapshot {
	t, _ := time.Parse(time.RFC3339Nano, s.Time)
	return backend.Snapshot{ID: s.ID, Time: t, Host: s.Hostname, Tags: s.Tags, Paths: s.Paths}
}

// EstimateRestoreSize sums file sizes from restic ls for the selected paths.
func (a *Adapter) EstimateRestoreSize(ctx context.Context, s backend.Snapshot, paths []string) (int64, error) {
	args := append([]string{"ls", "--json", s.ID}, paths...)
	args = append(args, a.globalFlags()...)
	var total int64
	stderr, code, err := a.runner().RunJSONLines(ctx, args, func(line []byte) error {
		var node struct {
			MessageType string `json:"message_type"`
			Type        string `json:"type"`
			Size        int64  `json:"size"`
		}
		if e := json.Unmarshal(line, &node); e != nil {
			return nil
		}
		if node.MessageType == "node" && node.Type == "file" {
			total += node.Size
		}
		return nil
	})
	if err != nil {
		return 0, classify("ls", stderr, code, err)
	}
	return total, nil
}

// Restore extracts the snapshot into target, preserving absolute paths below
// it. Progress is streamed from restic's JSON output.
func (a *Adapter) Restore(ctx context.Context, s backend.Snapshot, paths []string, target string, progress func(backend.Progress)) (backend.RestoreStats, error) {
	args := []string{"restore", s.ID, "--target", target, "--json"}
	for _, p := range paths {
		args = append(args, "--include", p)
	}
	args = append(args, a.globalFlags()...)
	start := time.Now()
	var stats backend.RestoreStats
	stderr, code, err := a.runner().RunJSONLines(ctx, args, func(line []byte) error {
		var msg struct {
			MessageType   string  `json:"message_type"`
			TotalBytes    int64   `json:"total_bytes"`
			BytesRestored int64   `json:"bytes_restored"`
			FilesRestored int64   `json:"files_restored"`
			PercentDone   float64 `json:"percent_done"`
		}
		if e := json.Unmarshal(line, &msg); e != nil {
			return nil
		}
		switch msg.MessageType {
		case "summary":
			stats.Bytes = msg.TotalBytes
			stats.Files = msg.FilesRestored
		case "progress":
			if progress != nil {
				progress(backend.Progress{BytesDone: msg.BytesRestored, BytesTotal: msg.TotalBytes})
			}
		}
		return nil
	})
	if err != nil {
		return stats, classify("restore", stderr, code, err)
	}
	stats.Duration = time.Since(start)
	return stats, nil
}

// classify maps restic exit codes and stderr to error kinds.
// restic exit codes: 10 = repository does not exist, 11 = locked,
// 12 = wrong password.
func classify(op, stderr string, code int, err error) error {
	lower := strings.ToLower(stderr)
	switch {
	case code == 12 || strings.Contains(lower, "wrong password") || strings.Contains(lower, "incorrect password"):
		return &backend.Error{Kind: backend.KindAuth, Message: fmt.Sprintf("restic %s: wrong repository password — check password_file/password_command", op), Err: err}
	case code == 10 || strings.Contains(lower, "unable to open") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "no such file"):
		return &backend.Error{Kind: backend.KindUnreachable, Message: fmt.Sprintf("restic %s: repository unreachable — check the repository URL, credentials and network", op), Err: err}
	case code == 11 || strings.Contains(lower, "locked"):
		return &backend.Error{Kind: backend.KindLocked, Message: fmt.Sprintf("restic %s: repository is locked — another operation (possibly prune) holds a lock; do not schedule drills alongside prune", op), Err: err}
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") || strings.Contains(lower, "timeout") || strings.Contains(lower, "ssh:"):
		return &backend.Error{Kind: backend.KindUnreachable, Message: fmt.Sprintf("restic %s: repository unreachable — check the repository URL, credentials and network", op), Err: err}
	default:
		msg := strings.TrimSpace(stderr)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return &backend.Error{Kind: backend.KindOther, Message: fmt.Sprintf("restic %s failed: %s", op, firstLines(msg, 3)), Err: err}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " ")
}

func hasAllTags(tags, wanted []string) bool {
	for _, w := range wanted {
		found := false
		for _, t := range tags {
			if t == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func pathMatches(snapPaths []string, filter string) bool {
	for _, p := range snapPaths {
		if p == filter || strings.HasPrefix(p, filter+"/") || strings.HasPrefix(filter, p+"/") {
			return true
		}
	}
	return false
}

// compareVersions compares dotted version strings (ignoring suffixes).
func compareVersions(a, b string) int {
	pa := strings.SplitN(strings.TrimPrefix(a, "v"), ".", 3)
	pb := strings.SplitN(strings.TrimPrefix(b, "v"), ".", 3)
	for i := 0; i < 3; i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(digits(pa[i]))
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(digits(pb[i]))
		}
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
	}
	return 0
}

func digits(s string) string {
	for i, r := range s {
		if r < '0' || r > '9' {
			return s[:i]
		}
	}
	return s
}