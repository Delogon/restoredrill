// Package store persists runs, check results, events and notifications in an
// embedded SQLite database (pure Go, no CGO). Migrations are embedded and
// forward-only. Run IDs are ULIDs.
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Status values.
const (
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusError   = "error"
	StatusSkipped = "skipped"
	StatusRunning = "running"
)

// Trigger values.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
	TriggerCLI      = "cli"
)

// Run is one execution of a drill.
type Run struct {
	ID             string    `json:"id"`
	Drill          string    `json:"drill"`
	Trigger        string    `json:"trigger"`
	Status         string    `json:"status"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	Repository     string    `json:"repository,omitempty"`
	Backend        string    `json:"backend,omitempty"`
	SnapshotID     string    `json:"snapshot_id,omitempty"`
	SnapshotTime   time.Time `json:"snapshot_time,omitempty"`
	SnapshotHost   string    `json:"snapshot_host,omitempty"`
	Seed           int64     `json:"seed,omitempty"`
	RestoredBytes  int64     `json:"restored_bytes,omitempty"`
	RestoredFiles  int64     `json:"restored_files,omitempty"`
	RestoreSeconds float64   `json:"restore_seconds,omitempty"`
	ErrorKind      string    `json:"error_kind,omitempty"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	KeptPath       string    `json:"kept_path,omitempty"`
	ConfigHash     string    `json:"config_hash,omitempty"`
}

// CheckResult is one assertion outcome.
type CheckResult struct {
	RunID       string    `json:"run_id"`
	Seq         int       `json:"seq"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Phase       string    `json:"phase"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	DurationMS  int64     `json:"duration_ms"`
	Message     string    `json:"message"`
	DetailsJSON string    `json:"details_json,omitempty"`
}

// RunEvent is a redacted log line tied to a lifecycle step.
type RunEvent struct {
	RunID   string    `json:"run_id"`
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"`
	Step    string    `json:"step"`
	Message string    `json:"message"`
}

// Notification records one notification attempt.
type Notification struct {
	RunID    string    `json:"run_id"`
	Notifier string    `json:"notifier"`
	Event    string    `json:"event"`
	SentAt   time.Time `json:"sent_at"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
}

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("cannot create state dir: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// A single connection serializes writes and avoids SQLITE_BUSY for this
	// embedded, low-concurrency workload.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot migrate state database: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration filename %q", name)
		}
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromTS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// InsertRun stores a new run.
func (s *Store) InsertRun(r Run) error {
	_, err := s.db.Exec(`INSERT INTO runs
		(id, drill, trigger, status, started_at, finished_at, repository, backend,
		 snapshot_id, snapshot_time, snapshot_host, seed, restored_bytes, restored_files,
		 restore_seconds, error_kind, error_message, kept_path, config_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Drill, r.Trigger, r.Status, ts(r.StartedAt), ts(r.FinishedAt), r.Repository, r.Backend,
		r.SnapshotID, ts(r.SnapshotTime), r.SnapshotHost, r.Seed, r.RestoredBytes, r.RestoredFiles,
		r.RestoreSeconds, r.ErrorKind, r.ErrorMessage, r.KeptPath, r.ConfigHash)
	return err
}

// UpdateRun persists mutable run fields after completion.
func (s *Store) UpdateRun(r Run) error {
	_, err := s.db.Exec(`UPDATE runs SET
		status=?, finished_at=?, repository=?, backend=?, snapshot_id=?, snapshot_time=?,
		snapshot_host=?, seed=?, restored_bytes=?, restored_files=?, restore_seconds=?,
		error_kind=?, error_message=?, kept_path=?, config_hash=? WHERE id=?`,
		r.Status, ts(r.FinishedAt), r.Repository, r.Backend, r.SnapshotID, ts(r.SnapshotTime),
		r.SnapshotHost, r.Seed, r.RestoredBytes, r.RestoredFiles, r.RestoreSeconds,
		r.ErrorKind, r.ErrorMessage, r.KeptPath, r.ConfigHash, r.ID)
	return err
}

const runCols = `id, drill, trigger, status, started_at, finished_at, repository, backend,
	snapshot_id, snapshot_time, snapshot_host, seed, restored_bytes, restored_files,
	restore_seconds, error_kind, error_message, kept_path, config_hash`

func scanRun(row interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var started, finished, snapTime int64
	err := row.Scan(&r.ID, &r.Drill, &r.Trigger, &r.Status, &started, &finished, &r.Repository, &r.Backend,
		&r.SnapshotID, &snapTime, &r.SnapshotHost, &r.Seed, &r.RestoredBytes, &r.RestoredFiles,
		&r.RestoreSeconds, &r.ErrorKind, &r.ErrorMessage, &r.KeptPath, &r.ConfigHash)
	r.StartedAt = fromTS(started)
	r.FinishedAt = fromTS(finished)
	r.SnapshotTime = fromTS(snapTime)
	return r, err
}

// GetRun returns one run by ID.
func (s *Store) GetRun(id string) (Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id))
}

// ListRuns returns runs for a drill (newest first). drill=="" lists all.
func (s *Store) ListRuns(drill string, limit, offset int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if drill == "" {
		rows, err = s.db.Query(`SELECT `+runCols+` FROM runs ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	} else {
		rows, err = s.db.Query(`SELECT `+runCols+` FROM runs WHERE drill=? ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?`, drill, limit, offset)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastRun returns the most recent non-skipped run for a drill, or a zero Run.
func (s *Store) LastRun(drill string) (Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE drill=? AND status!=? ORDER BY started_at DESC, id DESC LIMIT 1`, drill, StatusSkipped))
	if err == sql.ErrNoRows {
		return Run{}, nil
	}
	return r, err
}

// LastPassingRun returns the most recent passing run for a drill (size_trend).
func (s *Store) LastPassingRun(drill string) (Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE drill=? AND status=? ORDER BY started_at DESC, id DESC LIMIT 1`, drill, StatusPass))
	if err == sql.ErrNoRows {
		return Run{}, nil
	}
	return r, err
}

// LastNotified returns the most recent notification for a drill+notifier+event.
func (s *Store) LastNotified(drill, notifier, event string) (Notification, error) {
	var n Notification
	var sentAt int64
	var ok int
	err := s.db.QueryRow(`SELECT n.run_id, n.notifier, n.event, n.sent_at, n.ok, n.error
		FROM notifications n JOIN runs r ON r.id=n.run_id
		WHERE r.drill=? AND n.notifier=? AND n.event=? ORDER BY n.sent_at DESC LIMIT 1`,
		drill, notifier, event).Scan(&n.RunID, &n.Notifier, &n.Event, &sentAt, &ok, &n.Error)
	if err == sql.ErrNoRows {
		return Notification{}, nil
	}
	n.SentAt = fromTS(sentAt)
	n.OK = ok == 1
	return n, err
}

// InsertCheckResult stores one check outcome.
func (s *Store) InsertCheckResult(c CheckResult) error {
	_, err := s.db.Exec(`INSERT INTO check_results
		(run_id, seq, name, type, phase, status, started_at, duration_ms, message, details_json)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.RunID, c.Seq, c.Name, c.Type, c.Phase, c.Status, ts(c.StartedAt), c.DurationMS, c.Message, c.DetailsJSON)
	return err
}

// ListCheckResults returns check results for a run in order.
func (s *Store) ListCheckResults(runID string) ([]CheckResult, error) {
	rows, err := s.db.Query(`SELECT run_id, seq, name, type, phase, status, started_at, duration_ms, message, details_json
		FROM check_results WHERE run_id=? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckResult
	for rows.Next() {
		var c CheckResult
		var started int64
		if err := rows.Scan(&c.RunID, &c.Seq, &c.Name, &c.Type, &c.Phase, &c.Status, &started, &c.DurationMS, &c.Message, &c.DetailsJSON); err != nil {
			return nil, err
		}
		c.StartedAt = fromTS(started)
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertEvent stores a redacted log line.
func (s *Store) InsertEvent(e RunEvent) error {
	_, err := s.db.Exec(`INSERT INTO run_events (run_id, ts, level, step, message) VALUES (?,?,?,?,?)`,
		e.RunID, ts(e.TS), e.Level, e.Step, e.Message)
	return err
}

// ListEvents returns events for a run in time order.
func (s *Store) ListEvents(runID string) ([]RunEvent, error) {
	rows, err := s.db.Query(`SELECT run_id, ts, level, step, message FROM run_events WHERE run_id=? ORDER BY ts`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunEvent
	for rows.Next() {
		var e RunEvent
		var t int64
		if err := rows.Scan(&e.RunID, &t, &e.Level, &e.Step, &e.Message); err != nil {
			return nil, err
		}
		e.TS = fromTS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}

// InsertNotification records a notification attempt.
func (s *Store) InsertNotification(n Notification) error {
	ok := 0
	if n.OK {
		ok = 1
	}
	_, err := s.db.Exec(`INSERT INTO notifications (run_id, notifier, event, sent_at, ok, error) VALUES (?,?,?,?,?,?)`,
		n.RunID, n.Notifier, n.Event, ts(n.SentAt), ok, n.Error)
	return err
}

// ActiveRuns returns runs currently marked running (for crash recovery).
func (s *Store) ActiveRuns() ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE status=?`, StatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApplyRetention keeps only the newest keep runs per drill.
func (s *Store) ApplyRetention(drill string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	_, err := s.db.Exec(`DELETE FROM check_results WHERE run_id IN
		(SELECT id FROM runs WHERE drill=? ORDER BY started_at DESC, id DESC LIMIT -1 OFFSET ?)`, drill, keep)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM run_events WHERE run_id IN
		(SELECT id FROM runs WHERE drill=? ORDER BY started_at DESC, id DESC LIMIT -1 OFFSET ?)`, drill, keep); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM notifications WHERE run_id IN
		(SELECT id FROM runs WHERE drill=? ORDER BY started_at DESC, id DESC LIMIT -1 OFFSET ?)`, drill, keep); err != nil {
		return err
	}
	_, err = s.db.Exec(`DELETE FROM runs WHERE drill=? AND id IN
		(SELECT id FROM runs WHERE drill=? ORDER BY started_at DESC, id DESC LIMIT -1 OFFSET ?)`, drill, drill, keep)
	return err
}

// CountRuns returns the number of stored runs for a drill.
func (s *Store) CountRuns(drill string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE drill=?`, drill).Scan(&n)
	return n, err
}