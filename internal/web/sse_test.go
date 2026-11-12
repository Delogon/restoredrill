package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/store"
)

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	name string
	data string
}

// readSSEEvent reads one event from the stream, skipping heartbeat comments
// and blank separators. It returns an error when the stream ends.
func readSSEEvent(br *bufio.Reader) (sseEvent, error) {
	var ev sseEvent
	var data []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return ev, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if ev.name != "" || len(data) > 0 {
				ev.data = strings.Join(data, "\n")
				return ev, nil
			}
		case strings.HasPrefix(line, ":"):
			// heartbeat comment — ignore
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
}

// openSSE opens a streaming GET against a real test server with a timeout so
// a broken handler cannot hang the test forever.
func openSSE(t *testing.T, s *Server, path string) (*bufio.Reader, func()) {
	t.Helper()
	ts := httptest.NewServer(s.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", ts.URL+path, nil)
	if err != nil {
		cancel()
		ts.Close()
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		ts.Close()
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		resp.Body.Close()
		cancel()
		ts.Close()
		t.Fatalf("content-type=%q, want text/event-stream", ct)
	}
	cleanup := func() {
		resp.Body.Close()
		cancel()
		ts.Close()
	}
	return bufio.NewReader(resp.Body), cleanup
}

func TestRunEventsFinishedRun(t *testing.T) {
	s, _ := fixture(t, nil)
	// 01A2 is finished (fail): the stream sends run + done and returns.
	rec := get(s, "/runs/01A2/events")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
	body := mustBody(t, rec)
	if !strings.Contains(body, "event: run") || !strings.Contains(body, "event: done") {
		t.Fatalf("expected run and done events, got:\n%s", body)
	}
	if !strings.Contains(body, `"status":"fail"`) {
		t.Fatalf("done payload missing fail status:\n%s", body)
	}
}

func TestRunEventsNotFound(t *testing.T) {
	s, _ := fixture(t, nil)
	if rec := get(s, "/runs/missing/events"); rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRunEventsRunningRun(t *testing.T) {
	s, st := fixture(t, nil)
	canary := "live-secret-canary-1234"
	s.cfg.Redactor.Add(canary)

	run := store.Run{ID: "01LIVE", Drill: "alpha", Trigger: store.TriggerManual,
		Status: store.StatusRunning, StartedAt: fixedNow}
	if err := st.InsertRun(run); err != nil {
		t.Fatal(err)
	}

	br, cleanup := openSSE(t, s, "/runs/01LIVE/events")
	defer cleanup()

	// First event is the current (running) run.
	ev, err := readSSEEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	if ev.name != "run" || !strings.Contains(ev.data, `"status":"running"`) {
		t.Fatalf("first event = %+v", ev)
	}

	// Simulate progress: a log line (with a secret) and a finished run.
	if err := st.InsertEvent(store.RunEvent{RunID: "01LIVE", TS: fixedNow, Level: "info",
		Step: "restore", Message: "auth with " + canary}); err != nil {
		t.Fatal(err)
	}
	run.Status = store.StatusPass
	run.FinishedAt = fixedNow.Add(time.Second)
	if err := st.UpdateRun(run); err != nil {
		t.Fatal(err)
	}

	var gotLog, gotDone bool
	for !gotDone {
		ev, err := readSSEEvent(br)
		if err != nil {
			t.Fatalf("stream ended before done: %v", err)
		}
		switch ev.name {
		case "log":
			gotLog = true
			if strings.Contains(ev.data, canary) {
				t.Errorf("secret canary leaked in SSE log event: %s", ev.data)
			}
			if !strings.Contains(ev.data, "[REDACTED]") {
				t.Errorf("SSE log event not redacted: %s", ev.data)
			}
		case "done":
			gotDone = true
			if !strings.Contains(ev.data, `"status":"pass"`) {
				t.Errorf("done payload missing pass status: %s", ev.data)
			}
		}
	}
	if !gotLog {
		t.Error("no log event was streamed")
	}
}

func TestOverviewEventsChanged(t *testing.T) {
	s, st := fixture(t, nil)
	br, cleanup := openSSE(t, s, "/events")
	defer cleanup()

	// First event is the hello with the current fingerprint.
	ev, err := readSSEEvent(br)
	if err != nil {
		t.Fatal(err)
	}
	if ev.name != "hello" {
		t.Fatalf("first event = %+v, want hello", ev)
	}

	// Change the overview: a newer run for beta changes its last-run status.
	if err := st.InsertRun(store.Run{ID: "01NEW", Drill: "beta", Trigger: store.TriggerManual,
		Status: store.StatusPass, StartedAt: fixedNow.Add(time.Minute), FinishedAt: fixedNow.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	for {
		ev, err := readSSEEvent(br)
		if err != nil {
			t.Fatalf("stream ended before changed: %v", err)
		}
		if ev.name == "changed" {
			if !strings.Contains(ev.data, "fingerprint") {
				t.Errorf("changed payload missing fingerprint: %s", ev.data)
			}
			return
		}
	}
}