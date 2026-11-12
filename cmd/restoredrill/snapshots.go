package main

import (
	"context"
	"fmt"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/backend/borg"
	"github.com/restoredrill/restoredrill/internal/backend/restic"
	"github.com/spf13/cobra"
)

var snapshotsJSON bool

var snapshotsCmd = &cobra.Command{
	Use:   "snapshots <repository>",
	Short: "List snapshots through the adapter, to debug selectors",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		name := args[0]
		repo, ok := cfg.Repositories[name]
		if !ok {
			return exitCodeError{exitUsage, fmt.Sprintf("repository %q is not defined", name)}
		}
		var be backend.Backend
		switch repo.Type {
		case "restic":
			be = restic.New(cfg.Binaries.Restic, repo, cfg.Redactor, nil)
		case "borg":
			be = borg.New(cfg.Binaries.Borg, repo, cfg.StateDir, cfg.Redactor, nil)
		default:
			return exitCodeError{exitUsage, fmt.Sprintf("repository %q has unknown type %q", name, repo.Type)}
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()
		snaps, err := be.ListSnapshots(ctx, backend.SnapshotFilter{})
		if err != nil {
			return exitCodeError{exitError, err.Error()}
		}
		if snapshotsJSON {
			return writeJSON(jsonEnvelope(snaps))
		}
		if len(snaps) == 0 {
			fmt.Println("No snapshots found.")
			return nil
		}
		fmt.Printf("%-20s %-25s %-15s %s\n", "ID", "TIME", "HOST", "TAGS")
		for _, s := range snaps {
			id := s.ID
			if len(id) > 20 {
				id = id[:20]
			}
			fmt.Printf("%-20s %-25s %-15s %v\n", id, s.Time.Format(time.RFC3339), s.Host, s.Tags)
		}
		return nil
	},
}

func init() {
	snapshotsCmd.Flags().BoolVar(&snapshotsJSON, "json", false, "output JSON")
	rootCmd.AddCommand(snapshotsCmd)
}