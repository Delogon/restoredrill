package checks

import (
	"context"
	"strings"

	"github.com/restoredrill/restoredrill/internal/config"
)

// httpCheck issues an HTTP request from the probe container to a service and
// checks the response against expectations.
type httpCheck struct{}

func init() { Register(httpCheck{}) }

func (httpCheck) Type() string  { return "http" }
func (httpCheck) Phase() string { return "service" }

func (httpCheck) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	if c.Runtime == nil {
		return errorf("http: no container runtime available")
	}
	method := cfg.Method
	if method == "" {
		method = "GET"
	}
	path := cfg.Path
	if path == "" {
		path = "/"
	}
	status, body, err := c.Runtime.HTTPProbe(ctx, cfg.Service, cfg.Port, strings.ToUpper(method), path, cfg.Headers)
	if err != nil {
		return failf("http: probe of %s:%d%s failed: %v", cfg.Service, cfg.Port, path, err)
	}
	details := map[string]any{"service": cfg.Service, "status": status, "path": path}
	if ok, msg := matchHTTPResponse(cfg.Expect, status, body); !ok {
		if cfg.Expect.HasKey("status") && status != cfg.Expect.Status {
			details["expected_status"] = cfg.Expect.Status
		}
		return Result{Status: Fail, Message: sprintf("http: %s:%d%s %s", cfg.Service, cfg.Port, path, msg), Details: details}
	}
	return Result{Status: Pass, Message: sprintf("http: %s:%d%s returned %d as expected", cfg.Service, cfg.Port, path, status), Details: details}
}