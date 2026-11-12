package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var versionJSON bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Version, commit, detected restic and Borg versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		resticV := detectVersion("restic", "version")
		borgV := detectVersion("borg", "--version")
		if versionJSON {
			return writeJSON(jsonEnvelope(map[string]any{
				"version": version,
				"commit":  commit,
				"go":      runtime.Version(),
				"restic":  resticV,
				"borg":    borgV,
			}))
		}
		fmt.Printf("restoredrill %s (commit %s, %s)\n", version, commit, runtime.Version())
		fmt.Printf("  restic: %s\n", resticV)
		fmt.Printf("  borg:   %s\n", borgV)
		return nil
	},
}

func init() {
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "output JSON")
	rootCmd.AddCommand(versionCmd)
}

func detectVersion(bin string, args ...string) string {
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return "not found"
	}
	return strings.TrimSpace(string(out))
}