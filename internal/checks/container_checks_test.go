package checks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"gopkg.in/yaml.v3"
)

// parseCheck decodes a check from YAML so the expectation key set is populated
// exactly as in a real config.
func parseCheck(t *testing.T, src string) config.Check {
	t.Helper()
	var ch config.Check
	if err := yaml.Unmarshal([]byte(src), &ch); err != nil {
		t.Fatalf("parse check: %v", err)
	}
	return ch
}

// fakeRuntime is a scriptable ContainerRuntime for unit tests.
type fakeRuntime struct {
	availableErr error
	httpStatus   int
	httpBody     string
	httpErr      error
	httpCalls    int
	execExit     int
	execStdout   string
	execStderr   string
	execErr      error
	runOnceExit  int
	runOnceOut   string
	runOnceErr   error
	healthyErr   error
	lastImage    string
	lastCmd      []string
	lastMounts   []Mount
	stopped      []string
}

func (f *fakeRuntime) Available(ctx context.Context) error { return f.availableErr }
func (f *fakeRuntime) RunOnce(ctx context.Context, image string, cmd []string, env map[string]string, mounts []Mount) (int, string, string, error) {
	f.lastImage, f.lastCmd, f.lastMounts = image, cmd, mounts
	return f.runOnceExit, f.runOnceOut, "", f.runOnceErr
}
func (f *fakeRuntime) StartContainer(ctx context.Context, name, image string, env map[string]string, mounts []Mount) error {
	return nil
}
func (f *fakeRuntime) StopContainer(ctx context.Context, name string) error {
	f.stopped = append(f.stopped, name)
	return nil
}
func (f *fakeRuntime) Exec(ctx context.Context, container string, cmd []string) (int, string, string, error) {
	f.lastCmd = cmd
	return f.execExit, f.execStdout, f.execStderr, f.execErr
}
func (f *fakeRuntime) HTTPProbe(ctx context.Context, service string, port int, method, path string, headers map[string]string) (int, string, error) {
	f.httpCalls++
	return f.httpStatus, f.httpBody, f.httpErr
}
func (f *fakeRuntime) WaitHealthy(ctx context.Context, service string, timeout time.Duration) error {
	return f.healthyErr
}
func (f *fakeRuntime) CopyToContainer(ctx context.Context, container, localPath, containerPath string) error {
	return nil
}

func TestHTTPCheckPass(t *testing.T) {
	rt := &fakeRuntime{httpStatus: 200, httpBody: `{"version":"1.0"}`}
	c := &Context{Runtime: rt}
	ch, _ := Get("http")
	cfg := parseCheck(t, `
type: http
service: app
port: 80
path: /api/config
expect: { status: 200, json: { path: $.version, exists: true } }
`)
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestHTTPCheckWrongStatusFails(t *testing.T) {
	rt := &fakeRuntime{httpStatus: 500, httpBody: "oops"}
	c := &Context{Runtime: rt}
	ch, _ := Get("http")
	cfg := parseCheck(t, `
type: http
service: app
port: 80
expect: { status: 200 }
`)
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for 500, got %+v", r)
	}
}

func TestHTTPCheckProbeErrorFails(t *testing.T) {
	rt := &fakeRuntime{httpErr: fmt.Errorf("connection refused")}
	c := &Context{Runtime: rt}
	ch, _ := Get("http")
	cfg := parseCheck(t, `type: http
service: app
port: 80
`)
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail on probe error, got %+v", r)
	}
}

func TestHTTPCheckNoRuntimeErrors(t *testing.T) {
	c := &Context{Runtime: nil}
	ch, _ := Get("http")
	cfg := parseCheck(t, "type: http\nservice: app\nport: 80\n")
	if r := ch.Run(context.Background(), c, cfg); r.Status != Error {
		t.Fatalf("expected error without runtime, got %+v", r)
	}
}

func TestCommandCheckImagePass(t *testing.T) {
	rt := &fakeRuntime{runOnceExit: 0, runOnceOut: "hello world"}
	c := &Context{Runtime: rt, RestoreDir: "/restore"}
	ch, _ := Get("command")
	cfg := parseCheck(t, `
type: command
image: busybox
run: [echo, hello world]
expect: { contains: hello }
`)
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
	if rt.lastImage != "busybox" {
		t.Fatalf("image = %q", rt.lastImage)
	}
	if len(rt.lastMounts) != 1 || rt.lastMounts[0].Target != "/restore" || !rt.lastMounts[0].ReadOnly {
		t.Fatalf("restore must be mounted read-only at /restore, got %+v", rt.lastMounts)
	}
}

func TestCommandCheckWrongExitFails(t *testing.T) {
	rt := &fakeRuntime{runOnceExit: 1}
	c := &Context{Runtime: rt, RestoreDir: "/restore"}
	ch, _ := Get("command")
	cfg := parseCheck(t, "type: command\nimage: busybox\nrun: ['false']\n")
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for exit 1, got %+v", r)
	}
}

func TestCommandCheckServiceUsesExec(t *testing.T) {
	rt := &fakeRuntime{execExit: 0, execStdout: "ok"}
	c := &Context{Runtime: rt}
	ch, _ := Get("command")
	cfg := parseCheck(t, "type: command\nservice: app\nrun: ['true']\n")
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
	if rt.lastImage != "" {
		t.Fatalf("service command must exec, not run a new image")
	}
}

func TestServiceHealthyHTTPPass(t *testing.T) {
	rt := &fakeRuntime{httpStatus: 200}
	c := &Context{Runtime: rt}
	ch, _ := Get("service_healthy")
	cfg := parseCheck(t, `
type: service_healthy
service: app
port: 80
path: /alive
timeout: 2s
expect: { status: 200 }
`)
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestServiceHealthyHTTPTimeoutFails(t *testing.T) {
	rt := &fakeRuntime{httpErr: fmt.Errorf("connection refused")}
	c := &Context{Runtime: rt}
	ch, _ := Get("service_healthy")
	cfg := parseCheck(t, `
type: service_healthy
service: app
port: 80
timeout: 1200ms
expect: { status: 200 }
`)
	start := time.Now()
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail on timeout, got %+v", r)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout not respected")
	}
}

func TestServiceHealthyFallbackWaitHealthy(t *testing.T) {
	rt := &fakeRuntime{healthyErr: fmt.Errorf("never healthy")}
	c := &Context{Runtime: rt}
	ch, _ := Get("service_healthy")
	cfg := parseCheck(t, "type: service_healthy\nservice: app\ntimeout: 1s\n")
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail, got %+v", r)
	}
}