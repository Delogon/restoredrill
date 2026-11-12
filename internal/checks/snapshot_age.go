package checks

import (
	"context"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
)

// snapshotAge verifies the tested snapshot is newer than a limit. It is added
// automatically from snapshot.max_age by the runner.
type snapshotAge struct{}

func init() { Register(snapshotAge{}) }

func (snapshotAge) Type() string  { return "snapshot_age" }
func (snapshotAge) Phase() string { return "data" }

func (snapshotAge) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	maxAge := cfg.MaxAge.D()
	if maxAge <= 0 {
		return errorf("snapshot_age: max_age must be positive")
	}
	if c.Snapshot.Time.IsZero() {
		return errorf("snapshot_age: snapshot time is unknown")
	}
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	age := now.Sub(c.Snapshot.Time)
	details := map[string]any{
		"snapshot_time": c.Snapshot.Time.UTC().Format(time.RFC3339),
		"age_seconds":   int64(age.Seconds()),
		"max_age":       cfg.MaxAge.String(),
	}
	if age > maxAge {
		return Result{Status: Fail, Message: sprintf("snapshot is %s old, older than max_age %s — backups may have stopped", age.Round(time.Second), cfg.MaxAge), Details: details}
	}
	return Result{Status: Pass, Message: sprintf("snapshot age %s is within %s", age.Round(time.Second), cfg.MaxAge), Details: details}
}