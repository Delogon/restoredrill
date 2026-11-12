package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/spf13/cobra"
)

var (
	runAll      bool
	runSnapshot string
	runKeep     bool
	runNoNotify bool
	runJSON     bool
)

var runCmd = &cobra.Command{
	Use:   "run [<drill>... | --all]",
	Short: "Run drills now",
	RunE: func(cmd *cobra.Command, args []string) error {
		log := setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		printWarnings(cfg)

		var names []string
		if runAll {
			names = cfg.DrillNames()
		} else {
			names = args
		}
		if len(names) == 0 {
			return exitCodeError{exitUsage, "specify at least one drill or use --all"}
		}
		for _, n := range names {
			if _, ok := cfg.Drills[n]; !ok {
				return exitCodeError{exitUsage, fmt.Sprintf("drill %q is not defined", n)}
			}
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

		// Cancel runs cleanly on SIGINT/SIGTERM so teardown still happens.
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		r := runner.New(cfg, st, log, 1)
		var runs []store.Run
		for _, n := range names {
			run, err := r.Run(ctx, n, runner.RunOptions{
				Trigger:    store.TriggerCLI,
				SnapshotID: runSnapshot,
				Keep:       runKeep,
				NoNotify:   runNoNotify,
			})
			if err != nil {
				return exitCodeError{exitInternal, err.Error()}
			}
			runs = append(runs, run)
		}

		if runJSON {
			if err := writeJSON(jsonEnvelope(runs)); err != nil {
				return exitCodeError{exitInternal, err.Error()}
			}
		} else {
			printRunSummary(runs)
		}
		return exitCodeForRuns(runs)
	},
}

func init() {
	runCmd.Flags().BoolVar(&runAll, "all", false, "run all drills")
	runCmd.Flags().StringVar(&runSnapshot, "snapshot", "", "override snapshot selection with a specific ID")
	runCmd.Flags().BoolVar(&runKeep, "keep", false, "keep the sandbox after the run")
	runCmd.Flags().BoolVar(&runNoNotify, "no-notify", false, "skip notifications")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "output JSON")
	rootCmd.AddCommand(runCmd)
}

// exitCodeForRuns maps run statuses to the process exit code.
func exitCodeForRuns(runs []store.Run) error {
	anyFail, anyError := false, false
	for _, r := range runs {
		switch r.Status {
		case store.StatusFail:
			anyFail = true
		case store.StatusError:
			anyError = true
		}
	}
	switch {
	case anyFail:
		return exitCodeError{exitFail, ""}
	case anyError:
		return exitCodeError{exitError, ""}
	default:
		return nil
	}
}

func printRunSummary(runs []store.Run) {
	fmt.Printf("\n%-24s %-10s %-12s %-10s %s\n", "DRILL", "STATUS", "SNAPSHOT", "DURATION", "MESSAGE")
	for _, r := range runs {
		dur := ""
		if !r.FinishedAt.IsZero() {
			dur = r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond).String()
		}
		snap := r.SnapshotID
		if len(snap) > 12 {
			snap = snap[:12]
		}
		msg := r.ErrorMessage
		if msg == "" {
			msg = summarizeChecks(r)
		}
		fmt.Printf("%-24s %-10s %-12s %-10s %s\n", r.Drill, statusIcon(r.Status), snap, dur, msg)
	}
}

func summarizeChecks(r store.Run) string {
	if r.Status == store.StatusPass {
		return fmt.Sprintf("%d files, %d bytes restored", r.RestoredFiles, r.RestoredBytes)
	}
	return ""
}

// statusIcon shows status with text and a symbol, not colour alone.
func statusIcon(status string) string {
	switch status {
	case store.StatusPass:
		return "pass ✔"
	case store.StatusFail:
		return "fail ✘"
	case store.StatusError:
		return "error !"
	case store.StatusSkipped:
		return "skipped »"
	default:
		return status
	}
}