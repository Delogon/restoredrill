package checks

import (
	"context"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
)

// serviceHealthy waits for a service to reach its healthy state in time. It is
// added automatically from a service healthcheck by the runner. When the
// healthcheck defines an HTTP probe (the only kind in v1), it polls that probe
// from the probe container until the expectation is met; otherwise it falls
// back to the container runtime's health/running state.
type serviceHealthy struct{}

func init() { Register(serviceHealthy{}) }

func (serviceHealthy) Type() string  { return "service_healthy" }
func (serviceHealthy) Phase() string { return "service" }

func (serviceHealthy) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	if c.Runtime == nil {
		return errorf("service_healthy: no container runtime available")
	}
	timeout := cfg.Timeout.D()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	start := time.Now()

	if cfg.Port > 0 {
		// HTTP healthcheck: poll until the expectation is met.
		method := cfg.Method
		if method == "" {
			method = "GET"
		}
		path := cfg.Path
		if path == "" {
			path = "/"
		}
		var lastMsg string
		for {
			status, body, err := c.Runtime.HTTPProbe(ctx, cfg.Service, cfg.Port, method, path, cfg.Headers)
			if err != nil {
				lastMsg = err.Error()
			} else if ok, msg := matchHTTPResponse(cfg.Expect, status, body); !ok {
				lastMsg = msg
			} else {
				return Result{
					Status:  Pass,
					Message: sprintf("service_healthy: %s became healthy in %s", cfg.Service, time.Since(start).Round(time.Millisecond)),
					Details: map[string]any{"service": cfg.Service, "status": status},
				}
			}
			if time.Now().After(start.Add(timeout)) {
				return failf("service_healthy: %s did not become healthy within %s (last: %s)", cfg.Service, timeout, lastMsg)
			}
			select {
			case <-ctx.Done():
				return errorf("service_healthy: cancelled while waiting for %s", cfg.Service)
			case <-time.After(time.Second):
			}
		}
	}

	if err := c.Runtime.WaitHealthy(ctx, cfg.Service, timeout); err != nil {
		return failf("service_healthy: %v", err)
	}
	return Result{
		Status:  Pass,
		Message: sprintf("service_healthy: %s became healthy in %s", cfg.Service, time.Since(start).Round(time.Millisecond)),
		Details: map[string]any{"service": cfg.Service},
	}
}