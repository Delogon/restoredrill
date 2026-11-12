// Package sandbox manages throwaway restore directories and (in later
// milestones) isolated container networks and service containers. Everything
// it creates carries the run ID and is torn down after the run.
package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
)

// Sandbox is the temporary workspace for one run.
type Sandbox struct {
	RunID      string
	Root       string // work_dir/<run-id>
	RestoreDir string // Root/restore
}

// Create makes a new sandbox directory with mode 0700 owned by the runner
// user. Restored files keep their original absolute path below RestoreDir.
func Create(workDir, runID string) (*Sandbox, error) {
	root := filepath.Join(workDir, runID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create sandbox %q: %w — check work_dir permissions and free space", root, err)
	}
	// Ensure the root itself is 0700 even if it already existed.
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, err
	}
	restore := filepath.Join(root, "restore")
	if err := os.MkdirAll(restore, 0o700); err != nil {
		return nil, err
	}
	return &Sandbox{RunID: runID, Root: root, RestoreDir: restore}, nil
}

// Cleanup removes the sandbox directory and everything in it.
func (s *Sandbox) Cleanup() error {
	if s == nil || s.Root == "" {
		return nil
	}
	return os.RemoveAll(s.Root)
}

// LabelKey is the container/directory label used for crash recovery and GC.
const LabelKey = "restoredrill.run"

// FreeBytes returns the free bytes on the filesystem containing path.
func FreeBytes(path string) (uint64, error) {
	return freeBytes(path)
}