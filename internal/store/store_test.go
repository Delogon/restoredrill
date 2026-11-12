package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRunLifecycle(t *testing.T) {
	s := openTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Run{ID: "01J0000000000000000000000A", Drill: "d", Trigger: TriggerCLI, Status: StatusRunning, StartedAt: now}
	if err := s.InsertRun(r); err != nil {
		t.Fatal(err)
	}
	r.Status = StatusPass
	r.FinishedAt = now.Add(time.Second)
	r.RestoredBytes = 100
	r.RestoredFiles = 3
	if err := s.UpdateRun(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPass || got.RestoredBytes != 100 || got.RestoredFiles != 3 {
		t.Fatalf("got %+v", got)
	}
	if !got.FinishedAt.Equal(r.FinishedAt) {
		t.Fatalf("finishedAt %v != %v", got.FinishedAt, r.FinishedAt)
	}
}

func TestLastPassingAndRetention(t *testing.T) {
	s := openTest(t)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		status := StatusPass
		if i%2 == 1 {
			status = StatusFail
		}
		r := Run{ID: string(rune('A'+i)) + "AAAAAAAAAAAAAAAAAAAAAAAAA", Drill: "d", Trigger: TriggerCLI, Status: status, StartedAt: base.Add(time.Duration(i) * time.Second)}
		if err := s.InsertRun(r); err != nil {
			t.Fatal(err)
		}
	}
	last, err := s.LastRun("d")
	if err != nil || last.Status != StatusPass {
		t.Fatalf("last run: %+v %v", last, err)
	}
	pass, err := s.LastPassingRun("d")
	if err != nil || pass.Status != StatusPass {
		t.Fatalf("last passing: %+v %v", pass, err)
	}
	if err := s.ApplyRetention("d", 2); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountRuns("d")
	if n != 2 {
		t.Fatalf("retention kept %d, want 2", n)
	}
}

func TestCheckResultsAndEvents(t *testing.T) {
	s := openTest(t)
	r := Run{ID: "R1", Drill: "d", Trigger: TriggerCLI, Status: StatusRunning, StartedAt: time.Now()}
	s.InsertRun(r)
	c := CheckResult{RunID: "R1", Seq: 0, Name: "files_exist[0]", Type: "files_exist", Phase: "data", Status: StatusPass, StartedAt: time.Now(), DurationMS: 5, Message: "ok"}
	if err := s.InsertCheckResult(c); err != nil {
		t.Fatal(err)
	}
	s.InsertEvent(RunEvent{RunID: "R1", TS: time.Now(), Level: "info", Step: "restore", Message: "restored"})
	cs, err := s.ListCheckResults("R1")
	if err != nil || len(cs) != 1 || cs[0].Name != "files_exist[0]" {
		t.Fatalf("checks: %+v %v", cs, err)
	}
	ev, err := s.ListEvents("R1")
	if err != nil || len(ev) != 1 {
		t.Fatalf("events: %+v %v", ev, err)
	}
}

func TestNotifications(t *testing.T) {
	s := openTest(t)
	r := Run{ID: "R1", Drill: "d", Trigger: TriggerCLI, Status: StatusFail, StartedAt: time.Now()}
	s.InsertRun(r)
	n := Notification{RunID: "R1", Notifier: "ntfy", Event: "fail", SentAt: time.Now(), OK: true}
	if err := s.InsertNotification(n); err != nil {
		t.Fatal(err)
	}
	got, err := s.LastNotified("d", "ntfy", "fail")
	if err != nil || !got.OK || got.Notifier != "ntfy" {
		t.Fatalf("last notified: %+v %v", got, err)
	}
	none, err := s.LastNotified("d", "ntfy", "recovered")
	if err != nil || none.RunID != "" {
		t.Fatalf("expected no notification, got %+v %v", none, err)
	}
}

func TestActiveRuns(t *testing.T) {
	s := openTest(t)
	s.InsertRun(Run{ID: "R1", Drill: "d", Trigger: TriggerCLI, Status: StatusRunning, StartedAt: time.Now()})
	s.InsertRun(Run{ID: "R2", Drill: "d", Trigger: TriggerCLI, Status: StatusPass, StartedAt: time.Now()})
	active, err := s.ActiveRuns()
	if err != nil || len(active) != 1 || active[0].ID != "R1" {
		t.Fatalf("active: %+v %v", active, err)
	}
}