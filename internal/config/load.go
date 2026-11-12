package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultListen is the dashboard's default bind address (loopback only).
const DefaultListen = "127.0.0.1:8484"

// FindPath resolves the config file location: explicit flag, then
// RESTOREDRILL_CONFIG, then ./restoredrill.yaml, then
// /etc/restoredrill/restoredrill.yaml.
func FindPath(flagPath string) (string, error) {
	if flagPath != "" {
		if _, err := os.Stat(flagPath); err != nil {
			return "", fmt.Errorf("config file %q not found — pass --config or set RESTOREDRILL_CONFIG", flagPath)
		}
		return flagPath, nil
	}
	if env := os.Getenv("RESTOREDRILL_CONFIG"); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("config file %q (from RESTOREDRILL_CONFIG) not found", env)
		}
		return env, nil
	}
	for _, p := range []string{"restoredrill.yaml", "/etc/restoredrill/restoredrill.yaml"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file found — create restoredrill.yaml (restoredrill init), pass --config or set RESTOREDRILL_CONFIG")
}

// Load reads, merges, expands recipes, substitutes variables, decodes and
// validates the configuration. All returned errors are user-facing and state
// the next step.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %q: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("config %q is not valid YAML: %w", path, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config %q must be a YAML mapping", path)
	}
	root := doc.Content[0]
	dir := filepath.Dir(abs)

	if err := applyIncludes(root, dir); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}

	// Expand recipes before variable substitution so ${param.*} is resolved.
	if drills := mapEntry(root, "drills"); drills != nil {
		if drills.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config %q: \"drills\" must be a mapping of drill names to drills", path)
		}
		for i := 0; i+1 < len(drills.Content); i += 2 {
			name, node := drills.Content[i].Value, drills.Content[i+1]
			if node.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("drill %q must be a mapping", name)
			}
			if err := expandRecipe(node, dir); err != nil {
				return nil, fmt.Errorf("drill %q: %w", name, err)
			}
		}
	}

	if err := resolveConfigVars(root, nil); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, err)
	}

	// Strict decode: unknown top-level or nested fields are errors.
	buf, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(buf))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config %q: %w", path, friendlyDecodeError(err))
	}
	cfg.Path = abs
	cfg.Dir = dir
	cfg.applyDefaults()

	cfg.Redactor = resolveSecrets(&cfg)

	if errs := Validate(&cfg); len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = "  - " + e.Error()
		}
		return &cfg, fmt.Errorf("config %q is invalid:\n%s", path, strings.Join(msgs, "\n"))
	}
	return &cfg, nil
}

// applyIncludes merges included files into root. Include patterns are
// resolved relative to the main config's directory. Includes are not
// recursive: an include key inside an included file is ignored.
func applyIncludes(root *yaml.Node, dir string) error {
	inc := mapEntry(root, "include")
	if inc == nil {
		return nil
	}
	if inc.Kind != yaml.SequenceNode {
		return fmt.Errorf("\"include\" must be a list of file patterns")
	}
	var files []string
	for _, item := range inc.Content {
		pattern := item.Value
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(dir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("include pattern %q: %w", item.Value, err)
		}
		if len(matches) == 0 {
			return fmt.Errorf("include pattern %q matched no files — remove it or create the files", item.Value)
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("include %q: %w", f, err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("include %q: %w", f, err)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("include %q must be a YAML mapping", f)
		}
		mergeNodes(root, doc.Content[0])
	}
	deleteMapEntry(root, "include")
	return nil
}

func (c *Config) applyDefaults() {
	if c.Binaries.Restic == "" {
		c.Binaries.Restic = "restic"
	}
	if c.Binaries.Borg == "" {
		c.Binaries.Borg = "borg"
	}
	if c.StateDir == "" {
		home, _ := os.UserHomeDir()
		c.StateDir = filepath.Join(home, ".local", "state", "restoredrill")
	}
	if c.WorkDir == "" {
		c.WorkDir = filepath.Join(os.TempDir(), "restoredrill")
	}
	if c.Retention.RunsPerDrill == 0 {
		c.Retention.RunsPerDrill = 200
	}
	if c.Defaults.Timeout == 0 {
		c.Defaults.Timeout = Duration(30 * 60 * 1e9)
	}
	if c.Defaults.KeepTTL == 0 {
		c.Defaults.KeepTTL = Duration(24 * 3600 * 1e9)
	}
	if c.Server.Listen == "" {
		c.Server.Listen = DefaultListen
	}
	if c.Server.Auth.Mode == "" {
		c.Server.Auth.Mode = "none"
	}
	if c.Server.Timezone == "" {
		c.Server.Timezone = "UTC"
	}
	if c.Server.Auth.Header == "" {
		c.Server.Auth.Header = "X-Forwarded-User"
	}
	for name, d := range c.Drills {
		if d.Timeout == 0 {
			d.Timeout = c.Defaults.Timeout
		}
		if d.KeepOnFailure == nil {
			v := c.Defaults.KeepOnFailure
			d.KeepOnFailure = &v
		}
		if d.KeepTTL == 0 {
			d.KeepTTL = c.Defaults.KeepTTL
		}
		if d.Notify == nil {
			d.Notify = c.Defaults.Notify
		}
		if d.Resources == nil {
			d.Resources = c.Defaults.Resources
		}
		if d.Snapshot.Select == "" {
			d.Snapshot.Select = "latest"
		}
		if d.Sandbox != nil && d.Sandbox.Network == "" {
			d.Sandbox.Network = "isolated"
		}
		for i := range d.Checks {
			if d.Checks[i].Name == "" {
				d.Checks[i].Name = fmt.Sprintf("%s[%d]", d.Checks[i].Type, i)
			}
		}
		c.Drills[name] = d
	}
}

// friendlyDecodeError rewrites yaml type errors into actionable messages.
func friendlyDecodeError(err error) error {
	s := err.Error()
	if strings.Contains(s, "already set") || strings.Contains(s, "field ") {
		return fmt.Errorf("%w — remove the unknown field (see docs/config-reference.md)", err)
	}
	return err
}

// DrillNames returns drill names in sorted order.
func (c *Config) DrillNames() []string {
	names := make([]string, 0, len(c.Drills))
	for n := range c.Drills {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}