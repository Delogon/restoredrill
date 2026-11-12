package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/store"
)

func TestAggregate(t *testing.T) {
	cases := []struct {
		name   string
		status []string
		want   string
	}{
		{"all pass", []string{"pass", "pass"}, "pass"},
		{"empty", nil, "pass"},
		{"one fail", []string{"pass", "fail"}, "fail"},
		{"one error", []string{"pass", "error"}, "error"},
		{"fail beats error", []string{"error", "fail"}, "fail"},
		{"skipped ignored", []string{"pass", "skipped"}, "pass"},
		{"only skipped", []string{"skipped"}, "pass"},
	}
	for _, c := range cases {
		var results []store.CheckResult
		for _, s := range c.status {
			results = append(results, store.CheckResult{Status: s})
		}
		if got := aggregate(results); got != c.want {
			t.Errorf("%s: aggregate=%s want %s", c.name, got, c.want)
		}
	}
}

func TestPercentDropHelper(t *testing.T) {
	// percentDrop lives in checks; sanity-check walkStats here instead.
	dir := t.TempDir()
	if err := writeTree(dir); err != nil {
		t.Fatal(err)
	}
	bytes, files, err := walkStats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 6 {
		t.Fatalf("walkStats = %d bytes, %d files; want 6, 2", bytes, files)
	}
}

func writeTree(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("abc"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "sub", "b"), []byte("xyz"), 0o644)
}

func TestEffectiveChecksImplicitSnapshotAge(t *testing.T) {
	d := config.Drill{Snapshot: config.Snapshot{MaxAge: config.Duration(time.Hour)}}
	got := effectiveChecks(d)
	if len(got) != 1 || got[0].Type != "snapshot_age" {
		t.Fatalf("expected implicit snapshot_age, got %+v", got)
	}
}

func TestEffectiveChecksImplicitServiceHealthy(t *testing.T) {
	d := config.Drill{
		Sandbox: &config.Sandbox{Services: map[string]config.Service{
			"app": {Image: "x", Healthcheck: &config.Healthcheck{
				HTTP:    &config.HTTPProbe{Port: 80, Path: "/alive", Expect: config.Expect{Status: 200}},
				Timeout: config.Duration(2 * time.Minute),
			}},
			"db": {Image: "y"}, // no healthcheck -> no implicit check
		}},
	}
	got := effectiveChecks(d)
	var healthy []config.Check
	for _, c := range got {
		if c.Type == "service_healthy" {
			healthy = append(healthy, c)
		}
	}
	if len(healthy) != 1 {
		t.Fatalf("expected 1 implicit service_healthy, got %+v", got)
	}
	h := healthy[0]
	if h.Service != "app" || h.Port != 80 || h.Path != "/alive" {
		t.Fatalf("healthcheck HTTP params not propagated: %+v", h)
	}
	if h.Timeout.D() != 2*time.Minute {
		t.Fatalf("timeout=%v", h.Timeout.D())
	}
}

func TestEffectiveChecksExplicitServiceHealthyNotDuplicated(t *testing.T) {
	d := config.Drill{
		Sandbox: &config.Sandbox{Services: map[string]config.Service{
			"app": {Image: "x", Healthcheck: &config.Healthcheck{HTTP: &config.HTTPProbe{Port: 80}}},
		}},
		Checks: []config.Check{{Type: "service_healthy", Service: "app"}},
	}
	got := effectiveChecks(d)
	n := 0
	for _, c := range got {
		if c.Type == "service_healthy" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("explicit service_healthy duplicated: %d", n)
	}
}

func TestDrillNeedsHTTPProbe(t *testing.T) {
	withHTTP := config.Drill{Checks: []config.Check{{Type: "http", Service: "app", Port: 80}}}
	if !drillNeedsHTTPProbe(withHTTP) {
		t.Fatal("http check should need probe")
	}
	withHC := config.Drill{Sandbox: &config.Sandbox{Services: map[string]config.Service{
		"app": {Image: "x", Healthcheck: &config.Healthcheck{HTTP: &config.HTTPProbe{Port: 80}}},
	}}}
	if !drillNeedsHTTPProbe(withHC) {
		t.Fatal("http healthcheck should need probe")
	}
	fileOnly := config.Drill{Checks: []config.Check{{Type: "files_exist"}}}
	if drillNeedsHTTPProbe(fileOnly) {
		t.Fatal("file-only drill should not need probe")
	}
	pgOnly := config.Drill{Checks: []config.Check{{Type: "postgres_restore", Dump: "d"}}}
	if drillNeedsHTTPProbe(pgOnly) {
		t.Fatal("postgres-only drill should not need probe image")
	}
}

func TestDrillNeedsContainerChecks(t *testing.T) {
	for _, typ := range []string{"postgres_restore", "http", "command", "service_healthy"} {
		d := config.Drill{Checks: []config.Check{{Type: typ}}}
		if !drillNeedsContainerChecks(d) {
			t.Fatalf("%s should need containers", typ)
		}
	}
	d := config.Drill{Checks: []config.Check{{Type: "sqlite_query"}}}
	if drillNeedsContainerChecks(d) {
		t.Fatal("sqlite_query should not need containers")
	}
}