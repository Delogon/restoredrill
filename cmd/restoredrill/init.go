package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initDir string

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Write an example config and one sample drill",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := initDir
		if dir == "" {
			dir = "."
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return exitCodeError{exitUsage, err.Error()}
		}
		cfgPath := filepath.Join(dir, "restoredrill.yaml")
		if _, err := os.Stat(cfgPath); err == nil {
			return exitCodeError{exitUsage, fmt.Sprintf("%s already exists — remove it or choose another --dir", cfgPath)}
		}
		if err := os.WriteFile(cfgPath, []byte(exampleConfig), 0o644); err != nil {
			return exitCodeError{exitInternal, err.Error()}
		}
		drillDir := filepath.Join(dir, "drills.d")
		if err := os.MkdirAll(drillDir, 0o755); err != nil {
			return exitCodeError{exitInternal, err.Error()}
		}
		drillPath := filepath.Join(drillDir, "example.yaml")
		if err := os.WriteFile(drillPath, []byte(exampleDrill), 0o644); err != nil {
			return exitCodeError{exitInternal, err.Error()}
		}
		fmt.Printf("Wrote %s and %s\n", cfgPath, drillPath)
		fmt.Println("Next: edit the repository and drill, then run \"restoredrill validate\".")
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initDir, "dir", "", "directory to write the example into (default .)")
	rootCmd.AddCommand(initCmd)
}

const exampleConfig = `version: 1
# Where the SQLite database lives.
state_dir: /var/lib/restoredrill
# Where restore sandboxes are created. Needs free space (estimate x1.1 + 1 GiB).
# Prefer an encrypted local disk.
work_dir: /var/tmp/restoredrill

binaries:
  restic: restic
  borg: borg

retention:
  runs_per_drill: 200

defaults:
  timeout: 30m
  keep_on_failure: false
  keep_ttl: 24h

repositories:
  # Example restic repository. Unlock material goes through *_file/*_command
  # or environment variables, never inline.
  my-restic:
    type: restic
    repository: /path/to/restic/repo
    password_file: /etc/restoredrill/secrets/restic.pass

server:
  listen: 127.0.0.1:8484
  timezone: UTC
  auth:
    mode: none

include:
  - drills.d/*.yaml
`

const exampleDrill = `drills:
  example-files:
    repository: my-restic
    # schedule: '0 3 * * *'   # uncomment for nightly runs under "serve"
    snapshot:
      select: latest
      max_age: 26h            # adds an implicit snapshot_age check
    restore:
      paths: [/srv/app/data]  # absolute source paths to restore
    checks:
      - type: files_exist
        paths: ['srv/app/data/**']
        min_count: 1
      - type: size_trend
        max_drop_percent: 10
`