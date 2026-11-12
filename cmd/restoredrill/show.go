package main

import (
	"fmt"
	"time"

	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/spf13/cobra"
)

var (
	showLogs bool
	showJSON bool
)

var showCmd = &cobra.Command{
	Use:   "show <run-id>",
	Short: "Details and logs of one run",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := openStore(cfg)
		if err != nil {
			return err
		}
		defer st.Close()
		run, err := st.GetRun(args[0])
		if err != nil {
			return exitCodeError{exitUsage, fmt.Sprintf("run %q not found", args[0])}
		}
		results, err := st.ListCheckResults(run.ID)
		if err != nil {
			return exitCodeError{exitInternal, err.Error()}
		}
		var events []store.RunEvent
		if showLogs || showJSON {
			events, err = st.ListEvents(run.ID)
			if err != nil {
				return exitCodeError{exitInternal, err.Error()}
			}
		}
		if showJSON {
			return writeJSON(jsonEnvelope(map[string]any{
				"run":    run,
				"checks": results,
				"logs":   events,
			}))
		}

		fmt.Printf("Run %s\n", run.ID)
		fmt.Printf("  drill:      %s\n", run.Drill)
		fmt.Printf("  status:     %s\n", statusIcon(run.Status))
		fmt.Printf("  trigger:    %s\n", run.Trigger)
		fmt.Printf("  started:    %s\n", run.StartedAt.Format(time.RFC3339))
		if !run.FinishedAt.IsZero() {
			fmt.Printf("  finished:   %s (%s)\n", run.FinishedAt.Format(time.RFC3339), run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond))
		}
		fmt.Printf("  repository: %s (%s)\n", run.Repository, run.Backend)
		fmt.Printf("  snapshot:   %s (time %s, host %s)\n", run.SnapshotID, run.SnapshotTime.Format(time.RFC3339), run.SnapshotHost)
		fmt.Printf("  restored:   %d files, %d bytes in %.1fs\n", run.RestoredFiles, run.RestoredBytes, run.RestoreSeconds)
		if run.ErrorMessage != "" {
			fmt.Printf("  error:      [%s] %s\n", run.ErrorKind, run.ErrorMessage)
		}
		if run.KeptPath != "" {
			fmt.Printf("  kept:       %s\n", run.KeptPath)
		}
		fmt.Println("\nChecks:")
		for _, c := range results {
			fmt.Printf("  [%s] %s (%s, %dms): %s\n", c.Status, c.Name, c.Phase, c.DurationMS, c.Message)
		}
		if showLogs {
			fmt.Println("\nLog:")
			for _, e := range events {
				fmt.Printf("  %s %-5s %-10s %s\n", e.TS.Format(time.RFC3339), e.Level, e.Step, e.Message)
			}
		}
		return nil
	},
}

func init() {
	showCmd.Flags().BoolVar(&showLogs, "logs", false, "include the run log")
	showCmd.Flags().BoolVar(&showJSON, "json", false, "output JSON")
	rootCmd.AddCommand(showCmd)
}