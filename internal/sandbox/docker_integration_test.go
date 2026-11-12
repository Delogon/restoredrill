//go:build docker

// Container integration tests. These require a reachable Docker/Podman socket
// and pull small images (busybox, postgres:16-alpine). Run with:
//
//	TMPDIR=<big-disk> go test -tags docker ./internal/sandbox/ ./internal/runner/
//
// They are skipped automatically when no container runtime is reachable.
package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
)

// requireDocker returns a client, skipping the test if Docker is unreachable.
func requireDocker(t *testing.T) *DockerClient {
	t.Helper()
	c, err := NewDockerClient()
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

// repoRoot finds the module root (the directory containing go.mod).
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

// buildProbeImage builds a minimal image containing the restoredrill binary so
// the probe container can run `restoredrill probe`. It sets
// RESTOREDRILL_PROBE_IMAGE and returns the image tag.
func buildProbeImage(t *testing.T, c *DockerClient) string {
	t.Helper()
	root := repoRoot(t)
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "restoredrill")
	build := exec.Command("go", "build", "-o", bin, "./cmd/restoredrill")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	binBytes, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM busybox\nCOPY restoredrill /usr/local/bin/restoredrill\n"
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
		mode int64
	}{
		{"Dockerfile", []byte(dockerfile), 0o644},
		{"restoredrill", binBytes, 0o755},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	tag := "restoredrill-probe:test-" + filepath.Base(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := c.BuildImage(ctx, buf.Bytes(), tag); err != nil {
		t.Fatalf("build probe image: %v", err)
	}
	t.Setenv("RESTOREDRILL_PROBE_IMAGE", tag)
	t.Cleanup(func() {
		// Best-effort image removal.
		_ = c.do(context.Background(), "DELETE", "/images/"+tag, nil, nil)
	})
	return tag
}

func TestDockerIsolatedNetworkNoEgress(t *testing.T) {
	c := requireDocker(t)
	ctx := context.Background()
	rt := NewRuntime(c, "01EGRESS", "drill", nil, func(s string) { t.Log(s) })
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer rt.Teardown(context.Background())

	// A container on the isolated network must not reach the internet.
	exit, out, _, err := rt.RunOnce(ctx, "busybox", []string{"sh", "-c", "wget -T 3 -q -O /dev/null http://example.com && echo EGRESS_OK || echo EGRESS_BLOCKED"}, nil, nil)
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	_ = exit
	if !bytes.Contains([]byte(out), []byte("EGRESS_BLOCKED")) {
		t.Fatalf("expected egress to be blocked, got: %s", out)
	}
}

func TestDockerRunOnceExitAndOutput(t *testing.T) {
	c := requireDocker(t)
	ctx := context.Background()
	rt := NewRuntime(c, "01RUNONCE", "drill", nil, func(s string) { t.Log(s) })
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer rt.Teardown(context.Background())

	exit, out, _, err := rt.RunOnce(ctx, "busybox", []string{"sh", "-c", "echo hello; exit 7"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exit != 7 {
		t.Fatalf("exit=%d, want 7", exit)
	}
	if !bytes.Contains([]byte(out), []byte("hello")) {
		t.Fatalf("output=%q", out)
	}
}

func TestDockerTeardownLeavesNothing(t *testing.T) {
	c := requireDocker(t)
	ctx := context.Background()
	runID := "01TEARDOWN"
	rt := NewRuntime(c, runID, "drill", nil, func(s string) { t.Log(s) })
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartService(ctx, "app", serviceCfg(), ""); err != nil {
		t.Fatal(err)
	}
	rt.Teardown(context.Background())

	containers, err := c.ContainersByLabel(ctx, LabelKey, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 0 {
		t.Fatalf("containers remain after teardown: %+v", containers)
	}
	networks, err := c.AllManagedNetworks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range networks {
		if n.Labels[LabelKey] == runID {
			t.Fatalf("network remains after teardown: %+v", n)
		}
	}
}

func TestDockerGCRemovesLeftovers(t *testing.T) {
	c := requireDocker(t)
	ctx := context.Background()
	runID := "01GCLEFT"
	rt := NewRuntime(c, runID, "drill", nil, func(s string) { t.Log(s) })
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartService(ctx, "app", serviceCfg(), ""); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash: do NOT teardown. GC must remove the leftovers.
	n, err := GC(ctx, c, nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("GC removed %d, want >=2 (container+network)", n)
	}
	containers, _ := c.ContainersByLabel(ctx, LabelKey, runID)
	if len(containers) != 0 {
		t.Fatalf("containers remain after GC: %+v", containers)
	}
}

func TestDockerPostgresRestore(t *testing.T) {
	c := requireDocker(t)
	ctx := context.Background()
	rt := NewRuntime(c, "01PGRESTORE", "drill", nil, func(s string) { t.Log(s) })
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer rt.Teardown(context.Background())

	// A plain SQL dump standing in for a backed-up database.
	dir := t.TempDir()
	dump := "CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);\n" +
		"INSERT INTO users (name) VALUES ('a'), ('b'), ('c');\n"
	if err := os.WriteFile(filepath.Join(dir, "db.sql"), []byte(dump), 0o644); err != nil {
		t.Fatal(err)
	}
	min := 3.0
	cfg := config.Check{
		Type: "postgres_restore", Dump: "db.sql", Format: "plain", Image: "postgres:16-alpine",
		Queries: []config.SQLQuery{{SQL: "SELECT count(*) FROM users", Expect: config.Expect{Min: &min}}},
	}
	ch, ok := checks.Get("postgres_restore")
	if !ok {
		t.Fatal("postgres_restore not registered")
	}
	cctx := &checks.Context{RestoreDir: dir, Runtime: rt, RunID: "01PGRESTORE"}
	res := ch.Run(ctx, cctx, cfg)
	if res.Status != checks.Pass {
		t.Fatalf("expected pass, got %+v", res)
	}
}

func TestDockerPostgresRestoreBadDumpFails(t *testing.T) {
	c := requireDocker(t)
	ctx := context.Background()
	rt := NewRuntime(c, "01PGBAD", "drill", nil, func(s string) { t.Log(s) })
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer rt.Teardown(context.Background())

	dir := t.TempDir()
	// Invalid SQL must fail to load -> the check fails (never passes).
	if err := os.WriteFile(filepath.Join(dir, "db.sql"), []byte("THIS IS NOT SQL;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Check{Type: "postgres_restore", Dump: "db.sql", Format: "plain", Image: "postgres:16-alpine"}
	ch, _ := checks.Get("postgres_restore")
	cctx := &checks.Context{RestoreDir: dir, Runtime: rt, RunID: "01PGBAD"}
	res := ch.Run(ctx, cctx, cfg)
	if res.Status != checks.Fail {
		t.Fatalf("expected fail for invalid dump, got %+v", res)
	}
}