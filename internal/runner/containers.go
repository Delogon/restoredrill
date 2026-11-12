package runner

import (
	"context"
	"fmt"

	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
)

// startServicesContainers is replaced by the full container implementation in
// M3. Until then, drills that declare services cannot run.
func startServicesContainers(ctx context.Context, cctx *checks.Context, drill config.Drill, rt checks.ContainerRuntime, r *Runner) error {
	return fmt.Errorf("service containers are not yet implemented (M3)")
}