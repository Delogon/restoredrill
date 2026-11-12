package sandbox

import (
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
)

func TestResourcesForDefaultsAndOverride(t *testing.T) {
	defaults := &config.Resources{CPUs: 2, Memory: "2GiB"}
	rt := NewRuntime(nil, "run", "drill", defaults, nil)

	// No service override -> defaults.
	cpus, mem := rt.resourcesFor(nil)
	if cpus != 2 || mem != 2<<30 {
		t.Fatalf("defaults: cpus=%v mem=%v", cpus, mem)
	}
	// Service override wins.
	svc := &config.Service{Resources: &config.Resources{CPUs: 0.5, Memory: "512MiB"}}
	cpus, mem = rt.resourcesFor(svc)
	if cpus != 0.5 || mem != 512<<20 {
		t.Fatalf("override: cpus=%v mem=%v", cpus, mem)
	}
	// No defaults, no override -> unlimited (0).
	rt2 := NewRuntime(nil, "run", "drill", nil, nil)
	cpus, mem = rt2.resourcesFor(nil)
	if cpus != 0 || mem != 0 {
		t.Fatalf("unlimited: cpus=%v mem=%v", cpus, mem)
	}
}