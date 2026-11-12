package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/store"
)

// newTestScheduler builds a scheduler whose drills fail fast in preflight (the
// restic binary does not exist), so runs complete in milliseconds without
// touching any repository.
func newTestScheduler(t *testing.T, drill config.Drill) (*Scheduler, *runner.Runner, *store.Store) {
	t.Helper()
	base := t.TempDir()
	cfg := &config.Config{
		StateDir:  filepath.Join(base, "state"),
		WorkDir:   filepath.Join(base, "work"),
		Retention: config.Retention{RunsPerDrill: 50},
		Binaries:  config.Binaries{Restic: filepath.Join(base, "no-such-restic"), Borg: "borg"},
		Repositories: map[string]config.Repository{
			"r": {Type: "restic", Repository: filepath.Join(base, "repo")},
		},
		Drills:   map[string]config.Drill{"d": drill},
		Redactor: redact.New(),
	}
	st, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := runner.New(cfg, st, log, 1)
	s, err := New(cfg, r, log)
	if err != nil {
		t.Fatal(err)
	}
	return s, r, st
}

// waitForScheduledRun polls until a schedule-triggered run exists.
func waitForScheduledRun(t *testing.T, st *store.Store, want int) []store.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := st.ListRuns("d", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		var scheduled []store.Run
		for _, r := range runs {
			if r.Trigger == store.TriggerSchedule && r.Status != store.StatusRunning {
				scheduled = append(scheduled, r)
			}
		}
		if len(scheduled) >= want {
			return scheduled
		}
		time.Sleep(20 * time.Millisecond)
	}
	runs, _ := st.ListRuns("d", 100, 0)
	t.Fatalf("timed out waiting for %d scheduled run(s), have %+v", want, runs)
	return nil
}

func countScheduled(t *testing.T, st *store.Store) int {
	t.Helper()
	runs, err := st.ListRuns("d", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range runs {
		if r.Trigger == store.TriggerSchedule {
			n++
		}
	}
	return n
}

func TestNewInvalidTimezone(t *testing.T) {
	cfg := &config.Config{Server: config.Server{Timezone: "Not/AZone"}}
	_, err := New(cfg, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestStartInvalidSchedule(t *testing.T) {
	s, _, _ := newTestScheduler(t, config.Drill{Repository: "r", Schedule: "not a cron"})
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("expected error for invalid schedule")
	}
}

func TestNextRun(t *testing.T) {
	s, _, _ := newTestScheduler(t, config.Drill{Repository: "r", Schedule: "*/5 * * * *"})
	next := s.NextRun("d")
	if next.IsZero() {
		t.Fatal("NextRun must be set for a scheduled drill")
	}
	until := time.Until(next)
	if until <= 0 || until > 5*time.Minute {
		t.Fatalf("next run %v is not within the next 5 minutes", next)
	}
	if next.Minute()%5 != 0 {
		t.Fatalf("next run minute %d is not a multiple of 5", next.Minute())
	}
}

func TestNextRunUnscheduled(t *testing.T) {
	s, _, _ := newTestScheduler(t, config.Drill{Repository: "r"})
	if !s.NextRun("d").IsZero() {
		t.Fatal("unscheduled drill must have no next run")
	}
	if !s.NextRun("missing").IsZero() {
		t.Fatal("unknown drill must have no next run")
	}
}

func TestCatchUpRunsWhenLastRunOld(t *testing.T) {
	s, _, st := newTestScheduler(t, config.Drill{Repository: "r", Schedule: "0 4 * * *"})
	// Last run is 48h old: older than one schedule interval (24h).
	old := store.Run{ID: "01OLD", Drill: "d", Trigger: store.TriggerSchedule, Status: store.StatusPass,
		StartedAt: time.Now().Add(-48 * time.Hour), FinishedAt: time.Now().Add(-48 * time.Hour)}
	if err := st.InsertRun(old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	runs := waitForScheduledRun(t, st, 2) // old one + catch-up
	if len(runs) != 2 {
		t.Fatalf("expected exactly one catch-up run, got %d", len(runs)-1)
	}
}

func TestCatchUpSkipsRecentRun(t *testing.T) {
	s, _, st := newTestScheduler(t, config.Drill{Repository: "r", Schedule: "0 4 * * *"})
	recent := store.Run{ID: "01RECENT", Drill: "d", Trigger: store.TriggerSchedule, Status: store.StatusPass,
		StartedAt: time.Now().Add(-10 * time.Second), FinishedAt: time.Now()}
	if err := st.InsertRun(recent); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	time.Sleep(300 * time.Millisecond)
	if n := countScheduled(t, st); n != 1 {
		t.Fatalf("recent run must not trigger catch-up, scheduled runs=%d", n)
	}
}

func TestCatchUpDisabled(t *testing.T) {
	no := false
	s, _, st := newTestScheduler(t, config.Drill{Repository: "r", Schedule: "0 4 * * *", CatchUp: &no})
	old := store.Run{ID: "01OLD", Drill: "d", Trigger: store.TriggerSchedule, Status: store.StatusPass,
		StartedAt: time.Now().Add(-48 * time.Hour), FinishedAt: time.Now().Add(-48 * time.Hour)}
	if err := st.InsertRun(old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	time.Sleep(300 * time.Millisecond)
	if n := countScheduled(t, st); n != 1 {
		t.Fatalf("catch_up: false must not run at startup, scheduled runs=%d", n)
	}
}

func TestCatchUpRunsWhenNoHistory(t *testing.T) {
	s, _, st := newTestScheduler(t, config.Drill{Repository: "r", Schedule: "0 4 * * *"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	waitForScheduledRun(t, st, 1)
}

func TestUnscheduledDrillNotRunAtStartup(t *testing.T) {
	s, _, st := newTestScheduler(t, config.Drill{Repository: "r"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	time.Sleep(200 * time.Millisecond)
	if n := countScheduled(t, st); n != 0 {
		t.Fatalf("unscheduled drill must not run, got %d", n)
	}
}

func TestTriggerCancelledContextDoesNotRun(t *testing.T) {
	s, _, st := newTestScheduler(t, config.Drill{Repository: "r", Jitter: config.Duration(time.Hour)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.trigger(ctx, "d", s.cfg.Drills["d"])
	if n := countScheduled(t, st); n != 0 {
		t.Fatalf("cancelled context must not run, got %d", n)
	}
}

func TestTimezoneApplied(t *testing.T) {
	base := t.TempDir()
	cfg := &config.Config{
		StateDir: base,
		Server:   config.Server{Timezone: "UTC"},
		Drills:   map[string]config.Drill{"d": {Schedule: "30 4 * * *"}},
		Redactor: redact.New(),
	}
	s, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	next := s.NextRun("d")
	if next.Location().String() != "UTC" || next.Hour() != 4 || next.Minute() != 30 {
		t.Fatalf("next run %v not at 04:30 UTC", next)
	}
}