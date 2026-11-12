package config

import (
	"encoding/json"
	"reflect"
	"strings"
)

// JSONSchema generates a JSON Schema (draft-07) for the config format from
// the Go types. It is used for editor validation and to generate the config
// reference documentation.
func JSONSchema() map[string]any {
	g := &schemaGen{defs: map[string]any{}}
	g.typ(reflect.TypeOf(Config{}))
	root := map[string]any{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$id":     "https://restoredrill.dev/schema/v1.json",
		"title":   "restoredrill configuration",
		"$defs":   g.defs,
	}
	for k, v := range g.defs["Config"].(map[string]any) {
		root[k] = v
	}
	delete(g.defs, "Config")
	return root
}

type schemaGen struct {
	defs map[string]any
}

func (g *schemaGen) typ(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Int32:
		if t == reflect.TypeOf(Duration(0)) {
			return map[string]any{"type": "string", "pattern": `^[0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h|d|w)$`, "description": "Duration, e.g. 30m, 24h, 30d"}
		}
		return map[string]any{"type": "integer"}
	case reflect.Float64, reflect.Float32:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": g.typ(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.typ(t.Elem())}
	case reflect.Struct:
		name := t.Name()
		if _, ok := g.defs[name]; ok {
			return map[string]any{"$ref": "#/$defs/" + name}
		}
		props := map[string]any{}
		var required []string
		g.defs[name] = map[string]any{} // placeholder against recursion
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("yaml")
			if tag == "-" || tag == "" {
				continue
			}
			key, opts, _ := strings.Cut(tag, ",")
			s := g.typ(f.Type)
			if desc := f.Tag.Get("desc"); desc != "" {
				s["description"] = desc
			}
			if name == "Check" && key == "type" {
				s["enum"] = CheckTypes()
			}
			props[key] = s
			if !strings.Contains(opts, "omitempty") {
				required = append(required, key)
			}
		}
		def := map[string]any{
			"type":                 "object",
			"properties":           props,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			def["required"] = required
		}
		g.defs[name] = def
		return map[string]any{"$ref": "#/$defs/" + name}
	case reflect.Interface:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// JSONSchemaBytes returns the indented JSON Schema document.
func JSONSchemaBytes() ([]byte, error) {
	return json.MarshalIndent(JSONSchema(), "", "  ")
}