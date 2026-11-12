package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	historyLimit int
	historyJSON  bool
)

var historyCmd = &cobra.Command{
	Use:   "history <drill>",
	Short: "Past runs of one drill",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		name := args[0]
		if _, ok := cfg.Drills[name]; !ok {
			return exitCodeError{exitUsage, fmt.Sprintf("drill %q is not defined", name)}
		}
		st, err := openStore(cfg)
		if err != nil {
			return err
		}
		defer st.Close()
		runs, err := st.ListRuns(name, historyLimit, 0)
		if err != nil {
			return exitCodeError{exitInternal, err.Error()}
		}
		if historyJSON {
			return writeJSON(jsonEnvelope(runs))
		}
		if len(runs) == 0 {
			fmt.Println("No runs yet.")
			return nil
		}
		fmt.Printf("%-28s %-10s %-22s %-12s %s\n", "RUN", "STATUS", "STARTED", "SNAPSHOT", "DURATION")
		for _, r := range runs {
			dur := ""
			if !r.FinishedAt.IsZero() {
				dur = r.FinishedAt.Sub(r.StartedAt).Round(time.Millisecond).String()
			}
			snap := r.SnapshotID
			if len(snap) > 12 {
				snap = snap[:12]
			}
			fmt.Printf("%-28s %-10s %-22s %-12s %s\n", r.ID, statusIcon(r.Status), r.StartedAt.Format(time.RFC3339), snap, dur)
		}
		return nil
	},
}

func init() {
	historyCmd.Flags().IntVar(&historyLimit, "limit", 20, "maximum number of runs to show")
	historyCmd.Flags().BoolVar(&historyJSON, "json", false, "output JSON")
	rootCmd.AddCommand(historyCmd)
}