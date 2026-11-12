package sandbox

import (
	"context"
	"os"
	"path/filepath"

	"github.com/restoredrill/restoredrill/internal/ulid"
)

// GC removes leftover containers, networks and sandbox directories from runs
// that are no longer active. shouldKeep(runID) reports whether a sandbox
// directory must be retained (e.g. a kept sandbox still within keep_ttl).
// It returns the number of resources removed.
func GC(ctx context.Context, client *DockerClient, activeRunIDs []string, workDir string, shouldKeep func(runID string) bool) (int, error) {
	active := map[string]bool{}
	for _, id := range activeRunIDs {
		active[id] = true
	}
	removed := 0

	if client != nil {
		containers, err := client.AllManagedContainers(ctx)
		if err == nil {
			for _, c := range containers {
				runID := c.Labels[LabelKey]
				if runID != "" && !active[runID] {
					_ = client.ContainerStop(ctx, c.ID)
					if err := client.ContainerRemove(ctx, c.ID); err == nil {
						removed++
					}
				}
			}
		}
		networks, err := client.AllManagedNetworks(ctx)
		if err == nil {
			for _, n := range networks {
				runID := n.Labels[LabelKey]
				if runID != "" && !active[runID] {
					if err := client.NetworkRemove(ctx, n.ID); err == nil {
						removed++
					}
				}
			}
		}
	}

	// Sandbox directories. Only directories named after a run ID (ULID) are
	// touched; anything else in work_dir is left alone.
	entries, err := os.ReadDir(workDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() || !ulid.Valid(e.Name()) {
				continue
			}
			runID := e.Name()
			if active[runID] {
				continue
			}
			if shouldKeep != nil && shouldKeep(runID) {
				continue
			}
			if err := os.RemoveAll(filepath.Join(workDir, runID)); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}