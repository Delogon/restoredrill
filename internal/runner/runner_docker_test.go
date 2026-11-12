//go:build docker

// Full-drill container tests: a restored service served over an isolated
// network, probed by the probe container. Requires a reachable Docker/Podman
// socket and restic. Run with:
//
//	TMPDIR=<big-disk> go test -tags docker ./internal/runner/
package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/sandbox"
	"github.com/restoredrill/restoredrill/internal/store"
)

func requireDocker(t *testing.T) *sandbox.DockerClient {
	t.Helper()
	c, err := sandbox.NewDockerClient()
	if err != nil {
		t.Skipf("no container runtime configured: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("Docker/Podman not reachable: %v", err)
	}
	return c
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func buildImageFrom(t *testing.T, c *sandbox.DockerClient, dockerfile string, files map[string][]byte, tag string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		mode := int64(0o644)
		if strings.HasSuffix(name, "restoredrill") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.BuildImage(ctx, buf.Bytes(), tag); err != nil {
		t.Fatalf("build %s: %v", tag, err)
	}
	t.Cleanup(func() { _ = c.RemoveImage(context.Background(), tag) })
}

// buildProbeImage builds the tool's own image for the probe container.
func buildProbeImage(t *testing.T, c *sandbox.DockerClient) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "restoredrill")
	build := exec.Command("go", "build", "-o", bin, "./cmd/restoredrill")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	tag := "restoredrill-probe:test"
	buildImageFrom(t, c, "FROM busybox\nCOPY restoredrill /usr/local/bin/restoredrill\n", map[string][]byte{"restoredrill": data}, tag)
	t.Setenv("RESTOREDRILL_PROBE_IMAGE", tag)
	return tag
}

// buildHTTPDImage builds a tiny BusyBox HTTP server image (default CMD serves
// /www on port 80), so no service command override is needed.
func buildHTTPDImage(t *testing.T, c *sandbox.DockerClient) string {
	t.Helper()
	tag := "restoredrill-httpd:test"
	buildImageFrom(t, c, "FROM busybox\nCMD [\"httpd\",\"-f\",\"-p\",\"80\",\"-h\",\"/www\"]\n", nil, tag)
	return tag
}

// setupResticFixture backs up a small web root into a temporary restic repo.
func setupResticFixture(t *testing.T) (src, repoPath string) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	base := t.TempDir()
	src = filepath.Join(base, "src")
	www := filepath.Join(src, "srv", "app", "www")
	os.MkdirAll(www, 0o755)
	os.WriteFile(filepath.Join(www, "index.html"), []byte("restored-ok"), 0o644)
	repoPath = filepath.Join(base, "repo")
	env := append(os.Environ(), "RESTIC_PASSWORD="+pw, "RESTIC_REPOSITORY="+repoPath)
	run := func(args ...string) {
		cmd := exec.Command("restic", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("backup", "--tag", "web", www)
	return src, repoPath
}

func dockerDrillConfig(t *testing.T, repoPath, src, base, httpdImage string, checksYAML string) *config.Config {
	t.Helper()
	passFile := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(passFile, []byte(pw+"\n"), 0o600)
	cfg := &config.Config{
		Version:  1,
		StateDir: filepath.Join(base, "state"),
		WorkDir:  filepath.Join(base, "work"),
		Binaries: config.Binaries{Restic: "restic", Borg: "borg"},
		Retention: config.Retention{RunsPerDrill: 50},
		Repositories: map[string]config.Repository{
			"r": {Type: "restic", Repository: repoPath, PasswordFile: passFile},
		},
		Drills: map[string]config.Drill{
			"d": {
				Repository: "r",
				Snapshot:   config.Snapshot{Select: "latest", Tags: []string{"web"}, MaxAge: config.Duration(24 * time.Hour)},
				Restore:    config.Restore{Paths: []string{filepath.Join(src, "srv", "app", "www")}},
				Timeout:    config.Duration(3 * time.Minute),
				Sandbox: &config.Sandbox{Services: map[string]config.Service{
					"app": {
						Image: httpdImage,
						Mounts: []config.Mount{{Source: "${restore}/srv/app/www", Target: "/www", ReadOnly: true}},
						Healthcheck: &config.Healthcheck{
							HTTP:    &config.HTTPProbe{Port: 80, Path: "/", Expect: config.Expect{Status: 200}},
							Timeout: config.Duration(60 * time.Second),
						},
					},
				}},
			},
		},
		Redactor: redact.New(),
	}
	d := cfg.Drills["d"]
	d.Checks = parseChecks(t, checksYAML)
	cfg.Drills["d"] = d
	return cfg
}

func newDockerRunner(t *testing.T, cfg *config.Config) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := New(cfg, st, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), 1)
	return r, st
}

func TestDockerHTTPServiceDrillPass(t *testing.T) {
	c := requireDocker(t)
	buildProbeImage(t, c)
	httpd := buildHTTPDImage(t, c)
	src, repo := setupResticFixture(t)
	base := t.TempDir()
	cfg := dockerDrillConfig(t, repo, src, base, httpd, `
- type: http
  service: app
  port: 80
  path: /index.html
  expect: { status: 200, contains: restored-ok }
`)
	r, st := newDockerRunner(t, cfg)
	run, err := r.Run(context.Background(), "d", RunOptions{Trigger: store.TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusPass {
		t.Fatalf("expected pass, got %s (%s: %s)", run.Status, run.ErrorKind, run.ErrorMessage)
	}
	results, _ := st.ListCheckResults(run.ID)
	var sawHealthy, sawHTTP bool
	for _, res := range results {
		if res.Type == "service_healthy" && res.Status == store.StatusPass {
			sawHealthy = true
		}
		if res.Type == "http" && res.Status == store.StatusPass {
			sawHTTP = true
		}
	}
	if !sawHealthy || !sawHTTP {
		t.Fatalf("expected passing service_healthy and http, got %+v", results)
	}
	assertNoLeftovers(t, c, cfg.WorkDir, run.ID)
}

func TestDockerHTTPServiceDrillBrokenFails(t *testing.T) {
	c := requireDocker(t)
	buildProbeImage(t, c)
	httpd := buildHTTPDImage(t, c)
	src, repo := setupResticFixture(t)
	base := t.TempDir()
	// Expect content that is not in the restored page -> the drill must fail.
	cfg := dockerDrillConfig(t, repo, src, base, httpd, `
- type: http
  service: app
  port: 80
  path: /index.html
  expect: { status: 200, contains: this-string-is-not-present }
`)
	r, _ := newDockerRunner(t, cfg)
	run, err := r.Run(context.Background(), "d", RunOptions{Trigger: store.TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.StatusFail {
		t.Fatalf("expected fail for broken expectation, got %s", run.Status)
	}
	assertNoLeftovers(t, c, cfg.WorkDir, run.ID)
}

func TestDockerDrillCancelledLeavesNothing(t *testing.T) {
	c := requireDocker(t)
	buildProbeImage(t, c)
	httpd := buildHTTPDImage(t, c)
	src, repo := setupResticFixture(t)
	base := t.TempDir()
	cfg := dockerDrillConfig(t, repo, src, base, httpd, `
- type: http
  service: app
  port: 80
  path: /index.html
  expect: { status: 200 }
`)
	r, _ := newDockerRunner(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel almost immediately so the run is interrupted mid-lifecycle.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		cancel()
	}()
	run, err := r.Run(ctx, "d", RunOptions{Trigger: store.TriggerCLI})
	if err != nil {
		t.Fatal(err)
	}
	// Give teardown a moment, then assert nothing with the run label remains.
	assertNoLeftovers(t, c, cfg.WorkDir, run.ID)
}

// assertNoLeftovers checks that no container, network or sandbox directory with
// the run's label/ID survives.
func assertNoLeftovers(t *testing.T, c *sandbox.DockerClient, workDir, runID string) {
	t.Helper()
	ctx := context.Background()
	containers, err := c.ContainersByLabel(ctx, sandbox.LabelKey, runID)
	if err == nil && len(containers) != 0 {
		t.Fatalf("containers remain for run %s: %+v", runID, containers)
	}
	networks, err := c.AllManagedNetworks(ctx)
	if err == nil {
		for _, n := range networks {
			if n.Labels[sandbox.LabelKey] == runID {
				t.Fatalf("network remains for run %s: %+v", runID, n)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, runID)); !os.IsNotExist(err) {
		t.Fatalf("sandbox directory remains for run %s", runID)
	}
}