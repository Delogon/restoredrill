package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// UnmarshalYAML decodes a check and records which keys were present so
// validation can reject fields that do not belong to the check type.
func (c *Check) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("check must be a mapping")
	}
	type plain Check
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*c = Check(p)
	c.keys = mappingKeys(node)
	return nil
}

// MarshalYAML omits the internal key set.
func (c Check) MarshalYAML() (any, error) {
	type plain Check
	return plain(c), nil
}

// UnmarshalYAML decodes an expectation and records which keys were present.
func (e *Expect) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expect must be a mapping")
	}
	type plain Expect
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*e = Expect(p)
	e.keys = mappingKeys(node)
	return nil
}

// MarshalYAML omits the internal key set.
func (e Expect) MarshalYAML() (any, error) {
	type plain Expect
	return plain(e), nil
}

// UnmarshalYAML decodes a JSON expectation and records which keys were set.
func (j *JSONExpect) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("json expectation must be a mapping")
	}
	type plain JSONExpect
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	*j = JSONExpect(p)
	j.keys = mappingKeys(node)
	return nil
}

// MarshalYAML omits the internal key set.
func (j JSONExpect) MarshalYAML() (any, error) {
	type plain JSONExpect
	return plain(j), nil
}

func mappingKeys(node *yaml.Node) map[string]bool {
	keys := make(map[string]bool)
	if node.Kind != yaml.MappingNode {
		return keys
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys[node.Content[i].Value] = true
	}
	return keys
}