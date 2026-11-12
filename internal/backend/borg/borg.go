// Package borg implements the backend adapter for Borg 1.x repositories.
// It is strictly read-only: only --version, list, info and extract commands
// are ever built (enforced by backend.CheckAllowed). Borg 2.x is refused.
package borg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
)

// MinVersion is the minimum supported Borg version (1.2). Borg 2.x has a
// different CLI and is refused with a clear message.
const MinVersion = "1.2.0"

// Adapter talks to one Borg repository.
type Adapter struct {
	Bin      string
	Repo     config.Repository
	StateDir string
	Redactor *redact.Redactor
	Log      func(line string)
}

// New builds an adapter for a repository config.
func New(bin string, repo config.Repository, stateDir string, r *redact.Redactor, log func(string)) *Adapter {
	return &Adapter{Bin: bin, Repo: repo, StateDir: stateDir, Redactor: r, Log: log}
}

// Kind implements backend.Backend.
func (a *Adapter) Kind() string { return "borg" }

func (a *Adapter) runner() *backend.CmdRunner {
	env := []string{
		"BORG_RELOCATED_REPO_ACCESS_IS_OK=no",
	}
	if a.StateDir != "" {
		env = append(env, "BORG_BASE_DIR="+a.StateDir+"/borg")
	}
	if a.Repo.Passcommand != "" {
		env = append(env, "BORG_PASSCOMMAND="+a.Repo.Passcommand)
	}
	if a.Repo.Passphrase != "" {
		env = append(env, "BORG_PASSPHRASE="+a.Repo.Passphrase)
	}
	for k, v := range a.Repo.Env {
		env = append(env, k+"="+v)
	}
	return &backend.CmdRunner{Kind: "borg", Bin: a.Bin, Env: env, Redactor: a.Redactor, Log: a.Log}
}

// Version returns the Borg version and refuses 2.x and anything below 1.2.
func (a *Adapter) Version(ctx context.Context) (string, error) {
	stdout, stderr, code, err := a.runner().RunCapture(ctx, []string{"--version"})
	if err != nil {
		return "", classify("--version", stderr, code, err)
	}
	// Output: "borg 1.4.0"
	fields := strings.Fields(string(stdout))
	version := ""
	if len(fields) >= 2 {
		version = fields[1]
	}
	if version == "" {
		return "", &backend.Error{Kind: backend.KindVersion, Message: "could not parse borg version output"}
	}
	if strings.HasPrefix(version, "2.") {
		return version, &backend.Error{Kind: backend.KindVersion, Message: fmt.Sprintf("borg %s is not supported — restoredrill v1 supports Borg 1.2 and 1.4 only", version)}
	}
	if compareVersions(version, MinVersion) < 0 {
		return version, &backend.Error{Kind: backend.KindVersion, Message: fmt.Sprintf("borg %s is too old — install Borg >= %s", version, MinVersion)}
	}
	return version, nil
}

type archivesJSON struct {
	Archives []struct {
		Name string `json:"name"`
		ID   string `json:"id"`
		Time string `json:"time"`
	} `json:"archives"`
}

// ListSnapshots lists archives, filtered by archive_glob and host.
func (a *Adapter) ListSnapshots(ctx context.Context, f backend.SnapshotFilter) ([]backend.Snapshot, error) {
	stdout, stderr, code, err := a.runner().RunCapture(ctx, []string{"list", "--json", a.Repo.Repository})
	if err != nil {
		return nil, classify("list", stderr, code, err)
	}
	var doc archivesJSON
	if e := json.Unmarshal(bytes.TrimSpace(stdout), &doc); e != nil {
		return nil, &backend.Error{Kind: backend.KindOther, Message: fmt.Sprintf("borg list: cannot parse JSON output: %v", e), Err: e}
	}
	var out []backend.Snapshot
	for _, ar := range doc.Archives {
		if ar.Name == "" {
			return nil, &backend.Error{Kind: backend.KindOther, Message: "borg archive entry missing required field name"}
		}
		if f.ArchiveGlob != "" {
			if ok, _ := path.Match(f.ArchiveGlob, ar.Name); !ok {
				continue
			}
		}
		s := backend.Snapshot{ID: ar.Name, Time: parseBorgTime(ar.Time)}
		if f.Host != "" {
			d, e := a.SnapshotDetails(ctx, s)
			if e != nil {
				return nil, e
			}
			if d.Host != f.Host {
				continue
			}
			s.Host = d.Host
		}
		out = append(out, s)
	}
	backend.SortSnapshots(out)
	return out, nil
}

// SnapshotDetails enriches a snapshot with the archive hostname via borg info.
func (a *Adapter) SnapshotDetails(ctx context.Context, s backend.Snapshot) (backend.Snapshot, error) {
	stdout, stderr, code, err := a.runner().RunCapture(ctx, []string{"info", "--json", a.Repo.Repository + "::" + s.ID})
	if err != nil {
		return s, classify("info", stderr, code, err)
	}
	var doc struct {
		Archives []struct {
			Hostname string `json:"hostname"`
			Time     string `json:"time"`
		} `json:"archives"`
	}
	if e := json.Unmarshal(bytes.TrimSpace(stdout), &doc); e != nil {
		return s, &backend.Error{Kind: backend.KindOther, Message: fmt.Sprintf("borg info: cannot parse JSON output: %v", e), Err: e}
	}
	if len(doc.Archives) > 0 {
		s.Host = doc.Archives[0].Hostname
		if t := parseBorgTime(doc.Archives[0].Time); !t.IsZero() {
			s.Time = t
		}
	}
	return s, nil
}

// EstimateRestoreSize sums file sizes from borg list --json-lines for the
// selected paths. Borg stores paths without a leading slash.
func (a *Adapter) EstimateRestoreSize(ctx context.Context, s backend.Snapshot, paths []string) (int64, error) {
	args := []string{"list", "--json-lines", a.Repo.Repository + "::" + s.ID}
	args = append(args, stripLeadingSlash(paths)...)
	var total int64
	stderr, code, err := a.runner().RunJSONLines(ctx, args, func(line []byte) error {
		var node struct {
			Type string `json:"type"`
			Size int64  `json:"size"`
		}
		if e := json.Unmarshal(line, &node); e != nil {
			return nil
		}
		if node.Type == "-" {
			total += node.Size
		}
		return nil
	})
	if err != nil {
		return 0, classify("list", stderr, code, err)
	}
	return total, nil
}

// Restore extracts the archive into target (used as the working directory),
// preserving the absolute-path-below-root layout. Borg extract takes a
// non-exclusive lock.
func (a *Adapter) Restore(ctx context.Context, s backend.Snapshot, paths []string, target string, progress func(backend.Progress)) (backend.RestoreStats, error) {
	args := []string{"extract", "--log-json", "--progress", "--lock-wait", "60", a.Repo.Repository + "::" + s.ID}
	args = append(args, stripLeadingSlash(paths)...)
	start := time.Now()
	var stats backend.RestoreStats
	if err := os.MkdirAll(target, 0o700); err != nil {
		return stats, &backend.Error{Kind: backend.KindOther, Message: fmt.Sprintf("cannot create restore target %q: %v", target, err), Err: err}
	}
	r := a.runner()
	r.Dir = target
	stderr, code, err := r.RunJSONLines(ctx, args, func(line []byte) error {
		var msg struct {
			Type     string  `json:"type"`
			Current  int64   `json:"current"`
			Total    int64   `json:"total"`
			Finished bool    `json:"finished"`
			Message  string  `json:"message"`
		}
		if e := json.Unmarshal(line, &msg); e != nil {
			return nil
		}
		if msg.Type == "progress_percent" && progress != nil {
			progress(backend.Progress{BytesDone: msg.Current, BytesTotal: msg.Total, Message: msg.Message})
		}
		return nil
	})
	if err != nil {
		return stats, classify("extract", stderr, code, err)
	}
	stats.Duration = time.Since(start)
	return stats, nil
}

func stripLeadingSlash(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = strings.TrimPrefix(p, "/")
	}
	return out
}

// parseBorgTime parses Borg's naive local timestamps.
func parseBorgTime(s string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000000", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// classify maps Borg exit codes and stderr to error kinds. Borg uses rc 2 for
// errors; passphrase and connectivity problems are detected from the message.
func classify(op, stderr string, code int, err error) error {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "passphrase") && (strings.Contains(lower, "wrong") || strings.Contains(lower, "incorrect")):
		return &backend.Error{Kind: backend.KindAuth, Message: fmt.Sprintf("borg %s: wrong repository passphrase — check passcommand or BORG_PASSPHRASE", op), Err: err}
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route to host") || strings.Contains(lower, "connection closed") || strings.Contains(lower, "ssh:") || strings.Contains(lower, "remote:"):
		return &backend.Error{Kind: backend.KindUnreachable, Message: fmt.Sprintf("borg %s: repository unreachable — check the repository URL, credentials and network", op), Err: err}
	case strings.Contains(lower, "does not exist") || strings.Contains(lower, "not a valid repository") || strings.Contains(lower, "no such file"):
		return &backend.Error{Kind: backend.KindUnreachable, Message: fmt.Sprintf("borg %s: repository unreachable — check the repository URL and that it is initialized", op), Err: err}
	case strings.Contains(lower, "lock") && strings.Contains(lower, "timeout"):
		return &backend.Error{Kind: backend.KindLocked, Message: fmt.Sprintf("borg %s: repository is locked — another operation holds a lock; do not schedule drills alongside prune/compact", op), Err: err}
	default:
		msg := strings.TrimSpace(stderr)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return &backend.Error{Kind: backend.KindOther, Message: fmt.Sprintf("borg %s failed: %s", op, firstLines(msg, 3)), Err: err}
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " ")
}

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