package web

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/restoredrill/restoredrill/internal/store"
	"gopkg.in/yaml.v3"
)

// drillRow is one overview row.
type drillRow struct {
	Name     string
	Schedule string
	NextRun  time.Time
	Last     *store.Run
	LastPass *store.Run
	Squares  []store.Run // newest first, up to 30
	Running  bool
	Duration time.Duration // duration of the last run
}

type overviewData struct {
	Title   string
	CSRF    string
	Rows    []drillRow
	Running int
}

type drillData struct {
	Title      string
	CSRF       string
	Name       string
	Schedule   string
	NextRun    time.Time
	ConfigYAML string
	Runs       []store.Run
	Total      int
	Limit      int
	Offset     int
	HasPrev    bool
	HasNext    bool
	PrevOffset int
	NextOffset int
	SizeSpark  any
	DurSpark   any
}

type stepInfo struct {
	Step     string
	Started  time.Time
	Duration time.Duration
	Count    int
	Level    string
}

type runData struct {
	Title   string
	CSRF    string
	Run     store.Run
	Checks  []store.CheckResult
	Steps   []stepInfo
	Logs    []store.RunEvent
	LogTotal int
	Running bool
}

// overviewRows builds the per-drill overview with a constant number of
// queries, independent of the drill count (fast with 50 drills x 200 runs).
func (s *Server) overviewRows() ([]drillRow, int, error) {
	last, err := s.store.LastRunsAll()
	if err != nil {
		return nil, 0, err
	}
	pass, err := s.store.LastPassingRunsAll()
	if err != nil {
		return nil, 0, err
	}
	recent, err := s.store.RecentRunsAll(30)
	if err != nil {
		return nil, 0, err
	}
	running, err := s.store.RunningRuns()
	if err != nil {
		return nil, 0, err
	}
	runningDrills := map[string]bool{}
	for _, r := range running {
		runningDrills[r.Drill] = true
	}
	rows := make([]drillRow, 0, len(s.cfg.Drills))
	for _, name := range s.cfg.DrillNames() {
		d := s.cfg.Drills[name]
		row := drillRow{
			Name:     name,
			Schedule: d.Schedule,
			Squares:  recent[name],
			Running:  runningDrills[name],
		}
		if r, ok := last[name]; ok {
			row.Last = &r
			row.Duration = r.FinishedAt.Sub(r.StartedAt)
		}
		if r, ok := pass[name]; ok {
			row.LastPass = &r
		}
		if s.sched != nil {
			row.NextRun = s.sched.NextRun(name)
		}
		rows = append(rows, row)
	}
	return rows, len(running), nil
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	rows, running, err := s.overviewRows()
	if err != nil {
		s.log.Error("overview query failed", "err", err)
		s.renderError(w, http.StatusInternalServerError, "cannot load run history")
		return
	}
	s.render(w, http.StatusOK, "overview", overviewData{
		Title: "Overview", CSRF: s.csrfForRequest(r), Rows: rows, Running: running,
	})
}

func (s *Server) handleOverviewPartial(w http.ResponseWriter, r *http.Request) {
	rows, _, err := s.overviewRows()
	if err != nil {
		http.Error(w, "cannot load run history", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var buf bytes.Buffer
	if err := s.partials.ExecuteTemplate(&buf, "overview_rows", overviewData{Rows: rows, CSRF: s.csrfForRequest(r)}); err != nil {
		s.log.Error("partial render failed", "err", err)
		return
	}
	_, _ = io.WriteString(w, s.cfg.Redactor.String(buf.String()))
}

func (s *Server) handleDrill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	drill, ok := s.cfg.Drills[name]
	if !ok {
		s.renderError(w, http.StatusNotFound, fmt.Sprintf("drill %q is not defined", name))
		return
	}
	limit := queryInt(r, "limit", 50, 1, 200)
	offset := queryInt(r, "offset", 0, 0, 1<<30)
	runs, err := s.store.ListRuns(name, limit, offset)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "cannot load run history")
		return
	}
	total, err := s.store.CountRuns(name)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "cannot count runs")
		return
	}
	// Trends always use the newest 30 runs, regardless of the current page.
	trend, err := s.store.ListRuns(name, 30, 0)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "cannot load trends")
		return
	}
	var sizes, durs []float64
	for i := len(trend) - 1; i >= 0; i-- { // oldest first
		sizes = append(sizes, float64(trend[i].RestoredBytes))
		durs = append(durs, trend[i].FinishedAt.Sub(trend[i].StartedAt).Seconds())
	}
	cfgYAML, err := s.resolvedConfigYAML(name)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "cannot render drill config")
		return
	}
	data := drillData{
		Title: "Drill: " + name, CSRF: s.csrfForRequest(r),
		Name: name, Schedule: drill.Schedule, ConfigYAML: cfgYAML,
		Runs: runs, Total: total, Limit: limit, Offset: offset,
		HasPrev: offset > 0, HasNext: offset+limit < total,
		PrevOffset: maxInt(0, offset-limit), NextOffset: offset + limit,
		SizeSpark: sparkline(sizes), DurSpark: sparkline(durs),
	}
	if s.sched != nil {
		data.NextRun = s.sched.NextRun(name)
	}
	s.render(w, http.StatusOK, "drill", data)
}

// resolvedConfigYAML renders the drill and its repository with secrets
// redacted. Secret file paths stay visible (they are not secret values).
func (s *Server) resolvedConfigYAML(name string) (string, error) {
	drill := s.cfg.Drills[name]
	repo := s.cfg.Repositories[drill.Repository]
	doc := map[string]any{
		"drill":      drill,
		"repository": repo,
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return s.cfg.Redactor.String(string(b)), nil
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	data, status, err := s.runPageData(r)
	if err != nil {
		s.renderError(w, status, err.Error())
		return
	}
	s.render(w, status, "run", data)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	data, status, err := s.runPageData(r)
	if err != nil {
		s.renderError(w, status, err.Error())
		return
	}
	s.render(w, status, "live", data)
}

func (s *Server) runPageData(r *http.Request) (runData, int, error) {
	id := r.PathValue("id")
	run, err := s.store.GetRun(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return runData{}, http.StatusNotFound, fmt.Errorf("run %q not found", id)
		}
		return runData{}, http.StatusInternalServerError, fmt.Errorf("cannot load run")
	}
	checks, err := s.store.ListCheckResults(id)
	if err != nil {
		return runData{}, http.StatusInternalServerError, fmt.Errorf("cannot load checks")
	}
	events, err := s.store.ListEvents(id)
	if err != nil {
		return runData{}, http.StatusInternalServerError, fmt.Errorf("cannot load log")
	}
	data := runData{
		Title: "Run " + id, CSRF: s.csrfForRequest(r),
		Run: run, Checks: checks, Steps: stepsFromEvents(events),
		Running: run.Status == store.StatusRunning, LogTotal: len(events),
	}
	// Log tail: the last 200 lines; the full log is available for download.
	if len(events) > 200 {
		data.Logs = events[len(events)-200:]
	} else {
		data.Logs = events
	}
	return data, http.StatusOK, nil
}

// stepsFromEvents groups the log into a lifecycle timeline, preserving the
// order in which steps first appeared.
func stepsFromEvents(events []store.RunEvent) []stepInfo {
	var out []stepInfo
	idx := map[string]int{}
	for _, e := range events {
		i, ok := idx[e.Step]
		if !ok {
			idx[e.Step] = len(out)
			out = append(out, stepInfo{Step: e.Step, Started: e.TS, Level: e.Level})
			i = len(out) - 1
		}
		st := &out[i]
		st.Count++
		st.Duration = e.TS.Sub(st.Started)
		if levelRank(e.Level) > levelRank(st.Level) {
			st.Level = e.Level
		}
	}
	return out
}

func levelRank(level string) int {
	switch level {
	case "error":
		return 3
	case "warn":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

// handleFormRun is the dashboard "Run now" form target. It queues the run and
// redirects back to the drill page.
func (s *Server) handleFormRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.cfg.Drills[name]; !ok {
		s.renderError(w, http.StatusNotFound, fmt.Sprintf("drill %q is not defined", name))
		return
	}
	if !s.checkRunRateLimit(w, r, name) {
		return
	}
	opts := runOptionsFromForm(r)
	s.triggerRun(name, opts)
	http.Redirect(w, r, "/drills/"+name, http.StatusSeeOther)
}

func (s *Server) handleLogDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetRun(id); err != nil {
		s.renderError(w, http.StatusNotFound, fmt.Sprintf("run %q not found", id))
		return
	}
	events, err := s.store.ListEvents(id)
	if err != nil {
		s.renderError(w, http.StatusInternalServerError, "cannot load log")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", id+".log"))
	for _, e := range events {
		line := fmt.Sprintf("%s %-5s %-12s %s\n", e.TS.UTC().Format("2006-01-02T15:04:05.000Z"), e.Level, e.Step, e.Message)
		_, _ = io.WriteString(w, s.cfg.Redactor.String(line))
	}
}

func queryInt(r *http.Request, key string, def, min, max int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}