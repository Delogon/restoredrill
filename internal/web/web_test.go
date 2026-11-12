package web

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/metrics"
	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/store"
)

var update = flag.Bool("update", false, "update golden files")

// fixedNow makes relative times deterministic for golden tests.
var fixedNow = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

// baseConfig builds a minimal valid config for tests. The restic binary does
// not exist, so triggered runs fail fast in preflight.
func baseConfig(t *testing.T) *config.Config {
	t.Helper()
	base := t.TempDir()
	return &config.Config{
		Version:   1,
		StateDir:  base,
		WorkDir:   filepath.Join(base, "work"),
		Retention: config.Retention{RunsPerDrill: 200},
		Binaries:  config.Binaries{Restic: filepath.Join(base, "no-such-restic"), Borg: "borg"},
		Repositories: map[string]config.Repository{
			"r": {Type: "restic", Repository: filepath.Join(base, "repo"), PasswordFile: "/run/secrets/repo.pass"},
		},
		Drills: map[string]config.Drill{
			"alpha": {
				Repository: "r",
				Schedule:   "30 4 * * *",
				Snapshot:   config.Snapshot{Select: "latest"},
				Restore:    config.Restore{Paths: []string{"/srv/app"}},
				Checks:     []config.Check{{Type: "files_exist", Paths: []string{"srv/app"}}},
			},
			"beta": {Repository: "r"},
		},
		Server:   config.Server{Listen: "127.0.0.1:8484", Auth: config.Auth{Mode: "none"}, Timezone: "UTC"},
		Redactor: redact.New(),
	}
}

// newServerForTest opens a store and calls New, returning its error.
func newServerForTest(t *testing.T, cfg *config.Config) (*Server, error) {
	t.Helper()
	st, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := runner.New(cfg, st, log, 1)
	return New(context.Background(), cfg, st, r, nil, metrics.New(), log)
}

// fixture builds a server with two drills and a small run history.
func fixture(t *testing.T, mutate func(*config.Config)) (*Server, *store.Store) {
	t.Helper()
	cfg := baseConfig(t)
	if mutate != nil {
		mutate(cfg)
	}
	st, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := runner.New(cfg, st, log, 1)
	s, err := New(context.Background(), cfg, st, r, nil, metrics.New(), log)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return fixedNow }
	seedHistory(t, st)
	return s, st
}

func seedHistory(t *testing.T, st *store.Store) {
	t.Helper()
	mk := func(id, drill, status string, startMin int, snap time.Time) store.Run {
		t.Helper()
		start := fixedNow.Add(-time.Duration(startMin) * time.Minute)
		r := store.Run{
			ID: id, Drill: drill, Trigger: store.TriggerSchedule, Status: status,
			StartedAt: start, FinishedAt: start.Add(90 * time.Second),
			Repository: "r", Backend: "restic",
			SnapshotID: "snap" + id, SnapshotTime: snap, SnapshotHost: "location1",
			RestoredBytes: 2048, RestoredFiles: 4, RestoreSeconds: 1.5,
		}
		if status == store.StatusError {
			r.ErrorKind = "repo"
			r.ErrorMessage = "repository unreachable"
		}
		if err := st.InsertRun(r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	snapTime := fixedNow.Add(-3 * time.Hour)
	mk("01A1", "alpha", store.StatusPass, 120, snapTime)
	mk("01A2", "alpha", store.StatusFail, 30, snapTime)
	mk("01B1", "beta", store.StatusError, 60, snapTime)

	if err := st.InsertCheckResult(store.CheckResult{
		RunID: "01A2", Seq: 0, Name: "files_exist[0]", Type: "files_exist", Phase: "data",
		Status: store.StatusFail, StartedAt: fixedNow.Add(-29 * time.Minute), DurationMS: 12,
		Message: "missing 1 of 1 paths", DetailsJSON: `{"missing":["srv/app"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	for i, msg := range []string{"restic backend ready", "selected snapshot snap01A2", "restore started", "files_exist[0]: fail — missing 1 of 1 paths"} {
		if err := st.InsertEvent(store.RunEvent{
			RunID: "01A2", TS: fixedNow.Add(-time.Duration(30-i) * time.Minute), Level: "info", Step: "preflight", Message: msg,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// get performs a GET with a fixed session cookie (deterministic CSRF token).
func get(s *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "testsession"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func mustBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	b, _ := io.ReadAll(rec.Result().Body)
	return string(b)
}

func TestOverviewPage(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, mustBody(t, rec))
	}
	body := mustBody(t, rec)
	for _, want := range []string{
		"alpha", "beta",
		`badge fail`, "✘ fail", // alpha's newest run failed
		`badge error`, "⚠ error",
		`href="/drills/alpha"`, `href="/runs/01A2"`,
		"Run now", "csrf_token",
		`data-sse="/events"`,
		"30 4 * * *",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q", want)
		}
	}
	// No inline scripts or styles (strict CSP).
	if strings.Contains(body, "<script>") || strings.Contains(body, "style=") {
		t.Errorf("overview contains inline script/style")
	}
}

func TestDrillPage(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/drills/alpha")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := mustBody(t, rec)
	for _, want := range []string{
		"Run history", "Resolved configuration", "Trends",
		"01A2", "01A1", "files_exist", "/run/secrets/repo.pass",
		"<svg", // sparkline
	} {
		if !strings.Contains(body, want) {
			t.Errorf("drill page missing %q", want)
		}
	}
}

func TestDrillPageNotFound(t *testing.T) {
	s, _ := fixture(t, nil)
	if rec := get(s, "/drills/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRunPage(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/runs/01A2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := mustBody(t, rec)
	for _, want := range []string{
		"01A2", "snap01A2", "location1", "files_exist[0]", "missing 1 of 1 paths",
		"missing", "download full log", "Steps", "Checks",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
}

func TestRunPageNotFound(t *testing.T) {
	s, _ := fixture(t, nil)
	if rec := get(s, "/runs/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestLivePage(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/runs/01A2/live")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if body := mustBody(t, rec); !strings.Contains(body, `data-events="/runs/01A2/events"`) {
		t.Errorf("live page missing SSE data attribute")
	}
}

func TestLogDownload(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/runs/01A2/logs.txt")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type=%q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "01A2.log") {
		t.Fatalf("content-disposition=%q", cd)
	}
	if body := mustBody(t, rec); !strings.Contains(body, "selected snapshot snap01A2") {
		t.Errorf("log missing events")
	}
}

func TestSecurityHeaders(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/")
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP allows unsafe-inline: %s", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing nosniff")
	}
}

func TestAssetsServed(t *testing.T) {
	s, _ := fixture(t, nil)
	for _, p := range []string{"/assets/app.css", "/assets/app.js"} {
		rec := get(s, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", p, rec.Code)
		}
	}
}

func TestHealthEndpoints(t *testing.T) {
	s, _ := fixture(t, nil)
	for _, p := range []string{"/healthz", "/readyz"} {
		rec := get(s, p)
		if rec.Code != http.StatusOK || strings.TrimSpace(mustBody(t, rec)) != "ok" {
			t.Fatalf("%s: status=%d body=%q", p, rec.Code, mustBody(t, rec))
		}
	}
}

func TestMetricsEndpoint(t *testing.T) {
	s, st := fixture(t, nil)
	// Update metrics from the seeded history, then scrape.
	last, _ := st.LastRunsAll()
	for _, run := range last {
		results, _ := st.ListCheckResults(run.ID)
		s.metrics.Update(run, results)
	}
	rec := get(s, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := mustBody(t, rec)
	for _, want := range []string{
		`restoredrill_drill_status{drill="alpha",status="fail"} 1`,
		`restoredrill_drill_last_run_timestamp_seconds{drill="alpha"}`,
		`restoredrill_check_status{check="files_exist[0]",drill="alpha",status="fail"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// golden compares a rendered page against testdata/<name>.golden. Run with
// -update to regenerate. Times and the CSRF token are fixed in fixtures.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file missing (run go test ./internal/web -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s; run go test ./internal/web -update to regenerate", name)
	}
}

func TestGoldenPages(t *testing.T) {
	s, _ := fixture(t, func(cfg *config.Config) {
		// Pin the repository path so the resolved-config YAML is deterministic
		// (the fixture's t.TempDir() path is random per run).
		repo := cfg.Repositories["r"]
		repo.Repository = "/var/backups/restic-repo"
		cfg.Repositories["r"] = repo
	})
	s.csrfSecret = []byte("fixed-secret-for-golden-tests!!!") // 32 bytes
	for _, tc := range []struct{ path, name string }{
		{"/", "overview"},
		{"/drills/alpha", "drill"},
		{"/runs/01A2", "run"},
	} {
		rec := get(s, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", tc.path, rec.Code)
		}
		golden(t, tc.name, mustBody(t, rec))
	}
}

// TestOverviewPerformance asserts the plan's target: the overview stays under
// one second with 50 drills and 200 runs each.
func TestOverviewPerformance(t *testing.T) {
	s, st := fixture(t, func(cfg *config.Config) {
		cfg.Drills = map[string]config.Drill{}
		for i := 0; i < 50; i++ {
			cfg.Drills[fmt.Sprintf("drill-%02d", i)] = config.Drill{Repository: "r"}
		}
	})
	// 50 drills x 200 runs.
	seq := 0
	for name := range s.cfg.Drills {
		for j := 0; j < 200; j++ {
			start := fixedNow.Add(-time.Duration(j+1) * time.Minute)
			status := store.StatusPass
			if j%7 == 0 {
				status = store.StatusFail
			}
			seq++
			r := store.Run{
				ID: fmt.Sprintf("01P%023d", seq),
				Drill: name, Trigger: store.TriggerSchedule, Status: status,
				StartedAt: start, FinishedAt: start.Add(time.Second),
			}
			if err := st.InsertRun(r); err != nil {
				t.Fatal(err)
			}
		}
	}
	start := time.Now()
	rec := get(s, "/")
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if elapsed > time.Second {
		t.Fatalf("overview took %v with 50 drills x 200 runs (target < 1s)", elapsed)
	}
	t.Logf("overview rendered in %v", elapsed)
}