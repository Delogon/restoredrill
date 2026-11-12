// Package scheduler runs drills on a 5-field cron schedule (in the configured
// timezone) with optional jitter and catch-up after downtime. Scheduled and
// manual runs share the runner's concurrency queue and per-drill locks.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/robfig/cron/v3"
)

// Scheduler wraps a cron runner bound to a restoredrill runner.
type Scheduler struct {
	cfg    *config.Config
	runner *runner.Runner
	log    *slog.Logger
	cron   *cron.Cron
	loc    *time.Location
}

// New builds a scheduler. It resolves server.timezone (default: local).
func New(cfg *config.Config, r *runner.Runner, log *slog.Logger) (*Scheduler, error) {
	loc := time.Local
	if cfg.Server.Timezone != "" {
		l, err := time.LoadLocation(cfg.Server.Timezone)
		if err != nil {
			return nil, fmt.Errorf("server.timezone %q is not a valid IANA timezone: %w", cfg.Server.Timezone, err)
		}
		loc = l
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		cfg:    cfg,
		runner: r,
		log:    log,
		cron:   cron.New(cron.WithLocation(loc)),
		loc:    loc,
	}, nil
}

// Start registers every scheduled drill, starts the cron loop and performs
// catch-up runs. It returns an error if a schedule is invalid.
func (s *Scheduler) Start(ctx context.Context) error {
	for _, name := range s.cfg.DrillNames() {
		drill := s.cfg.Drills[name]
		if drill.Schedule == "" {
			continue
		}
		drillName, d := name, drill
		if _, err := s.cron.AddFunc(d.Schedule, func() { s.trigger(ctx, drillName, d) }); err != nil {
			return fmt.Errorf("drill %s: invalid schedule %q: %w", drillName, d.Schedule, err)
		}
		s.log.Info("scheduled drill", "drill", drillName, "schedule", d.Schedule, "timezone", s.loc.String())
	}
	s.cron.Start()
	s.catchUp(ctx)
	return nil
}

// Stop halts the cron loop (running jobs are allowed to finish).
func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
}

// trigger applies jitter then runs the drill. The runner queues it behind the
// global concurrency limit and skips it if the same drill is already running.
func (s *Scheduler) trigger(ctx context.Context, name string, d config.Drill) {
	if d.Jitter > 0 {
		delay := time.Duration(rand.Int63n(int64(d.Jitter.D())))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	if _, err := s.runner.Run(ctx, name, runner.RunOptions{Trigger: store.TriggerSchedule}); err != nil {
		s.log.Error("scheduled run failed to start", "drill", name, "err", err)
	}
}

// catchUp runs a drill once at startup if its last run is older than one
// schedule interval (catch_up defaults to true). This recovers from downtime.
func (s *Scheduler) catchUp(ctx context.Context) {
	for _, name := range s.cfg.DrillNames() {
		d := s.cfg.Drills[name]
		if d.Schedule == "" {
			continue
		}
		if d.CatchUp != nil && !*d.CatchUp {
			continue
		}
		sched, err := cron.ParseStandard(d.Schedule)
		if err != nil {
			continue // invalid schedules are reported by Start
		}
		now := time.Now().In(s.loc)
		next := sched.Next(now)
		interval := sched.Next(next).Sub(next)
		if interval <= 0 {
			continue
		}
		last, err := s.runner.Store.LastRun(name)
		if err != nil {
			s.log.Warn("catch-up: cannot read last run", "drill", name, "err", err)
			continue
		}
		if last.ID != "" && now.Sub(last.StartedAt) <= interval {
			continue // ran recently enough
		}
		s.log.Info("catch-up run after downtime", "drill", name)
		go s.trigger(ctx, name, d)
	}
}

// NextRun returns the next scheduled time for a drill (zero if unscheduled).
func (s *Scheduler) NextRun(name string) time.Time {
	d, ok := s.cfg.Drills[name]
	if !ok || d.Schedule == "" {
		return time.Time{}
	}
	sched, err := cron.ParseStandard(d.Schedule)
	if err != nil {
		return time.Time{}
	}
	return sched.Next(time.Now().In(s.loc))
}