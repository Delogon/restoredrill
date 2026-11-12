// Command gendocs regenerates docs/schema.json and docs/config-reference.md
// from the config JSON Schema. Run: go run ./cmd/gendocs
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/restoredrill/restoredrill/internal/config"
)

func main() {
	schema := config.JSONSchema()
	b, err := config.JSONSchemaBytes()
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("docs/schema.json", append(b, '\n'), 0o644); err != nil {
		panic(err)
	}
	if err := os.WriteFile("docs/config-reference.md", []byte(render(schema)), 0o644); err != nil {
		panic(err)
	}
	fmt.Println("wrote docs/schema.json and docs/config-reference.md")
}

func render(schema map[string]any) string {
	var sb strings.Builder
	sb.WriteString("# Configuration reference\n\n")
	sb.WriteString("This reference is generated from the config JSON Schema (`docs/schema.json`).\n")
	sb.WriteString("Regenerate with `go run ./cmd/gendocs`. The schema can be used for editor\n")
	sb.WriteString("validation of `restoredrill.yaml`.\n\n")
	sb.WriteString("Top-level keys of `restoredrill.yaml`:\n\n")
	writeProps(&sb, schema)

	defs, _ := schema["$defs"].(map[string]any)
	names := make([]string, 0, len(defs))
	for n := range defs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		def, _ := defs[n].(map[string]any)
		sb.WriteString("\n## " + n + "\n\n")
		writeProps(&sb, def)
	}
	return sb.String()
}

func writeProps(sb *strings.Builder, def map[string]any) {
	props, _ := def["properties"].(map[string]any)
	if len(props) == 0 {
		return
	}
	required := map[string]bool{}
	if req, ok := def["required"].([]any); ok {
		for _, r := range req {
			required[fmt.Sprint(r)] = true
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString("| Field | Type | Required | Description |\n")
	sb.WriteString("|-------|------|----------|-------------|\n")
	for _, k := range keys {
		p, _ := props[k].(map[string]any)
		typ := typeString(p)
		desc, _ := p["description"].(string)
		if enum, ok := p["enum"].([]string); ok {
			desc = strings.Join(enum, ", ")
		}
		req := "no"
		if required[k] {
			req = "yes"
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s |\n", k, typ, req, desc))
	}
}

func typeString(p map[string]any) string {
	if ref, ok := p["$ref"].(string); ok {
		parts := strings.Split(ref, "/")
		return "[" + parts[len(parts)-1] + "](#" + strings.ToLower(parts[len(parts)-1]) + ")"
	}
	t, _ := p["type"].(string)
	switch t {
	case "array":
		items, _ := p["items"].(map[string]any)
		return "list of " + typeString(items)
	case "object":
		if ap, ok := p["additionalProperties"].(map[string]any); ok {
			return "map of " + typeString(ap)
		}
		return "object"
	case "":
		return "any"
	default:
		return t
	}
}