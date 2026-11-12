package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/restoredrill/restoredrill/internal/metrics"
	"github.com/restoredrill/restoredrill/internal/notify"
	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/scheduler"
	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/restoredrill/restoredrill/internal/web"
	"github.com/spf13/cobra"
)

var serveConcurrency int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the scheduler, dashboard, API and metrics",
	Long: "serve runs scheduled drills and serves the web dashboard, JSON API,\n" +
		"Prometheus metrics and health endpoints in one process.\n\n" +
		"It binds to 127.0.0.1:8484 by default. Binding to any other address\n" +
		"requires server.auth.mode basic or trusted_header.",
	RunE: func(cmd *cobra.Command, args []string) error {
		log := setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		printWarnings(cfg)

		// Refuse unsafe binds before doing anything else (fail closed).
		if err := web.CheckBind(cfg.Server.Listen, cfg.Server.Auth.Mode); err != nil {
			return exitCodeError{exitUsage, err.Error()}
		}

		st, err := openStore(cfg)
		if err != nil {
			return err
		}
		defer st.Close()

		// Crash recovery: remove leftovers from runs that are no longer active.
		if _, err := recoverLeftovers(cmd.Context(), cfg, st, log); err != nil {
			log.Warn("crash recovery incomplete", "err", err)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		mc := metrics.New()
		r := runner.New(cfg, st, log, serveConcurrency)
		r.Metrics = mc.Update
		r.Notify = notify.New(cfg, st, log).Notify

		// Seed metrics from history so /metrics is correct right after a
		// restart, before any new run finishes.
		seedMetrics(st, mc, log)

		sched, err := scheduler.New(cfg, r, log)
		if err != nil {
			return exitCodeError{exitUsage, err.Error()}
		}
		if err := sched.Start(ctx); err != nil {
			return exitCodeError{exitUsage, err.Error()}
		}

		srvWeb, err := web.New(ctx, cfg, st, r, sched, mc, log)
		if err != nil {
			return exitCodeError{exitUsage, err.Error()}
		}
		httpSrv := &http.Server{
			Addr:              cfg.Server.Listen,
			Handler:           srvWeb.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			// No WriteTimeout: SSE streams stay open for the whole run.
		}
		errCh := make(chan error, 1)
		go func() {
			log.Info("dashboard listening", "addr", cfg.Server.Listen, "auth", cfg.Server.Auth.Mode)
			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()

		select {
		case err := <-errCh:
			sched.Stop()
			return exitCodeError{exitInternal, fmt.Sprintf("cannot listen on %s: %v", cfg.Server.Listen, err)}
		case <-ctx.Done():
			log.Info("shutting down")
		}

		// Stop scheduling new runs, then give active runs time to tear down.
		sched.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("http shutdown", "err", err)
		}
		waitForRuns(r, 30*time.Second, log)
		return nil
	},
}

func init() {
	serveCmd.Flags().IntVar(&serveConcurrency, "concurrency", 1, "maximum number of drills running at once")
	rootCmd.AddCommand(serveCmd)
}

// seedMetrics initialises the collector from the newest run of every drill.
func seedMetrics(st *store.Store, mc *metrics.Collector, log *slog.Logger) {
	last, err := st.LastRunsAll()
	if err != nil {
		log.Warn("cannot seed metrics", "err", err)
		return
	}
	for _, run := range last {
		results, err := st.ListCheckResults(run.ID)
		if err != nil {
			continue
		}
		mc.Update(run, results)
	}
}

// waitForRuns waits until no runs are active (teardown finished) or the grace
// period expires.
func waitForRuns(r *runner.Runner, grace time.Duration, log *slog.Logger) {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if len(r.ActiveRunIDs()) == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Warn("active runs did not finish within the grace period", "grace", grace.String())
}