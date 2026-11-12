package runner

import (
	"context"
	"fmt"
	"sort"

	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/sandbox"
)

// startServicesContainers starts every sandbox service on the isolated
// network. Health waiting happens in the service_healthy checks (service
// phase), so a service that never becomes healthy is reported as a failed
// check rather than a lifecycle error.
func startServicesContainers(ctx context.Context, cctx *checks.Context, drill config.Drill, rt checks.ContainerRuntime, r *Runner) error {
	srt, ok := rt.(*sandbox.Runtime)
	if !ok {
		return fmt.Errorf("unexpected container runtime type %T", rt)
	}
	names := make([]string, 0, len(drill.Sandbox.Services))
	for n := range drill.Sandbox.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := drill.Sandbox.Services[name]
		if err := srt.StartService(ctx, name, svc, cctx.RestoreDir); err != nil {
			return err
		}
		cctx.Services[name] = true
	}
	return nil
}