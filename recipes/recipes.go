// Package recipes bundles drill templates for common self-hosted apps.
// Importing it for side effects registers the recipes with the config
// package (config.BundledRecipes); the CLI does this from main.
package recipes

import (
	"embed"

	"github.com/restoredrill/restoredrill/internal/config"
)

// FS holds the bundled recipe YAML files at its root.
//
//go:embed *.yaml
var FS embed.FS

func init() {
	config.BundledRecipes = FS
}