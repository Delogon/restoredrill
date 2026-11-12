package checks

import (
	"context"

	"github.com/restoredrill/restoredrill/internal/config"
)

// commandCheck runs a command in a container and checks its exit code and
// output. With "service" it execs into the running service (service phase);
// with "image" it runs a throwaway container with the restore mounted
// read-only at /restore (data phase).
type commandCheck struct{}

func init() { Register(commandCheck{}) }

func (commandCheck) Type() string  { return "command" }
func (commandCheck) Phase() string { return "data" } // service variant handled by CheckPhaseFor

func (commandCheck) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	if c.Runtime == nil {
		return errorf("command: no container runtime available")
	}
	var exit int
	var stdout, stderr string
	var err error
	if cfg.Service != "" {
		exit, stdout, stderr, err = c.Runtime.Exec(ctx, cfg.Service, cfg.Run)
	} else {
		mounts := []Mount{{Source: c.RestoreDir, Target: "/restore", ReadOnly: true}}
		exit, stdout, stderr, err = c.Runtime.RunOnce(ctx, cfg.Image, cfg.Run, nil, mounts)
	}
	if err != nil {
		return errorf("command: cannot run %v: %v", cfg.Run, err)
	}
	details := map[string]any{"exit_code": exit, "run": cfg.Run}
	wantExit := 0
	if cfg.Expect.ExitCode != nil {
		wantExit = *cfg.Expect.ExitCode
	}
	if exit != wantExit {
		details["expected_exit_code"] = wantExit
		details["stderr"] = truncate(stderr, 500)
		return Result{Status: Fail, Message: sprintf("command: exited %d, want %d", exit, wantExit), Details: details}
	}
	if ok, msg := matchText(cfg.Expect, stdout); !ok {
		details["stdout"] = truncate(stdout, 500)
		return Result{Status: Fail, Message: "command: " + msg, Details: details}
	}
	return Result{Status: Pass, Message: sprintf("command: exited %d as expected", exit), Details: details}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}