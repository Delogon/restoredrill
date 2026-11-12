package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Last result per drill",
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

		type row struct {
			Drill      string    `json:"drill"`
			Status     string    `json:"status"`
			LastRun    time.Time `json:"last_run,omitempty"`
			SnapshotID string    `json:"snapshot_id,omitempty"`
			RunID      string    `json:"run_id,omitempty"`
		}
		var rows []row
		for _, name := range cfg.DrillNames() {
			last, err := st.LastRun(name)
			if err != nil {
				return exitCodeError{exitInternal, err.Error()}
			}
			rw := row{Drill: name, Status: "never run"}
			if last.ID != "" {
				rw.Status = last.Status
				rw.LastRun = last.StartedAt
				rw.SnapshotID = last.SnapshotID
				rw.RunID = last.ID
			}
			rows = append(rows, rw)
		}
		if statusJSON {
			return writeJSON(jsonEnvelope(rows))
		}
		fmt.Printf("%-24s %-12s %-22s %s\n", "DRILL", "STATUS", "LAST RUN", "SNAPSHOT")
		for _, rw := range rows {
			last := "-"
			if !rw.LastRun.IsZero() {
				last = rw.LastRun.Format(time.RFC3339)
			}
			snap := rw.SnapshotID
			if len(snap) > 12 {
				snap = snap[:12]
			}
			fmt.Printf("%-24s %-12s %-22s %s\n", rw.Drill, rw.Status, last, snap)
		}
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "output JSON")
	rootCmd.AddCommand(statusCmd)
}