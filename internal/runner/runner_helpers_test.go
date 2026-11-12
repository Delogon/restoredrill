//go:build integration || docker

package runner

import (
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
	"gopkg.in/yaml.v3"
)

// pw is the fixture repository password shared by integration and docker tests.
const pw = "runner-test-pass"

// parseChecks decodes a YAML list of checks (with expectation key tracking).
func parseChecks(t *testing.T, snippet string) []config.Check {
	t.Helper()
	var out []config.Check
	if err := yaml.Unmarshal([]byte(snippet), &out); err != nil {
		t.Fatalf("parse checks: %v", err)
	}
	return out
}