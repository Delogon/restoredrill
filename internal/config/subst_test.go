package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRetagPlainScalar pins the tag re-resolution rules used after variable
// substitution: numbers and booleans are adopted, everything else stays !!str.
func TestRetagPlainScalar(t *testing.T) {
	cases := []struct {
		value   string
		style   yaml.Style
		wantTag string
	}{
		{"5", 0, "!!int"},
		{"-3", 0, "!!int"},
		{"1.5", 0, "!!float"},
		{"true", 0, "!!bool"},
		{"false", 0, "!!bool"},
		{"null", 0, "!!str"},     // must stay a string in string fields
		{"~", 0, "!!str"},        // null alias
		{"2026-01-15", 0, "!!str"}, // timestamp-like
		{"26h", 0, "!!str"},      // duration string
		{"/srv/1234", 0, "!!str"},
		{"", 0, "!!str"},
		{"a: b", 0, "!!str"},     // parses as a mapping, not a scalar
		{"line1\nline2", 0, "!!str"},
		{"5 ", 0, "!!str"},       // probe value differs (trailing space)
		{"5", yaml.DoubleQuotedStyle, "!!str"}, // quoted: author wants a string
		{"5", yaml.SingleQuotedStyle, "!!str"},
	}
	for _, c := range cases {
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: c.value, Style: c.style}
		retagPlainScalar(n)
		if n.Tag != c.wantTag {
			t.Errorf("value %q style %d: tag = %s, want %s", c.value, c.style, n.Tag, c.wantTag)
		}
		if n.Value != c.value {
			t.Errorf("value %q: retag changed the value to %q", c.value, n.Value)
		}
	}
}