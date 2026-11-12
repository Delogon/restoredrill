package checks

import (
	"context"

	"github.com/restoredrill/restoredrill/internal/config"
)

// sizeTrend verifies restored bytes and file count did not drop by more than
// max_drop_percent versus the last passing run. With no prior passing run the
// check is skipped (nothing to compare against).
type sizeTrend struct{}

func init() { Register(sizeTrend{}) }

func (sizeTrend) Type() string  { return "size_trend" }
func (sizeTrend) Phase() string { return "data" }

func (sizeTrend) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	if c.Store == nil {
		return errorf("size_trend: no store available")
	}
	prev, err := c.Store.LastPassingRun(c.Drill)
	if err != nil {
		return errorf("size_trend: cannot read history: %v", err)
	}
	if prev.ID == "" || prev.ID == c.RunID {
		return skipf("size_trend: no previous passing run to compare against")
	}
	maxDrop := cfg.MaxDropPercent
	details := map[string]any{
		"previous_run":    prev.ID,
		"previous_bytes":  prev.RestoredBytes,
		"previous_files":  prev.RestoredFiles,
		"current_bytes":   c.RestoredBytes,
		"current_files":   c.RestoredFiles,
		"max_drop_percent": maxDrop,
	}
	// Compare bytes and file count; either dropping too far fails the check.
	if drop := percentDrop(prev.RestoredBytes, c.RestoredBytes); drop > maxDrop {
		details["bytes_drop_percent"] = drop
		return Result{Status: Fail, Message: sprintf("restored bytes dropped %.1f%% versus last passing run (max %.1f%%) — the backup may have lost data", drop, maxDrop), Details: details}
	}
	if drop := percentDrop(prev.RestoredFiles, c.RestoredFiles); drop > maxDrop {
		details["files_drop_percent"] = drop
		return Result{Status: Fail, Message: sprintf("restored file count dropped %.1f%% versus last passing run (max %.1f%%)", drop, maxDrop), Details: details}
	}
	return Result{Status: Pass, Message: sprintf("size within %.1f%% of last passing run", maxDrop), Details: details}
}

// percentDrop returns how much cur dropped relative to prev, in percent. A
// rise yields a negative drop. prev==0 yields 0 (no baseline).
func percentDrop(prev, cur int64) float64 {
	if prev <= 0 {
		return 0
	}
	return float64(prev-cur) / float64(prev) * 100
}