// Package metrics exposes Prometheus metrics about drills and runs. It is
// updated after every run (runner.MetricsFunc) and scraped at /metrics.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/restoredrill/restoredrill/internal/store"
)

// statuses are the run/check statuses tracked by the status gauges.
var statuses = []string{store.StatusPass, store.StatusFail, store.StatusError, store.StatusSkipped}

// Collector holds the metric vectors and updates them after each run.
type Collector struct {
	reg *prometheus.Registry

	lastRun     *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
	drillStatus *prometheus.GaugeVec
	duration    *prometheus.GaugeVec
	snapshotAge *prometheus.GaugeVec
	restored    *prometheus.GaugeVec
	checkStatus *prometheus.GaugeVec
}

// New creates and registers all collectors.
func New() *Collector {
	reg := prometheus.NewRegistry()
	c := &Collector{
		reg: reg,
		lastRun: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_drill_last_run_timestamp_seconds",
			Help: "Unix timestamp of the last completed run of a drill.",
		}, []string{"drill"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_drill_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last passing run of a drill.",
		}, []string{"drill"}),
		drillStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_drill_status",
			Help: "Current status of a drill (1 for the active status).",
		}, []string{"drill", "status"}),
		duration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_run_duration_seconds",
			Help: "Duration of the last run of a drill, in seconds.",
		}, []string{"drill"}),
		snapshotAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_snapshot_age_seconds",
			Help: "Age of the snapshot tested by the last run, in seconds.",
		}, []string{"drill"}),
		restored: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_restored_bytes",
			Help: "Bytes restored by the last run of a drill.",
		}, []string{"drill"}),
		checkStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "restoredrill_check_status",
			Help: "Status of each check in the last run (1 for the active status).",
		}, []string{"drill", "check", "status"}),
	}
	reg.MustRegister(c.lastRun, c.lastSuccess, c.drillStatus, c.duration, c.snapshotAge, c.restored, c.checkStatus)
	return c
}

// Update refreshes the metrics from a completed run and its check results. It
// matches runner.MetricsFunc.
func (c *Collector) Update(run store.Run, results []store.CheckResult) {
	if run.FinishedAt.IsZero() {
		return
	}
	drill := run.Drill
	c.lastRun.WithLabelValues(drill).Set(float64(run.FinishedAt.Unix()))
	if run.Status == store.StatusPass {
		c.lastSuccess.WithLabelValues(drill).Set(float64(run.FinishedAt.Unix()))
	}
	for _, s := range statuses {
		v := 0.0
		if s == run.Status {
			v = 1.0
		}
		c.drillStatus.WithLabelValues(drill, s).Set(v)
	}
	c.duration.WithLabelValues(drill).Set(run.FinishedAt.Sub(run.StartedAt).Seconds())
	if !run.SnapshotTime.IsZero() {
		c.snapshotAge.WithLabelValues(drill).Set(run.FinishedAt.Sub(run.SnapshotTime).Seconds())
	}
	c.restored.WithLabelValues(drill).Set(float64(run.RestoredBytes))

	for _, res := range results {
		name := res.Name
		if name == "" {
			name = res.Type
		}
		for _, s := range statuses {
			v := 0.0
			if s == res.Status {
				v = 1.0
			}
			c.checkStatus.WithLabelValues(drill, name, s).Set(v)
		}
	}
}

// Handler returns the /metrics HTTP handler.
func (c *Collector) Handler() http.Handler {
	return promhttp.HandlerFor(c.reg, promhttp.HandlerOpts{})
}