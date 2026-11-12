// Package checks implements the assertion registry. Each check type lives in
// its own file and registers itself. Data checks run on restored files before
// any service starts; service checks run after services are healthy.
package checks

import (
	"context"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/store"
)

// Result statuses mirror run statuses at the check level.
const (
	Pass    = "pass"
	Fail    = "fail"
	Error   = "error"
	Skipped = "skipped"
)

// Result is the outcome of one check.
type Result struct {
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Convenience constructors.
func passf(format string, args ...any) Result  { return Result{Status: Pass, Message: sprintf(format, args...)} }
func failf(format string, args ...any) Result  { return Result{Status: Fail, Message: sprintf(format, args...)} }
func errorf(format string, args ...any) Result { return Result{Status: Error, Message: sprintf(format, args...)} }
func skipf(format string, args ...any) Result  { return Result{Status: Skipped, Message: sprintf(format, args...)} }

// Mount is a bind mount for container-based checks.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// ContainerRuntime is the subset of the sandbox runtime that container-based
// checks need. It is nil for file-only drills. The sandbox package implements
// it; defining it here keeps checks decoupled from container internals.
type ContainerRuntime interface {
	// Available reports whether the container runtime is reachable.
	Available(ctx context.Context) error
	// RunOnce starts a throwaway container from image, runs cmd, and returns
	// its exit code and output. The container is removed afterwards.
	RunOnce(ctx context.Context, image string, cmd []string, env map[string]string, mounts []Mount) (exit int, stdout, stderr string, err error)
	// Exec runs cmd inside an already-running service container.
	Exec(ctx context.Context, service string, cmd []string) (exit int, stdout, stderr string, err error)
	// HTTPProbe issues an HTTP request from the probe container to a service.
	HTTPProbe(ctx context.Context, service string, port int, method, path string, headers map[string]string) (status int, body string, err error)
	// WaitHealthy waits until a service is healthy or the timeout elapses.
	WaitHealthy(ctx context.Context, service string, timeout time.Duration) error
	// CopyToContainer copies a local file into a service container.
	CopyToContainer(ctx context.Context, service, localPath, containerPath string) error
}

// Context carries everything a check may need.
type Context struct {
	RestoreDir    string
	Snapshot      backend.Snapshot
	Drill         string
	RunID         string
	Store         *store.Store
	Runtime       ContainerRuntime // nil when no containers are available/needed
	Services      map[string]bool  // names of started services
	RestoredBytes int64            // bytes restored this run (for size_trend)
	RestoredFiles int64            // files restored this run (for size_trend)
	Now           time.Time
	Log           func(level, msg string)
}

// Check is one assertion type.
type Check interface {
	Type() string
	Phase() string // "data" or "service"
	Run(ctx context.Context, c *Context, cfg config.Check) Result
}

var registry = map[string]Check{}

// Register adds a check type to the registry. Called from init().
func Register(c Check) { registry[c.Type()] = c }

// Get returns a check by type name.
func Get(typ string) (Check, bool) {
	c, ok := registry[typ]
	return c, ok
}

// Types lists registered check type names.
func Types() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}

// CheckPhaseFor returns the phase a configured check runs in. Most types have
// a fixed phase; a command check runs in the service phase when it targets a
// service, otherwise in the data phase.
func CheckPhaseFor(ch config.Check) string {
	if ch.Type == "command" && ch.Service != "" {
		return "service"
	}
	if c, ok := registry[ch.Type]; ok {
		return c.Phase()
	}
	return config.CheckPhase(ch.Type)
}