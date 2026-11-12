package checks

import (
	"context"
	"os"
	"path/filepath"

	"github.com/restoredrill/restoredrill/internal/config"
)

// filesExist verifies glob patterns match at least min_count files, optionally
// with a minimum total size.
type filesExist struct{}

func init() { Register(filesExist{}) }

func (filesExist) Type() string  { return "files_exist" }
func (filesExist) Phase() string { return "data" }

func (filesExist) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	files, err := GlobFiles(c.RestoreDir, cfg.Paths)
	if err != nil {
		return errorf("files_exist: cannot walk restore dir: %v", err)
	}
	var totalBytes int64
	for _, rel := range files {
		fi, err := os.Stat(filepath.Join(c.RestoreDir, filepath.FromSlash(rel)))
		if err == nil {
			totalBytes += fi.Size()
		}
	}
	minCount := cfg.MinCount
	if minCount == 0 {
		minCount = 1
	}
	details := map[string]any{
		"matched_files": len(files),
		"total_bytes":   totalBytes,
		"min_count":     minCount,
	}
	if len(files) < minCount {
		return Result{Status: Fail, Message: sprintf("matched %d files, need at least %d — expected files are missing from the backup", len(files), minCount), Details: details}
	}
	if cfg.MinBytes > 0 && totalBytes < cfg.MinBytes {
		details["min_bytes"] = cfg.MinBytes
		return Result{Status: Fail, Message: sprintf("matched files total %d bytes, need at least %d", totalBytes, cfg.MinBytes), Details: details}
	}
	return Result{Status: Pass, Message: sprintf("matched %d files (%d bytes)", len(files), totalBytes), Details: details}
}