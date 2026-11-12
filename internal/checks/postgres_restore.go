package checks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
)

// postgresRestore loads a dump into a temporary Postgres container and checks
// that queries meet expectations. It is a data-phase check that needs the
// container runtime; if the runtime is unavailable the check is error.
type postgresRestore struct{}

func init() { Register(postgresRestore{}) }

func (postgresRestore) Type() string  { return "postgres_restore" }
func (postgresRestore) Phase() string { return "data" }

func (postgresRestore) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	if c.Runtime == nil {
		return errorf("postgres_restore: no container runtime available — ensure Docker/Podman is reachable")
	}
	if err := c.Runtime.Available(ctx); err != nil {
		return errorf("postgres_restore: container runtime unavailable: %v", err)
	}
	dumpPath := filepath.Join(c.RestoreDir, filepath.FromSlash(cfg.Dump))
	image := cfg.Image
	if image == "" {
		image = "postgres:16"
	}
	format := cfg.Format
	if format == "" {
		format = "custom"
	}

	// Random password; the container is isolated and short-lived.
	pwBytes := make([]byte, 16)
	if _, err := rand.Read(pwBytes); err != nil {
		return errorf("postgres_restore: cannot generate password: %v", err)
	}
	password := hex.EncodeToString(pwBytes)
	name := "rdpg-" + c.RunID
	env := map[string]string{
		"POSTGRES_PASSWORD": password,
		"POSTGRES_DB":       "restoredb",
	}
	if err := c.Runtime.StartContainer(ctx, name, image, env, nil); err != nil {
		return errorf("postgres_restore: cannot start Postgres container: %v", err)
	}
	defer c.Runtime.StopContainer(context.WithoutCancel(ctx), name)

	if err := waitPostgresReady(ctx, c.Runtime, name); err != nil {
		return errorf("postgres_restore: Postgres did not become ready: %v", err)
	}

	// Copy the dump into the container and load it.
	target := "/tmp/restore-dump"
	if err := c.Runtime.CopyToContainer(ctx, name, dumpPath, target); err != nil {
		return errorf("postgres_restore: cannot copy dump into container: %v", err)
	}
	if err := loadPostgresDump(ctx, c.Runtime, name, format, target); err != nil {
		return failf("postgres_restore: dump failed to load (%s format): %v", format, err)
	}

	// Run the expectation queries.
	for i, q := range cfg.Queries {
		exit, out, stderr, err := c.Runtime.Exec(ctx, name, []string{"psql", "-U", "postgres", "-d", "restoredb", "-tAc", q.SQL})
		if err != nil || exit != 0 {
			return failf("postgres_restore: query[%d] %q failed: %s %v", i, q.SQL, strings.TrimSpace(stderr), err)
		}
		raw := strings.TrimSpace(out)
		value, perr := parseFloat(raw)
		details := map[string]any{"query": q.SQL, "result": raw}
		if perr != nil {
			if q.Expect.HasKey("equals") && raw != q.Expect.Equals {
				return Result{Status: Fail, Message: sprintf("postgres_restore: query[%d] result %q != %q", i, raw, q.Expect.Equals), Details: details}
			}
			continue
		}
		if ok, msg := matchScalar(q.Expect, value, raw); !ok {
			return Result{Status: Fail, Message: sprintf("postgres_restore: query[%d]: %s", i, msg), Details: details}
		}
	}
	return passf("postgres_restore: dump loaded and %d queries satisfied", len(cfg.Queries))
}

func waitPostgresReady(ctx context.Context, rt ContainerRuntime, name string) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		exit, _, _, err := rt.Exec(ctx, name, []string{"pg_isready", "-U", "postgres"})
		if err == nil && exit == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for pg_isready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func loadPostgresDump(ctx context.Context, rt ContainerRuntime, name, format, target string) error {
	var cmd []string
	switch format {
	case "plain":
		cmd = []string{"psql", "-U", "postgres", "-d", "restoredb", "-v", "ON_ERROR_STOP=1", "-f", target}
	case "custom", "directory":
		cmd = []string{"pg_restore", "-U", "postgres", "-d", "restoredb", "--no-owner", "--no-privileges", target}
	default:
		return fmt.Errorf("unknown format %q", format)
	}
	exit, _, stderr, err := rt.Exec(ctx, name, cmd)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("%s", strings.TrimSpace(stderr))
	}
	return nil
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%g", &f)
	return f, err
}