package runner

import (
	"context"
	"encoding/json"

	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// startServicesImpl starts sandbox service containers and waits for health.
// The full implementation lands in M3 (containers.go); for file-only drills
// there is nothing to start.
func startServicesImpl(ctx context.Context, cctx *checks.Context, drill config.Drill, rt checks.ContainerRuntime, r *Runner) error {
	if drill.Sandbox == nil || len(drill.Sandbox.Services) == 0 {
		return nil
	}
	return startServicesContainers(ctx, cctx, drill, rt, r)
}