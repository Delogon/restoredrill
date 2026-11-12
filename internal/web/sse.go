package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/store"
)

// sseWriter wraps the response for server-sent events.
type sseWriter struct {
	w        http.ResponseWriter
	f        http.Flusher
	redactor *redact.Redactor
}

func newSSEWriter(w http.ResponseWriter, redactor *redact.Redactor) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, f: f, redactor: redactor}, true
}

// send writes one named event with a JSON payload. The payload passes through
// the redactor before it leaves the process.
func (s *sseWriter) send(event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data := s.redactor.String(string(b))
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sseWriter) heartbeat() error {
	if _, err := fmt.Fprint(s.w, ": keep-alive\n\n"); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// handleRunEvents streams a run's log events, then a final "done" event with
// the finished run. Clients that only read HTML still get the full log on the
// run page; this endpoint powers the live updates.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(id)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	sse, ok := newSSEWriter(w, s.cfg.Redactor)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if err := sse.send("run", run); err != nil {
		return
	}
	if run.Status != store.StatusRunning {
		_ = sse.send("done", run)
		return
	}

	sent := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sse.heartbeat(); err != nil {
				return
			}
		case <-ticker.C:
			events, err := s.store.ListEvents(id)
			if err == nil && len(events) > sent {
				for _, e := range events[sent:] {
					if err := sse.send("log", e); err != nil {
						return
					}
				}
				sent = len(events)
			}
			cur, err := s.store.GetRun(id)
			if err == nil && cur.Status != store.StatusRunning {
				// Flush any events written after the last poll, then finish.
				if events, err := s.store.ListEvents(id); err == nil && len(events) > sent {
					for _, e := range events[sent:] {
						if err := sse.send("log", e); err != nil {
							return
						}
					}
				}
				_ = sse.send("done", cur)
				return
			}
		}
	}
}

// handleOverviewEvents notifies the overview when anything changes (a run
// starts or finishes). The page then refreshes its table fragment.
func (s *Server) handleOverviewEvents(w http.ResponseWriter, r *http.Request) {
	sse, ok := newSSEWriter(w, s.cfg.Redactor)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	last := s.overviewFingerprint()
	_ = sse.send("hello", map[string]string{"fingerprint": last})
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sse.heartbeat(); err != nil {
				return
			}
		case <-ticker.C:
			fp := s.overviewFingerprint()
			if fp != last {
				last = fp
				if err := sse.send("changed", map[string]string{"fingerprint": fp}); err != nil {
					return
				}
			}
		}
	}
}

// overviewFingerprint summarises running runs and each drill's last run, so
// any start/finish/status change produces a new value.
func (s *Server) overviewFingerprint() string {
	var sb strings.Builder
	if running, err := s.store.RunningRuns(); err == nil {
		for _, r := range running {
			fmt.Fprintf(&sb, "R%s;", r.ID)
		}
	}
	if last, err := s.store.LastRunsAll(); err == nil {
		for _, name := range s.cfg.DrillNames() {
			if r, ok := last[name]; ok {
				fmt.Fprintf(&sb, "%s=%s/%s;", name, r.ID, r.Status)
			}
		}
	}
	return sb.String()
}