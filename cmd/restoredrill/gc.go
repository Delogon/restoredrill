package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/sandbox"
	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/spf13/cobra"
)

var gcJSON bool

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Remove leftover sandboxes, containers and networks from finished runs",
	Long: "Garbage-collect resources left behind by runs that are no longer active:\n" +
		"restore directories in work_dir, and containers/networks labelled\n" +
		"restoredrill.run. Kept sandboxes (keep_on_failure / --keep) are retained\n" +
		"until their keep_ttl expires. run and serve do this automatically at startup.",
	RunE: func(cmd *cobra.Command, args []string) error {
		log := setupLogger()
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := openStore(cfg)
		if err != nil {
			return err
		}
		defer st.Close()
		n, err := recoverLeftovers(cmd.Context(), cfg, st, log)
		if err != nil {
			return exitCodeError{exitInternal, err.Error()}
		}
		if gcJSON {
			return writeJSON(jsonEnvelope(map[string]any{"removed": n}))
		}
		fmt.Printf("removed %d leftover resource(s)\n", n)
		return nil
	},
}

func init() {
	gcCmd.Flags().BoolVar(&gcJSON, "json", false, "output JSON")
	rootCmd.AddCommand(gcCmd)
}

// recoverLeftovers is the crash-recovery pass shared by run, serve and gc: it
// removes containers, networks and sandbox directories belonging to runs that
// are no longer active. Kept sandboxes survive until keep_ttl expires. If the
// container runtime is unreachable, directory cleanup still happens and the
// container part is skipped with a warning.
func recoverLeftovers(ctx context.Context, cfg *config.Config, st *store.Store, log *slog.Logger) (int, error) {
	active, err := st.ActiveRuns()
	if err != nil {
		return 0, fmt.Errorf("cannot list active runs: %w", err)
	}
	activeIDs := make([]string, 0, len(active))
	for _, r := range active {
		activeIDs = append(activeIDs, r.ID)
	}

	var client *sandbox.DockerClient
	if c, err := sandbox.NewDockerClient(); err == nil {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.Ping(pingCtx)
		cancel()
		if err == nil {
			client = c
		} else {
			log.Warn("Docker/Podman not reachable — skipping container cleanup", "err", err)
		}
	}

	// Kept sandboxes expire after the drill's keep_ttl (default from defaults).
	kept, err := st.ListKeptRuns()
	if err != nil {
		return 0, fmt.Errorf("cannot list kept runs: %w", err)
	}
	keepUntil := map[string]time.Time{}
	for _, r := range kept {
		ttl := cfg.Defaults.KeepTTL
		if d, ok := cfg.Drills[r.Drill]; ok && d.KeepTTL > 0 {
			ttl = d.KeepTTL
		}
		keepUntil[r.ID] = r.FinishedAt.Add(ttl.D())
	}
	shouldKeep := func(runID string) bool {
		until, ok := keepUntil[runID]
		return ok && time.Now().Before(until)
	}

	n, err := sandbox.GC(ctx, client, activeIDs, cfg.WorkDir, shouldKeep)
	if err != nil {
		return n, err
	}

	// Forget kept sandboxes that GC removed (TTL expired).
	for _, r := range kept {
		if shouldKeep(r.ID) {
			continue
		}
		if _, err := os.Stat(r.KeptPath); os.IsNotExist(err) {
			if err := st.ClearKeptPath(r.ID); err != nil {
				log.Warn("cannot clear kept_path", "run", r.ID, "err", err)
			}
		}
	}
	if n > 0 {
		log.Info("crash recovery: removed leftovers", "count", n)
	}
	return n, nil
}