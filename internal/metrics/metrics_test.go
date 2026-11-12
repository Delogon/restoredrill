package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/store"
)

func TestUpdateAndScrape(t *testing.T) {
	c := New()
	start := time.Now().Add(-10 * time.Second)
	finish := time.Now()
	snapTime := finish.Add(-2 * time.Hour)
	run := store.Run{
		ID: "01RUN", Drill: "service1", Status: store.StatusPass,
		StartedAt: start, FinishedAt: finish, SnapshotTime: snapTime,
		RestoredBytes: 1234,
	}
	results := []store.CheckResult{
		{Name: "sqlite_integrity", Type: "sqlite_integrity", Status: store.StatusPass},
		{Name: "http", Type: "http", Status: store.StatusPass},
	}
	c.Update(run, results)

	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	out := string(body)

	want := []string{
		`restoredrill_drill_last_run_timestamp_seconds{drill="service1"}`,
		`restoredrill_drill_last_success_timestamp_seconds{drill="service1"}`,
		`restoredrill_drill_status{drill="service1",status="pass"} 1`,
		`restoredrill_drill_status{drill="service1",status="fail"} 0`,
		`restoredrill_restored_bytes{drill="service1"} 1234`,
		`restoredrill_check_status{check="http",drill="service1",status="pass"} 1`,
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("missing metric line %q", w)
		}
	}
	// Snapshot age ~2h.
	if !strings.Contains(out, `restoredrill_snapshot_age_seconds{drill="service1"}`) {
		t.Errorf("missing snapshot age metric")
	}
}

func TestFailStatusSetsGauge(t *testing.T) {
	c := New()
	run := store.Run{Drill: "d", Status: store.StatusFail, StartedAt: time.Now(), FinishedAt: time.Now()}
	c.Update(run, nil)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Result().Body)
	out := string(body)
	if !strings.Contains(out, `restoredrill_drill_status{drill="d",status="fail"} 1`) {
		t.Errorf("fail gauge not set:\n%s", out)
	}
	if !strings.Contains(out, `restoredrill_drill_status{drill="d",status="pass"} 0`) {
		t.Errorf("pass gauge should be 0")
	}
	// No passing run -> last_success must not be present for this drill.
	if strings.Contains(out, `restoredrill_drill_last_success_timestamp_seconds{drill="d"}`) {
		t.Errorf("last_success should be absent for a never-passing drill")
	}
}