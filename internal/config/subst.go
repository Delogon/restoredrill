package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var varRE = regexp.MustCompile(`\$\{([^{}]*)\}`)

// runtimeVars are resolved per run, not at config load time.
var runtimeVars = map[string]bool{"restore": true, "run.id": true}

// substituteString replaces ${...} variables using lookup. When lookup does
// not know a variable, it is an error unless the variable is a runtime
// placeholder and allowRuntime is true.
func substituteString(s string, lookup func(string) (string, bool), allowRuntime bool) (string, error) {
	var firstErr error
	out := varRE.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		if v, ok := lookup(name); ok {
			return v
		}
		if allowRuntime && runtimeVars[name] {
			return m
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("unknown variable ${%s} — allowed: ${restore}, ${run.id}, ${env.NAME}, ${param.name} (in recipes)", name)
		}
		return m
	})
	return out, firstErr
}

// resolveConfigVars walks a YAML tree and resolves ${env.NAME} and
// ${param.name} variables. ${restore} and ${run.id} are left for run time.
func resolveConfigVars(node *yaml.Node, params map[string]string) error {
	switch node.Kind {
	case yaml.DocumentNode:
		for _, c := range node.Content {
			if err := resolveConfigVars(c, params); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			if err := resolveConfigVars(node.Content[i+1], params); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range node.Content {
			if err := resolveConfigVars(c, params); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return nil
		}
		orig := node.Value
		v, err := substituteString(node.Value, func(name string) (string, bool) {
			switch {
			case strings.HasPrefix(name, "env."):
				return os.LookupEnv(strings.TrimPrefix(name, "env."))
			case strings.HasPrefix(name, "param."):
				v, ok := params[strings.TrimPrefix(name, "param.")]
				return v, ok
			}
			return "", false
		}, true)
		if err != nil {
			return err
		}
		node.Value = v
		if v != orig {
			retagPlainScalar(node)
		}
	}
	return nil
}

// retagPlainScalar re-resolves the implicit tag of a plain scalar whose value
// changed during substitution, so a numeric or boolean parameter value decodes
// into int/bool fields (e.g. min_count: ${param.min_count}). Without this the
// substituted value keeps its !!str tag, is re-marshalled quoted, and fails
// strict decoding. Only !!int/!!float/!!bool are adopted: null-like or
// timestamp-like values must stay strings in string fields. Quoted scalars
// keep !!str (the author asked for a string), as do values that do not
// re-parse as a single identical scalar (empty, multi-line, mappings, ...).
func retagPlainScalar(node *yaml.Node) {
	if node.Style != 0 {
		return
	}
	var probe yaml.Node
	if err := yaml.Unmarshal([]byte(node.Value), &probe); err != nil {
		return
	}
	if len(probe.Content) != 1 {
		return
	}
	p := probe.Content[0]
	if p.Kind != yaml.ScalarNode || p.Value != node.Value {
		return
	}
	switch p.Tag {
	case "!!int", "!!float", "!!bool":
		node.Tag = p.Tag
	}
}

// SubstituteRuntime replaces ${restore} and ${run.id} in every string field
// of the drill. It is called by the runner once the sandbox path and run ID
// are known.
func (d *Drill) SubstituteRuntime(restoreDir, runID string) {
	fn := func(s string) string {
		s = strings.ReplaceAll(s, "${restore}", restoreDir)
		return strings.ReplaceAll(s, "${run.id}", runID)
	}
	for i := range d.Restore.Paths {
		d.Restore.Paths[i] = fn(d.Restore.Paths[i])
	}
	if d.Sandbox != nil {
		for name, svc := range d.Sandbox.Services {
			svc.Image = fn(svc.Image)
			svc.User = fn(svc.User)
			for i := range svc.Mounts {
				svc.Mounts[i].Source = fn(svc.Mounts[i].Source)
				svc.Mounts[i].Target = fn(svc.Mounts[i].Target)
			}
			for k, v := range svc.Env {
				svc.Env[k] = fn(v)
			}
			if svc.Healthcheck != nil && svc.Healthcheck.HTTP != nil {
				svc.Healthcheck.HTTP.Path = fn(svc.Healthcheck.HTTP.Path)
			}
			d.Sandbox.Services[name] = svc
		}
	}
	for i := range d.Checks {
		c := &d.Checks[i]
		c.Path = fn(c.Path)
		c.Query = fn(c.Query)
		c.Dump = fn(c.Dump)
		c.SourceRoot = fn(c.SourceRoot)
		c.Image = fn(c.Image)
		for j := range c.Paths {
			c.Paths[j] = fn(c.Paths[j])
		}
		for j := range c.Run {
			c.Run[j] = fn(c.Run[j])
		}
		for k, v := range c.Headers {
			c.Headers[k] = fn(v)
		}
		for j := range c.Queries {
			c.Queries[j].SQL = fn(c.Queries[j].SQL)
		}
	}
}

// mergeNodes deep-merges src into dst. Maps merge recursively; scalars and
// lists from src replace dst values. Both must be mapping nodes.
func mergeNodes(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, val := src.Content[i], src.Content[i+1]
		found := false
		for j := 0; j+1 < len(dst.Content); j += 2 {
			if dst.Content[j].Value == key.Value {
				if dst.Content[j+1].Kind == yaml.MappingNode && val.Kind == yaml.MappingNode {
					mergeNodes(dst.Content[j+1], val)
				} else {
					dst.Content[j+1] = val
				}
				found = true
				break
			}
		}
		if !found {
			dst.Content = append(dst.Content, key, val)
		}
	}
}

// cloneNode deep-copies a YAML node.
func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	if n.Content != nil {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i, c := range n.Content {
			cp.Content[i] = cloneNode(c)
		}
	}
	return &cp
}

// mapEntry returns the value node for key in a mapping node, or nil.
func mapEntry(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// deleteMapEntry removes key from a mapping node.
func deleteMapEntry(n *yaml.Node, key string) {
	if n == nil || n.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}