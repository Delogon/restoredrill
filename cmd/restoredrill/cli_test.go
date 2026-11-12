package main

import (
	"testing"

	"github.com/restoredrill/restoredrill/internal/store"
)

func TestExitCodeForRuns(t *testing.T) {
	cases := []struct {
		name   string
		status []string
		want   int
	}{
		{"all pass", []string{"pass", "pass"}, exitOK},
		{"one fail", []string{"pass", "fail"}, exitFail},
		{"fail beats error", []string{"error", "fail"}, exitFail},
		{"only error", []string{"error"}, exitError},
		{"skipped only", []string{"skipped"}, exitOK},
		{"empty", nil, exitOK},
	}
	for _, c := range cases {
		var runs []store.Run
		for _, s := range c.status {
			runs = append(runs, store.Run{Status: s})
		}
		err := exitCodeForRuns(runs)
		got := exitOK
		if err != nil {
			ec, ok := err.(exitCodeError)
			if !ok {
				t.Fatalf("%s: unexpected error type %T", c.name, err)
			}
			got = ec.code
		}
		if got != c.want {
			t.Errorf("%s: exit=%d want %d", c.name, got, c.want)
		}
	}
}

func TestJSONEnvelope(t *testing.T) {
	env := jsonEnvelope(map[string]any{"x": 1})
	if env["schema"] != 1 {
		t.Fatalf("schema = %v, want 1", env["schema"])
	}
	if _, ok := env["data"]; !ok {
		t.Fatal("missing data key")
	}
}