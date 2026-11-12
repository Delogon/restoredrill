package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/spf13/cobra"
)

var (
	flagConfig    string
	flagLogLevel  string
	flagLogFormat string
)

var rootCmd = &cobra.Command{
	Use:   "restoredrill",
	Short: "Prove your backups can actually be restored",
	Long: "restoredrill periodically restores restic and Borg snapshots into a throwaway\n" +
		"sandbox and checks that the data and the services built on it work.\n\n" +
		"It is strictly read-only towards repositories: it never modifies a backup.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "", "config file (default: ./restoredrill.yaml, then /etc/restoredrill/restoredrill.yaml)")
	rootCmd.PersistentFlags().StringVar(&flagLogLevel, "log-level", "info", "log level: debug, info, warn, error")
	rootCmd.PersistentFlags().StringVar(&flagLogFormat, "log-format", "text", "log format: text or json")
}

func execute() error {
	return rootCmd.ExecuteContext(context.Background())
}

// setupLogger configures the global slog logger from the global flags.
func setupLogger() *slog.Logger {
	var level slog.Level
	switch strings.ToLower(flagLogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.ToLower(flagLogFormat) == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	log := slog.New(h)
	slog.SetDefault(log)
	return log
}

// loadConfig finds and loads the configuration, mapping failures to exit code 2.
func loadConfig() (*config.Config, error) {
	path, err := config.FindPath(flagConfig)
	if err != nil {
		return nil, exitCodeError{exitUsage, err.Error()}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, exitCodeError{exitUsage, err.Error()}
	}
	return cfg, nil
}

// openStore opens the state database for a loaded config.
func openStore(cfg *config.Config) (*store.Store, error) {
	st, err := store.Open(filepath.Join(cfg.StateDir, "restoredrill.db"))
	if err != nil {
		return nil, exitCodeError{exitInternal, fmt.Sprintf("cannot open state database: %v", err)}
	}
	return st, nil
}

// writeJSON prints a versioned JSON document with schema: 1.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// jsonEnvelope wraps a payload in the stable schema envelope.
func jsonEnvelope(payload any) map[string]any {
	return map[string]any{"schema": 1, "data": payload}
}

// printWarnings writes config warnings to stderr.
func printWarnings(cfg *config.Config) {
	for _, w := range cfg.Warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
}