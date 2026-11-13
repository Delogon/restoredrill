package recipes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
	"gopkg.in/yaml.v3"
)

// sampleValues supplies values for required parameters in the expansion test.
var sampleValues = map[string]string{
	"repository": "r",
	"data_path":  "/srv/data",
}

// TestBundledRecipesExpandAndValidate loads every bundled recipe through the
// full config pipeline (expansion, substitution, strict decode, validation)
// with its required parameters set and defaults for the rest.
func TestBundledRecipesExpandAndValidate(t *testing.T) {
	names, err := config.ListBundledRecipes()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"generic-files", "postgres-dump", "service1", "service2", "service3", "sqlite-app"}
	if len(names) != len(want) {
		t.Fatalf("bundled recipes = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("bundled recipes = %v, want %v", names, want)
		}
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			r, err := config.LoadRecipe(name, ".")
			if err != nil {
				t.Fatal(err)
			}
			if r.Description == "" {
				t.Error("recipe has no description")
			}
			var with strings.Builder
			for pname, p := range r.Params {
				if !p.Required {
					continue
				}
				v, ok := sampleValues[pname]
				if !ok {
					t.Fatalf("required parameter %q has no sample value", pname)
				}
				fmt.Fprintf(&with, "      %s: %s\n", pname, v)
			}
			dir := t.TempDir()
			main := "version: 1\nrepositories:\n  r:\n    type: restic\n    repository: /tmp/repo\n" +
				"drills:\n  d:\n    uses: " + name + "\n    with:\n" + with.String()
			path := filepath.Join(dir, "restoredrill.yaml")
			if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("recipe %s does not expand/validate: %v", name, err)
			}
			d := cfg.Drills["d"]
			if d.Repository != "r" {
				t.Errorf("repository = %q, want r", d.Repository)
			}
			if len(d.Checks) == 0 {
				t.Error("recipe produced no checks")
			}
			if len(d.Restore.Paths) == 0 {
				t.Error("recipe produced no restore paths")
			}
			// No unresolved parameter placeholders may survive.
			resolved, err := yaml.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(resolved), "${param.") {
				t.Errorf("unresolved parameter in resolved drill:\n%s", resolved)
			}
		})
	}
}

// TestBundledRecipeOverride checks that drill-level fields override a bundled
// recipe (checks list replaces, schedule is added).
func TestBundledRecipeOverride(t *testing.T) {
	dir := t.TempDir()
	main := `version: 1
repositories:
  r:
    type: restic
    repository: /tmp/repo
drills:
  d:
    uses: generic-files
    with:
      repository: r
      data_path: /srv/data
    schedule: '0 5 * * 1'
    checks:
      - type: snapshot_age
        max_age: 48h
`
	path := filepath.Join(dir, "restoredrill.yaml")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Drills["d"]
	if len(d.Checks) != 1 || d.Checks[0].Type != "snapshot_age" {
		t.Fatalf("checks override failed: %+v", d.Checks)
	}
	if d.Schedule != "0 5 * * 1" {
		t.Fatalf("schedule = %q", d.Schedule)
	}
}