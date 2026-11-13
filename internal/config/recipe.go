package config

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// BundledRecipes holds recipes embedded in the binary. It is set by the
// recipes package at init time; nil when no recipes are bundled.
var BundledRecipes fs.FS

// Recipe is a parameterised drill template.
type Recipe struct {
	Recipe      string                 `yaml:"recipe"`
	Version     int                    `yaml:"version"`
	Description string                 `yaml:"description"`
	Params      map[string]RecipeParam `yaml:"params"`
	Drill       yaml.Node              `yaml:"drill"`

	// Source describes where the recipe was loaded from (not serialized).
	Source string `yaml:"-"`
}

// RecipeParam declares one recipe parameter.
type RecipeParam struct {
	Required bool `yaml:"required"`
	Default  any  `yaml:"default"`
}

// LoadRecipe loads a recipe by bundled name or file path. Paths are resolved
// relative to dir (the main config's directory).
func LoadRecipe(name, dir string) (*Recipe, error) {
	var data []byte
	source := "bundled:" + name
	isPath := strings.ContainsAny(name, "/\\") || strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
	if isPath {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("recipe %q: %w — check the path in uses", name, err)
		}
		data = b
		source = p
	} else {
		if BundledRecipes == nil {
			return nil, fmt.Errorf("recipe %q not found — no bundled recipes available", name)
		}
		b, err := fs.ReadFile(BundledRecipes, name+".yaml")
		if err != nil {
			return nil, fmt.Errorf("recipe %q not found — run \"restoredrill recipe list\" to see available recipes", name)
		}
		data = b
	}
	var r Recipe
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("recipe %q: %w", name, err)
	}
	if r.Recipe == "" {
		return nil, fmt.Errorf("recipe %q: missing \"recipe\" name field", source)
	}
	if r.Version != SchemaVersion {
		return nil, fmt.Errorf("recipe %q: unsupported version %d — expected %d", source, r.Version, SchemaVersion)
	}
	if r.Drill.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("recipe %q: \"drill\" must be a mapping", source)
	}
	r.Source = source
	return &r, nil
}

// ListBundledRecipes returns the names of all bundled recipes.
func ListBundledRecipes() ([]string, error) {
	if BundledRecipes == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(BundledRecipes, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return names, nil
}

// expandRecipe resolves a drill's "uses"/"with" fields into a plain drill
// node: recipe drill fields are the base, drill fields override (deep merge
// for maps, replace for lists), and ${param.*} variables are substituted.
func expandRecipe(drillNode *yaml.Node, dir string) error {
	usesNode := mapEntry(drillNode, "uses")
	if usesNode == nil {
		return nil
	}
	name := usesNode.Value
	recipe, err := LoadRecipe(name, dir)
	if err != nil {
		return err
	}

	// Collect parameter values: with overrides defaults; required must exist.
	with := map[string]string{}
	if w := mapEntry(drillNode, "with"); w != nil {
		if w.Kind != yaml.MappingNode {
			return fmt.Errorf("drill uses %q: \"with\" must be a mapping", name)
		}
		for i := 0; i+1 < len(w.Content); i += 2 {
			k, v := w.Content[i].Value, w.Content[i+1]
			if v.Kind != yaml.ScalarNode {
				return fmt.Errorf("drill uses %q: parameter %q must be a scalar value", name, k)
			}
			if _, declared := recipe.Params[k]; !declared {
				return fmt.Errorf("drill uses %q: unknown parameter %q — recipe declares: %s", name, k, strings.Join(paramNames(recipe), ", "))
			}
			with[k] = v.Value
		}
	}
	for pname, p := range recipe.Params {
		if _, ok := with[pname]; ok {
			continue
		}
		if p.Default != nil {
			with[pname] = fmt.Sprintf("%v", p.Default)
			continue
		}
		if p.Required {
			return fmt.Errorf("drill uses %q: missing required parameter %q — set it under with", name, pname)
		}
	}

	// Substitute ${param.*} in a copy of the recipe drill.
	base := cloneNode(&recipe.Drill)
	if err := resolveConfigVars(base, with); err != nil {
		return fmt.Errorf("recipe %q: %w", name, err)
	}
	// Any remaining ${param.*} refers to an undeclared parameter.
	if err := checkNoParams(base); err != nil {
		return fmt.Errorf("recipe %q: %w", name, err)
	}

	override := cloneNode(drillNode)
	deleteMapEntry(override, "uses")
	deleteMapEntry(override, "with")
	mergeNodes(base, override)
	*drillNode = *base
	return nil
}

func paramNames(r *Recipe) []string {
	names := make([]string, 0, len(r.Params))
	for k := range r.Params {
		names = append(names, k)
	}
	return names
}

func checkNoParams(node *yaml.Node) error {
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range node.Content {
			if err := checkNoParams(c); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if err := checkNoParams(node.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		for _, m := range varRE.FindAllStringSubmatch(node.Value, -1) {
			if strings.HasPrefix(m[1], "param.") {
				return fmt.Errorf("undeclared parameter ${%s}", m[1])
			}
		}
	}
	return nil
}