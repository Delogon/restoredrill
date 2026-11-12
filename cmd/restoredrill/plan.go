package main

import (
	"context"
	"fmt"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/backend/borg"
	"github.com/restoredrill/restoredrill/internal/backend/restic"
	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var planJSON bool

var planCmd = &cobra.Command{
	Use:   "plan <drill>",
	Short: "Dry run: resolved drill, chosen snapshot, estimated size, checks (restores nothing)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		name := args[0]
		drill, ok := cfg.Drills[name]
		if !ok {
			return exitCodeError{exitUsage, fmt.Sprintf("drill %q is not defined", name)}
		}
		repo := cfg.Repositories[drill.Repository]
		var be backend.Backend
		switch repo.Type {
		case "restic":
			be = restic.New(cfg.Binaries.Restic, repo, cfg.Redactor, nil)
		case "borg":
			be = borg.New(cfg.Binaries.Borg, repo, cfg.StateDir, cfg.Redactor, nil)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()

		filter := backend.SnapshotFilter{Tags: drill.Snapshot.Tags, Host: drill.Snapshot.Host, Paths: drill.Snapshot.Paths, ArchiveGlob: drill.Snapshot.ArchiveGlob}
		snaps, err := be.ListSnapshots(ctx, filter)
		if err != nil {
			return exitCodeError{exitError, err.Error()}
		}
		snap, err := backend.SelectSnapshot(snaps, backend.Selection{Select: drill.Snapshot.Select, Within: drill.Snapshot.Within.D()}, time.Now())
		if err != nil {
			return exitCodeError{exitError, err.Error()}
		}
		est, err := be.EstimateRestoreSize(ctx, snap, drill.Restore.Paths)
		if err != nil {
			return exitCodeError{exitError, err.Error()}
		}

		// Effective checks (implicit snapshot_age + service_healthy).
		var eff []config.Check
		if drill.Snapshot.MaxAge > 0 {
			eff = append(eff, config.Check{Type: "snapshot_age", Name: "snapshot_age", MaxAge: drill.Snapshot.MaxAge})
		}
		eff = append(eff, drill.Checks...)

		if planJSON {
			return writeJSON(jsonEnvelope(map[string]any{
				"drill":           name,
				"resolved":        drill,
				"snapshot":        snap,
				"estimated_bytes": est,
				"checks":          eff,
			}))
		}

		fmt.Printf("Drill %q (repository %s, backend %s)\n\n", name, drill.Repository, be.Kind())
		fmt.Println("Resolved drill (secrets redacted):")
		y, _ := yaml.Marshal(drill)
		fmt.Println(cfg.Redactor.String(string(y)))
		fmt.Printf("Chosen snapshot: %s (time %s, host %s)\n", snap.ID, snap.Time.Format(time.RFC3339), snap.Host)
		fmt.Printf("Estimated restore size: %d bytes\n\n", est)
		fmt.Println("Checks (in order):")
		for _, ch := range eff {
			fmt.Printf("  - %s [%s phase]\n", ch.Type, checks.CheckPhaseFor(ch))
		}
		return nil
	},
}

func init() {
	planCmd.Flags().BoolVar(&planJSON, "json", false, "output JSON")
	rootCmd.AddCommand(planCmd)
}