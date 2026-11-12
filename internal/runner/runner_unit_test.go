package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/restoredrill/restoredrill/internal/store"
)

func TestAggregate(t *testing.T) {
	cases := []struct {
		name   string
		status []string
		want   string
	}{
		{"all pass", []string{"pass", "pass"}, "pass"},
		{"empty", nil, "pass"},
		{"one fail", []string{"pass", "fail"}, "fail"},
		{"one error", []string{"pass", "error"}, "error"},
		{"fail beats error", []string{"error", "fail"}, "fail"},
		{"skipped ignored", []string{"pass", "skipped"}, "pass"},
		{"only skipped", []string{"skipped"}, "pass"},
	}
	for _, c := range cases {
		var results []store.CheckResult
		for _, s := range c.status {
			results = append(results, store.CheckResult{Status: s})
		}
		if got := aggregate(results); got != c.want {
			t.Errorf("%s: aggregate=%s want %s", c.name, got, c.want)
		}
	}
}

func TestPercentDropHelper(t *testing.T) {
	// percentDrop lives in checks; sanity-check walkStats here instead.
	dir := t.TempDir()
	if err := writeTree(dir); err != nil {
		t.Fatal(err)
	}
	bytes, files, err := walkStats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || bytes != 6 {
		t.Fatalf("walkStats = %d bytes, %d files; want 6, 2", bytes, files)
	}
}

func writeTree(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("abc"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "sub", "b"), []byte("xyz"), 0o644)
}