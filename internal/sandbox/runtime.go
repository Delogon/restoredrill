package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
)

// ProbeBin is the path of the restoredrill binary inside the tool's own image.
// The probe container uses the hidden "probe" subcommand, so no curl image is
// needed.
const ProbeBin = "/usr/local/bin/restoredrill"

// ProbeImage returns the image used for the HTTP probe container. Override with
// RESTOREDRILL_PROBE_IMAGE (e.g. a locally built image).
func ProbeImage() string {
	if v := os.Getenv("RESTOREDRILL_PROBE_IMAGE"); v != "" {
		return v
	}
	return "ghcr.io/restoredrill/restoredrill:latest"
}

// Runtime manages the isolated network, probe container and service containers
// for one run. It implements checks.ContainerRuntime.
type Runtime struct {
	client      *DockerClient
	runID       string
	drill       string
	labels      map[string]string
	networkID   string
	networkName string
	probeName   string
	defaults    *config.Resources
	log         func(string)

	mu         sync.Mutex
	containers []string // every container created, for teardown
	services   map[string]bool
}

// NewRuntime creates a runtime bound to a run. It does not create anything yet.
func NewRuntime(client *DockerClient, runID, drill string, defaults *config.Resources, log func(string)) *Runtime {
	return &Runtime{
		client: client,
		runID:  runID,
		drill:  drill,
		labels: map[string]string{
			LabelKey:       runID,
			LabelManaged:   "true",
			"restoredrill.drill": drill,
		},
		defaults:  defaults,
		log:       log,
		services:  map[string]bool{},
	}
}

// Available implements checks.ContainerRuntime.
func (r *Runtime) Available(ctx context.Context) error { return r.client.Ping(ctx) }

func (r *Runtime) logf(format string, args ...any) {
	if r.log != nil {
		r.log(fmt.Sprintf(format, args...))
	}
}

// Init creates the isolated network and the probe container.
func (r *Runtime) Init(ctx context.Context) error {
	r.networkName = "rd-" + r.runID
	id, err := r.client.NetworkCreate(ctx, r.networkName, r.labels)
	if err != nil {
		return fmt.Errorf("cannot create isolated network: %w", err)
	}
	r.networkID = id
	r.logf("created isolated network %s (no egress)", r.networkName)
	return nil
}

// ensureProbe lazily creates the probe container on first HTTP probe and
// returns its name. Drills without HTTP checks never need the probe image.
func (r *Runtime) ensureProbe(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probeName != "" {
		return r.probeName, nil
	}
	name := "rd-probe-" + r.runID
	if err := r.client.EnsureImage(ctx, ProbeImage()); err != nil {
		return "", err
	}
	probeID, err := r.client.ContainerCreate(ctx, ContainerConfig{
		Image:   ProbeImage(),
		Name:    name,
		Cmd:     []string{ProbeBin, "probe", "wait"},
		Labels:  r.labels,
		Network: r.networkName,
	})
	if err != nil {
		return "", fmt.Errorf("cannot create probe container: %w", err)
	}
	r.containers = append(r.containers, probeID)
	if err := r.client.ContainerStart(ctx, probeID); err != nil {
		return "", fmt.Errorf("cannot start probe container: %w", err)
	}
	r.probeName = name
	return name, nil
}

func (r *Runtime) track(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.containers = append(r.containers, id)
}

func (r *Runtime) resourcesFor(svc *config.Service) (float64, int64) {
	res := r.defaults
	if svc != nil && svc.Resources != nil {
		res = svc.Resources
	}
	if res == nil {
		return 0, 0
	}
	mem, _ := res.MemoryBytes()
	return res.CPUs, mem
}

// StartService starts one service container on the isolated network.
func (r *Runtime) StartService(ctx context.Context, name string, svc config.Service, restoreDir string) error {
	if err := r.client.EnsureImage(ctx, svc.Image); err != nil {
		return err
	}
	cpus, mem := r.resourcesFor(&svc)
	cfg := ContainerConfig{
		Image:        svc.Image,
		Name:         "rd-" + name + "-" + r.runID,
		Env:          svc.Env,
		Mounts:       configMountsToSandbox(svc.Mounts),
		Labels:       r.labels,
		Network:      r.networkName,
		NetworkAlias: name,
		CPUs:         cpus,
		MemoryBytes:  mem,
		CapAdd:       svc.CapAdd,
		User:         svc.User,
	}
	id, err := r.client.ContainerCreate(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cannot create service %s: %w", name, err)
	}
	r.track(id)
	if err := r.client.ContainerStart(ctx, id); err != nil {
		return fmt.Errorf("cannot start service %s: %w", name, err)
	}
	r.mu.Lock()
	r.services[name] = true
	r.mu.Unlock()
	r.logf("started service %s (%s)", name, svc.Image)
	return nil
}

// WaitHealthy implements checks.ContainerRuntime. It waits for the container's
// Docker healthcheck to report healthy, or for it to be running when no
// healthcheck is defined.
func (r *Runtime) WaitHealthy(ctx context.Context, service string, timeout time.Duration) error {
	name := "rd-" + service + "-" + r.runID
	deadline := time.Now().Add(timeout)
	for {
		state, err := r.client.ContainerInspect(ctx, name)
		if err == nil {
			if state.State.Health.Status == "healthy" {
				return nil
			}
			if state.State.Health.Status == "" && state.State.Running {
				return nil // no healthcheck defined; running is enough
			}
			if !state.State.Running && state.State.Status == "exited" {
				return fmt.Errorf("service %s exited with code %d before becoming healthy", service, state.State.ExitCode)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service %s did not become healthy within %s", service, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// RunOnce implements checks.ContainerRuntime.
func (r *Runtime) RunOnce(ctx context.Context, image string, cmd []string, env map[string]string, mounts []checks.Mount) (int, string, string, error) {
	if err := r.client.EnsureImage(ctx, image); err != nil {
		return -1, "", "", err
	}
	name := fmt.Sprintf("rd-once-%s-%d", r.runID, time.Now().UnixNano())
	id, err := r.client.ContainerCreate(ctx, ContainerConfig{
		Image: image, Name: name, Cmd: cmd, Env: env, Mounts: toSandboxMounts(mounts),
		Labels: r.labels, Network: r.networkName,
	})
	if err != nil {
		return -1, "", "", err
	}
	r.track(id)
	defer r.client.ContainerRemove(context.WithoutCancel(ctx), id)
	if err := r.client.ContainerStart(ctx, id); err != nil {
		return -1, "", "", err
	}
	// Wait for it to exit, then read logs.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		state, err := r.client.ContainerInspect(ctx, id)
		if err == nil && !state.State.Running {
			logs, _ := r.client.ContainerLogs(ctx, id, 1000)
			return state.State.ExitCode, logs, "", nil
		}
		if time.Now().After(deadline) {
			return -1, "", "", fmt.Errorf("command container timed out")
		}
		select {
		case <-ctx.Done():
			return -1, "", "", ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// StartContainer implements checks.ContainerRuntime (ephemeral named server).
func (r *Runtime) StartContainer(ctx context.Context, name, image string, env map[string]string, mounts []checks.Mount) error {
	if err := r.client.EnsureImage(ctx, image); err != nil {
		return err
	}
	full := "rd-" + name
	id, err := r.client.ContainerCreate(ctx, ContainerConfig{
		Image: image, Name: full, Env: env, Mounts: toSandboxMounts(mounts),
		Labels: r.labels, Network: r.networkName, NetworkAlias: name,
	})
	if err != nil {
		return err
	}
	r.track(id)
	return r.client.ContainerStart(ctx, id)
}

// StopContainer implements checks.ContainerRuntime.
func (r *Runtime) StopContainer(ctx context.Context, name string) error {
	full := "rd-" + name
	_ = r.client.ContainerStop(ctx, full)
	return r.client.ContainerRemove(ctx, full)
}

// Exec implements checks.ContainerRuntime.
func (r *Runtime) Exec(ctx context.Context, container string, cmd []string) (int, string, string, error) {
	// Service names map to their container name; ephemeral names are prefixed.
	name := container
	if r.isService(container) {
		name = "rd-" + container + "-" + r.runID
	} else if !startsWithRD(container) {
		name = "rd-" + container
	}
	return r.client.Exec(ctx, name, cmd)
}

func (r *Runtime) isService(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.services[name]
}

func startsWithRD(s string) bool { return len(s) >= 3 && s[:3] == "rd-" }

// CopyToContainer implements checks.ContainerRuntime.
func (r *Runtime) CopyToContainer(ctx context.Context, container, localPath, containerPath string) error {
	name := container
	if r.isService(container) {
		name = "rd-" + container + "-" + r.runID
	} else if !startsWithRD(container) {
		name = "rd-" + container
	}
	return r.client.CopyToContainer(ctx, name, localPath, containerPath)
}

// HTTPProbe implements checks.ContainerRuntime by exec'ing the hidden probe
// subcommand inside the probe container on the isolated network.
func (r *Runtime) HTTPProbe(ctx context.Context, service string, port int, method, path string, headers map[string]string) (int, string, error) {
	probeName, err := r.ensureProbe(ctx)
	if err != nil {
		return 0, "", err
	}
	url := fmt.Sprintf("http://%s:%d%s", service, port, path)
	cmd := []string{ProbeBin, "probe", "http", "--url", url, "--method", method}
	for k, v := range headers {
		cmd = append(cmd, "--header", k+":"+v)
	}
	exit, out, stderr, err := r.client.Exec(ctx, probeName, cmd)
	if err != nil {
		return 0, "", fmt.Errorf("probe exec failed: %w", err)
	}
	if exit != 0 {
		return 0, "", fmt.Errorf("probe failed: %s", stderr)
	}
	var res struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return 0, "", fmt.Errorf("cannot parse probe output: %w", err)
	}
	if res.Error != "" {
		return 0, "", fmt.Errorf("probe: %s", res.Error)
	}
	return res.Status, res.Body, nil
}

func toSandboxMounts(ms []checks.Mount) []Mount {
	out := make([]Mount, len(ms))
	for i, m := range ms {
		out[i] = Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly}
	}
	return out
}

func configMountsToSandbox(ms []config.Mount) []Mount {
	out := make([]Mount, len(ms))
	for i, m := range ms {
		out[i] = Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly}
	}
	return out
}

// Teardown removes every container and the network created for this run. It is
// safe to call multiple times and tolerates already-removed resources.
func (r *Runtime) Teardown(ctx context.Context) {
	r.mu.Lock()
	ids := append([]string(nil), r.containers...)
	r.containers = nil
	netID := r.networkID
	r.networkID = ""
	r.probeName = ""
	r.mu.Unlock()
	for _, id := range ids {
		_ = r.client.ContainerStop(ctx, id)
		_ = r.client.ContainerRemove(ctx, id)
	}
	if netID != "" {
		_ = r.client.NetworkRemove(ctx, netID)
	}
	r.logf("container teardown complete")
}