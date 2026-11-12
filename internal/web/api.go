package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/store"
)

// writeJSON writes the versioned API envelope. Everything passes through the
// redactor before it leaves the process.
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		s.log.Error("json marshal failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body := s.cfg.Redactor.String(string(b))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"schema":1,"data":`+body+`}`)
}

func (s *Server) writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	b, _ := json.Marshal(s.cfg.Redactor.String(msg))
	_, _ = io.WriteString(w, `{"schema":1,"error":`+string(b)+`}`)
}

// apiDrills lists all drills with their last status.
func (s *Server) apiDrills(w http.ResponseWriter, r *http.Request) {
	last, err := s.store.LastRunsAll()
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot load runs")
		return
	}
	pass, err := s.store.LastPassingRunsAll()
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot load runs")
		return
	}
	running, err := s.store.RunningRuns()
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot load runs")
		return
	}
	runningDrills := map[string]bool{}
	for _, run := range running {
		runningDrills[run.Drill] = true
	}
	type item struct {
		Name        string     `json:"name"`
		Schedule    string     `json:"schedule,omitempty"`
		NextRun     *time.Time `json:"next_run,omitempty"`
		Running     bool       `json:"running"`
		LastRun     *store.Run `json:"last_run,omitempty"`
		LastSuccess *store.Run `json:"last_success,omitempty"`
	}
	out := make([]item, 0, len(s.cfg.Drills))
	for _, name := range s.cfg.DrillNames() {
		it := item{Name: name, Schedule: s.cfg.Drills[name].Schedule, Running: runningDrills[name]}
		if run, ok := last[name]; ok {
			it.LastRun = &run
		}
		if run, ok := pass[name]; ok {
			it.LastSuccess = &run
		}
		if s.sched != nil {
			if next := s.sched.NextRun(name); !next.IsZero() {
				it.NextRun = &next
			}
		}
		out = append(out, it)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// apiDrill returns the resolved drill config with secrets redacted.
func (s *Server) apiDrill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	drill, ok := s.cfg.Drills[name]
	if !ok {
		s.writeAPIError(w, http.StatusNotFound, fmt.Sprintf("drill %q is not defined", name))
		return
	}
	yamlStr, err := s.resolvedConfigYAML(name)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot render drill config")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"name":        name,
		"drill":       drill,
		"repository":  s.cfg.Repositories[drill.Repository],
		"config_yaml": yamlStr,
	})
}

// apiDrillRuns returns paginated run history.
func (s *Server) apiDrillRuns(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.cfg.Drills[name]; !ok {
		s.writeAPIError(w, http.StatusNotFound, fmt.Sprintf("drill %q is not defined", name))
		return
	}
	limit := queryInt(r, "limit", 50, 1, 200)
	offset := queryInt(r, "offset", 0, 0, 1<<30)
	runs, err := s.store.ListRuns(name, limit, offset)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot load runs")
		return
	}
	total, err := s.store.CountRuns(name)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot count runs")
		return
	}
	if runs == nil {
		runs = []store.Run{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"runs": runs, "total": total, "limit": limit, "offset": offset,
	})
}

// apiRunGet returns one run with its check results.
func (s *Server) apiRunGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(id)
	if err != nil {
		s.writeAPIError(w, http.StatusNotFound, fmt.Sprintf("run %q not found", id))
		return
	}
	checks, err := s.store.ListCheckResults(id)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot load checks")
		return
	}
	if checks == nil {
		checks = []store.CheckResult{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"run": run, "checks": checks})
}

// apiRunLogs returns the redacted run log.
func (s *Server) apiRunLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetRun(id); err != nil {
		s.writeAPIError(w, http.StatusNotFound, fmt.Sprintf("run %q not found", id))
		return
	}
	events, err := s.store.ListEvents(id)
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, "cannot load log")
		return
	}
	if events == nil {
		events = []store.RunEvent{}
	}
	s.writeJSON(w, http.StatusOK, events)
}

// apiRun triggers a run. CSRF (session) or API token auth is enforced by the
// csrfProtect wrapper. The run executes asynchronously: the response is 202
// and the client polls the run history.
func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.cfg.Drills[name]; !ok {
		s.writeAPIError(w, http.StatusNotFound, fmt.Sprintf("drill %q is not defined", name))
		return
	}
	if !s.checkRunRateLimit(w, r, name) {
		return
	}
	opts := runner.RunOptions{}
	if r.Body != nil && r.ContentLength != 0 {
		var body struct {
			Snapshot string `json:"snapshot"`
			Keep     bool   `json:"keep"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			s.writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		opts.SnapshotID = body.Snapshot
		opts.Keep = body.Keep
	}
	s.triggerRun(name, opts)
	s.writeJSON(w, http.StatusAccepted, map[string]any{"drill": name, "status": "queued"})
}

// runOptionsFromForm reads the dashboard form fields.
func runOptionsFromForm(r *http.Request) runner.RunOptions {
	opts := runner.RunOptions{SnapshotID: r.FormValue("snapshot")}
	if v := r.FormValue("keep"); v == "on" || v == "true" || v == "1" {
		opts.Keep = true
	}
	return opts
}

// triggerRun queues a manual run on the runner. The runner applies the global
// concurrency queue and per-drill lock (a second concurrent run is skipped).
func (s *Server) triggerRun(name string, opts runner.RunOptions) {
	if s.runner == nil {
		return
	}
	opts.Trigger = store.TriggerManual
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		run, err := s.runner.Run(ctx, name, opts)
		if err != nil {
			s.log.Error("manual run failed", "drill", name, "err", err)
			return
		}
		s.log.Info("manual run finished", "drill", name, "run", run.ID, "status", run.Status)
	}()
}