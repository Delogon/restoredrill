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

var validateOnline bool

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check config schema and semantics, secret permissions, binary versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		printWarnings(cfg)
		fmt.Printf("Config %s is valid: %d repositories, %d drills, %d notifiers.\n",
			cfg.Path, len(cfg.Repositories), len(cfg.Drills), len(cfg.Notifiers))

		if !validateOnline {
			return nil
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()
		failed := false
		for name, repo := range cfg.Repositories {
			var be backend.Backend
			switch repo.Type {
			case "restic":
				be = restic.New(cfg.Binaries.Restic, repo, cfg.Redactor, nil)
			case "borg":
				be = borg.New(cfg.Binaries.Borg, repo, cfg.StateDir, cfg.Redactor, nil)
			default:
				continue
			}
			v, err := be.Version(ctx)
			if err != nil {
				fmt.Printf("  [error] repository %s: %v\n", name, err)
				failed = true
				continue
			}
			snaps, err := be.ListSnapshots(ctx, backend.SnapshotFilter{})
			if err != nil {
				fmt.Printf("  [error] repository %s (%s %s): %v\n", name, be.Kind(), v, err)
				failed = true
				continue
			}
			fmt.Printf("  [ok] repository %s (%s %s): %d snapshots\n", name, be.Kind(), v, len(snaps))
		}
		if failed {
			return exitCodeError{exitError, "one or more repositories are not reachable"}
		}
		return nil
	},
}

func init() {
	validateCmd.Flags().BoolVar(&validateOnline, "online", false, "also contact repositories to verify reachability")
	rootCmd.AddCommand(validateCmd)
}