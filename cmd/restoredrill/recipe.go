package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var recipeJSON bool

var recipeCmd = &cobra.Command{
	Use:   "recipe",
	Short: "List and show drill recipes (bundled and local)",
}

// recipeEntry is one row of `recipe list`.
type recipeEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "bundled" or a file path
}

var recipeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List bundled recipes and local recipes in <config dir>/recipes/",
	RunE: func(cmd *cobra.Command, args []string) error {
		var entries []recipeEntry
		names, err := config.ListBundledRecipes()
		if err != nil {
			return err
		}
		for _, n := range names {
			r, err := config.LoadRecipe(n, ".")
			if err != nil {
				return err
			}
			entries = append(entries, recipeEntry{Name: r.Recipe, Description: r.Description, Source: "bundled"})
		}
		// Local recipes live in a recipes/ directory next to the config file.
		// A missing config is fine: bundled recipes are still listed.
		if path, err := config.FindPath(flagConfig); err == nil {
			dir, err := filepath.Abs(filepath.Dir(path))
			if err != nil {
				return err
			}
			for _, pattern := range []string{"*.yaml", "*.yml"} {
				files, _ := filepath.Glob(filepath.Join(dir, "recipes", pattern))
				sort.Strings(files)
				for _, f := range files {
					r, err := config.LoadRecipe(f, dir)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: %v\n", err)
						continue
					}
					entries = append(entries, recipeEntry{Name: r.Recipe, Description: r.Description, Source: f})
				}
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		if recipeJSON {
			return writeJSON(jsonEnvelope(entries))
		}
		if len(entries) == 0 {
			fmt.Println("No recipes available.")
			return nil
		}
		fmt.Printf("%-20s %-10s %s\n", "NAME", "SOURCE", "DESCRIPTION")
		for _, e := range entries {
			fmt.Printf("%-20s %-10s %s\n", e.Name, e.Source, e.Description)
		}
		return nil
	},
}

var recipeShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a recipe's parameters and drill template (bundled name or file path)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if path, err := config.FindPath(flagConfig); err == nil {
			if abs, err := filepath.Abs(filepath.Dir(path)); err == nil {
				dir = abs
			}
		}
		r, err := loadRecipeForShow(args[0], dir)
		if err != nil {
			return exitCodeError{exitUsage, err.Error()}
		}
		if recipeJSON {
			var drill any
			if err := r.Drill.Decode(&drill); err != nil {
				return fmt.Errorf("recipe %q: %w", r.Recipe, err)
			}
			params := map[string]any{}
			for name, p := range r.Params {
				params[name] = map[string]any{"required": p.Required, "default": p.Default}
			}
			return writeJSON(jsonEnvelope(map[string]any{
				"recipe":      r.Recipe,
				"version":     r.Version,
				"description": r.Description,
				"source":      r.Source,
				"params":      params,
				"drill":       drill,
			}))
		}
		b, err := yaml.Marshal(r)
		if err != nil {
			return err
		}
		fmt.Printf("# source: %s\n%s", r.Source, b)
		return nil
	},
}

// loadRecipeForShow resolves a recipe by bundled name, local name (a file in
// <config dir>/recipes/) or file path. Bundled names win, matching `uses:`.
func loadRecipeForShow(name, dir string) (*config.Recipe, error) {
	r, err := config.LoadRecipe(name, dir)
	if err == nil {
		return r, nil
	}
	if dir != "." && !filepath.IsAbs(name) {
		for _, ext := range []string{".yaml", ".yml"} {
			p := filepath.Join(dir, "recipes", name+ext)
			if _, statErr := os.Stat(p); statErr == nil {
				return config.LoadRecipe(p, dir)
			}
		}
	}
	return nil, err
}

func init() {
	recipeCmd.PersistentFlags().BoolVar(&recipeJSON, "json", false, "output JSON")
	recipeCmd.AddCommand(recipeListCmd, recipeShowCmd)
	rootCmd.AddCommand(recipeCmd)
}