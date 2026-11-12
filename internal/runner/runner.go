// Package runner executes drills through the full run lifecycle: lock,
// preflight, snapshot selection, space check, restore, checks, teardown,
// persist and notify. Teardown runs whatever happens before it.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/backend/borg"
	"github.com/restoredrill/restoredrill/internal/backend/restic"
	"github.com/restoredrill/restoredrill/internal/checks"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/sandbox"
	"github.com/restoredrill/restoredrill/internal/store"
	"github.com/restoredrill/restoredrill/internal/ulid"
	"gopkg.in/yaml.v3"
)

// RunOptions control a single run.
type RunOptions struct {
	Trigger    string // schedule | manual | cli
	SnapshotID string // override snapshot selection
	Keep       bool   // force keeping the sandbox
	NoNotify   bool
	Seed       int64 // override random seed (0 = random)
}

// NotifyFunc is called after a run completes (wired in M4).
type NotifyFunc func(ctx context.Context, run store.Run, results []store.CheckResult)

// MetricsFunc updates metrics after a run (wired in M4).
type MetricsFunc func(run store.Run, results []store.CheckResult)

// RuntimeFactory builds the container runtime for a drill (wired in M3). It
// returns the runtime, a teardown function, and an error. nil means the drill
// needs no containers.
type RuntimeFactory func(ctx context.Context, sb *sandbox.Sandbox, drill config.Drill) (checks.ContainerRuntime, func(), error)

// Runner executes drills.
type Runner struct {
	Cfg         *config.Config
	Store       *store.Store
	Log         *slog.Logger
	Concurrency int
	Notify      NotifyFunc
	Metrics     MetricsFunc
	Runtime     RuntimeFactory

	sem        chan struct{}
	mu         sync.Mutex
	drillLocks map[string]*sync.Mutex
	active     map[string]context.CancelFunc
}

// New creates a runner. concurrency <= 0 defaults to 1.
func New(cfg *config.Config, st *store.Store, log *slog.Logger, concurrency int) *Runner {
	if concurrency <= 0 {
		concurrency = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		Cfg:         cfg,
		Store:       st,
		Log:         log,
		Concurrency: concurrency,
		sem:         make(chan struct{}, concurrency),
		drillLocks:  map[string]*sync.Mutex{},
		active:      map[string]context.CancelFunc{},
	}
}

func (r *Runner) drillLock(name string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.drillLocks[name]
	if !ok {
		l = &sync.Mutex{}
		r.drillLocks[name] = l
	}
	return l
}

// ActiveRunIDs returns the IDs of runs currently executing.
func (r *Runner) ActiveRunIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.active))
	for id := range r.active {
		ids = append(ids, id)
	}
	return ids
}

// Run executes one drill and returns the persisted run record.
func (r *Runner) Run(ctx context.Context, drillName string, opts RunOptions) (store.Run, error) {
	drill, ok := r.Cfg.Drills[drillName]
	if !ok {
		return store.Run{}, fmt.Errorf("drill %q is not defined — run \"restoredrill validate\" to list drills", drillName)
	}
	if opts.Trigger == "" {
		opts.Trigger = store.TriggerCLI
	}

	// Step 1: per-drill lock. If already running, record a skipped run.
	lock := r.drillLock(drillName)
	if !lock.TryLock() {
		run := store.Run{ID: ulid.New(), Drill: drillName, Trigger: opts.Trigger, Status: store.StatusSkipped, StartedAt: time.Now(), FinishedAt: time.Now()}
		_ = r.Store.InsertRun(run)
		r.logEvent(run.ID, "info", "lock", "skipped: a previous run of this drill is still active")
		return run, nil
	}
	defer lock.Unlock()

	// Global concurrency limit.
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return store.Run{}, ctx.Err()
	}

	runID := ulid.New()
	run := store.Run{
		ID:         runID,
		Drill:      drillName,
		Trigger:    opts.Trigger,
		Status:     store.StatusRunning,
		StartedAt:  time.Now(),
		Repository: drill.Repository,
		ConfigHash: drillHash(drill),
	}
	if err := r.Store.InsertRun(run); err != nil {
		return run, err
	}

	// Register as active with a cancellable context for the whole drill.
	dctx, cancel := context.WithTimeout(ctx, drill.Timeout.D())
	r.mu.Lock()
	r.active[runID] = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.active, runID)
		r.mu.Unlock()
	}()

	// Execute the lifecycle; teardown is deferred inside.
	status, errKind, errMsg, keptPath := r.execute(dctx, &run, drill, opts)
	run.Status = status
	run.ErrorKind = errKind
	run.ErrorMessage = r.Cfg.Redactor.String(errMsg)
	run.KeptPath = keptPath
	run.FinishedAt = time.Now()

	if err := r.Store.UpdateRun(run); err != nil {
		r.Log.Error("cannot persist run", "run", runID, "err", err)
	}
	_ = r.Store.ApplyRetention(drillName, r.Cfg.Retention.RunsPerDrill)

	results, _ := r.Store.ListCheckResults(runID)
	if r.Metrics != nil {
		r.Metrics(run, results)
	}
	if r.Notify != nil && !opts.NoNotify {
		r.Notify(ctx, run, results)
	}
	return run, nil
}

// execute runs the lifecycle steps and returns the aggregate status. Teardown
// (step 10) is deferred so it runs on failure, timeout, cancellation or panic.
func (r *Runner) execute(ctx context.Context, run *store.Run, drill config.Drill, opts RunOptions) (status, errKind, errMsg, keptPath string) {
	// Recover from panics so teardown still runs and the run is marked error.
	defer func() {
		if rec := recover(); rec != nil {
			status, errKind, errMsg = store.StatusError, "internal", fmt.Sprintf("internal panic: %v", rec)
			r.logEvent(run.ID, "error", "panic", errMsg)
		}
	}()

	var sb *sandbox.Sandbox
	var runtime checks.ContainerRuntime
	var runtimeTeardown func()
	tornDown := false
	teardown := func() {
		if tornDown {
			return
		}
		tornDown = true
		if runtimeTeardown != nil {
			runtimeTeardown()
		}
		if sb != nil {
			keep := opts.Keep || (drill.KeepOnFailure != nil && *drill.KeepOnFailure && status != store.StatusPass)
			if keep {
				keptPath = sb.Root
				r.logEvent(run.ID, "info", "teardown", "keeping sandbox at "+sb.Root)
			} else {
				if err := sb.Cleanup(); err != nil {
					r.logEvent(run.ID, "warn", "teardown", "cannot remove sandbox: "+err.Error())
				} else {
					r.logEvent(run.ID, "info", "teardown", "sandbox removed")
				}
			}
		}
	}
	defer teardown()

	fail := func(kind, msg string) (string, string, string, string) {
		r.logEvent(run.ID, "error", "lifecycle", msg)
		return store.StatusError, kind, msg, keptPath
	}

	// Step 2: preflight — backend version and repository reachability.
	repo := r.Cfg.Repositories[drill.Repository]
	be, err := r.backendFor(repo)
	if err != nil {
		return fail("config", err.Error())
	}
	run.Backend = be.Kind()
	if _, err := be.Version(ctx); err != nil {
		return fail(kindOf(err), err.Error())
	}
	r.logEvent(run.ID, "info", "preflight", be.Kind()+" backend ready")

	// Step 3: select snapshot.
	filter := backend.SnapshotFilter{Tags: drill.Snapshot.Tags, Host: drill.Snapshot.Host, Paths: drill.Snapshot.Paths, ArchiveGlob: drill.Snapshot.ArchiveGlob}
	snaps, err := be.ListSnapshots(ctx, filter)
	if err != nil {
		return fail(kindOf(err), err.Error())
	}
	seed := opts.Seed
	if seed == 0 {
		seed = rand.Int63()
	}
	run.Seed = seed
	var snap backend.Snapshot
	if opts.SnapshotID != "" {
		found := false
		for _, s := range snaps {
			if s.ID == opts.SnapshotID {
				snap, found = s, true
				break
			}
		}
		if !found {
			return fail("notfound", fmt.Sprintf("snapshot %q not found in repository", opts.SnapshotID))
		}
	} else {
		snap, err = backend.SelectSnapshot(snaps, backend.Selection{Select: drill.Snapshot.Select, Within: drill.Snapshot.Within.D(), Seed: seed}, time.Now())
		if err != nil {
			return fail(kindOf(err), err.Error())
		}
	}
	if d, ok := be.(backend.SnapshotDetailer); ok {
		if enriched, e := d.SnapshotDetails(ctx, snap); e == nil {
			snap = enriched
		}
	}
	run.SnapshotID = snap.ID
	run.SnapshotTime = snap.Time
	run.SnapshotHost = snap.Host
	r.logEvent(run.ID, "info", "snapshot", fmt.Sprintf("selected snapshot %s (time %s, host %s)", snap.ID, snap.Time.Format(time.RFC3339), snap.Host))

	// Step 4: space check (before restoring anything).
	est, err := be.EstimateRestoreSize(ctx, snap, drill.Restore.Paths)
	if err != nil {
		return fail(kindOf(err), err.Error())
	}
	if err := r.checkSpace(est); err != nil {
		return fail("disk", err.Error())
	}
	r.logEvent(run.ID, "info", "space", fmt.Sprintf("estimated restore size %d bytes, free space OK", est))

	// Step 5: restore into the sandbox.
	sb, err = sandbox.Create(r.Cfg.WorkDir, run.ID)
	if err != nil {
		return fail("disk", err.Error())
	}
	drill.SubstituteRuntime(sb.RestoreDir, run.ID)
	r.logEvent(run.ID, "info", "restore", "restoring to "+sb.RestoreDir)
	restoreStart := time.Now()
	_, err = be.Restore(ctx, snap, drill.Restore.Paths, sb.RestoreDir, func(p backend.Progress) {
		if p.BytesTotal > 0 {
			r.logEvent(run.ID, "debug", "restore", fmt.Sprintf("progress %d/%d bytes", p.BytesDone, p.BytesTotal))
		}
	})
	if err != nil {
		return fail(kindOf(err), err.Error())
	}
	run.RestoreSeconds = time.Since(restoreStart).Seconds()

	// Step 6: authoritative restore stats by walking the restored tree.
	bytes, files, err := walkStats(sb.RestoreDir)
	if err != nil {
		return fail("internal", err.Error())
	}
	run.RestoredBytes = bytes
	run.RestoredFiles = files
	r.logEvent(run.ID, "info", "restore", fmt.Sprintf("restored %d files, %d bytes in %.1fs", files, bytes, run.RestoreSeconds))

	// Build the container runtime if the drill needs one (M3).
	needsContainers := drill.Sandbox != nil || drillNeedsContainerChecks(drill)
	if needsContainers && r.Runtime != nil {
		runtime, runtimeTeardown, err = r.Runtime(ctx, sb, drill)
		if err != nil {
			return fail("docker", err.Error())
		}
	} else if needsContainers {
		return fail("docker", "this drill needs containers but no container runtime is configured — ensure Docker/Podman is reachable")
	}

	// Steps 7-9: run checks (data phase, then services, then service phase).
	cctx := &checks.Context{
		RestoreDir:    sb.RestoreDir,
		Snapshot:      snap,
		Drill:         run.Drill,
		RunID:         run.ID,
		Store:         r.Store,
		Runtime:       runtime,
		Services:      map[string]bool{},
		RestoredBytes: bytes,
		RestoredFiles: files,
		Now:           time.Now(),
		Log:           func(level, msg string) { r.logEvent(run.ID, level, "check", msg) },
	}
	effective := effectiveChecks(drill)
	var results []store.CheckResult
	seq := 0
	runPhase := func(phase string) {
		for _, ch := range effective {
			if checks.CheckPhaseFor(ch) != phase {
				continue
			}
			res := r.runOneCheck(ctx, cctx, ch, seq)
			results = append(results, res)
			seq++
		}
	}
	runPhase("data")

	// M3: start services between phases. For file-only drills this is a no-op.
	if drill.Sandbox != nil && runtime != nil {
		if err := r.startServices(ctx, cctx, drill, runtime); err != nil {
			// Service start failure is a fail (the restored service is broken).
			res := store.CheckResult{RunID: run.ID, Seq: seq, Name: "service_start", Type: "service_start", Phase: "service", Status: store.StatusFail, StartedAt: time.Now(), Message: err.Error()}
			_ = r.Store.InsertCheckResult(res)
			results = append(results, res)
			seq++
		}
	}
	runPhase("service")

	// Step 11 (aggregation): fail beats error beats pass.
	status = aggregate(results)
	return status, "", "", keptPath
}

// runOneCheck executes a single check, records it, and returns the result.
func (r *Runner) runOneCheck(ctx context.Context, cctx *checks.Context, ch config.Check, seq int) store.CheckResult {
	impl, ok := checks.Get(ch.Type)
	res := store.CheckResult{RunID: cctx.RunID, Seq: seq, Name: ch.Name, Type: ch.Type, Phase: checks.CheckPhaseFor(ch), StartedAt: time.Now()}
	if !ok {
		res.Status = store.StatusError
		res.Message = "unknown check type " + ch.Type
		res.DurationMS = 0
		_ = r.Store.InsertCheckResult(res)
		return res
	}
	start := time.Now()
	out := impl.Run(ctx, cctx, ch)
	res.Status = out.Status
	res.Message = r.Cfg.Redactor.String(out.Message)
	res.DurationMS = time.Since(start).Milliseconds()
	if out.Details != nil {
		if b, err := marshalJSON(out.Details); err == nil {
			res.DetailsJSON = r.Cfg.Redactor.String(string(b))
		}
	}
	_ = r.Store.InsertCheckResult(res)
	r.logEvent(cctx.RunID, levelForStatus(out.Status), "check", fmt.Sprintf("%s: %s — %s", ch.Name, out.Status, res.Message))
	return res
}

// startServices is implemented in M3 (services.go). For M1 it is a stub that
// returns nil when there are no services.
func (r *Runner) startServices(ctx context.Context, cctx *checks.Context, drill config.Drill, rt checks.ContainerRuntime) error {
	return startServicesImpl(ctx, cctx, drill, rt, r)
}

func (r *Runner) backendFor(repo config.Repository) (backend.Backend, error) {
	switch repo.Type {
	case "restic":
		return restic.New(r.Cfg.Binaries.Restic, repo, r.Cfg.Redactor, func(line string) {}), nil
	case "borg":
		return borg.New(r.Cfg.Binaries.Borg, repo, r.Cfg.StateDir, r.Cfg.Redactor, func(line string) {}), nil
	default:
		return nil, fmt.Errorf("repository %q has unknown type %q", repo.Repository, repo.Type)
	}
}

func (r *Runner) checkSpace(estimate int64) error {
	if err := os.MkdirAll(r.Cfg.WorkDir, 0o700); err != nil {
		return fmt.Errorf("cannot create work_dir %q: %w", r.Cfg.WorkDir, err)
	}
	free, err := sandbox.FreeBytes(r.Cfg.WorkDir)
	if err != nil {
		return fmt.Errorf("cannot determine free space in work_dir %q: %w", r.Cfg.WorkDir, err)
	}
	need := uint64(float64(estimate)*1.1) + 1<<30
	if free < need {
		return fmt.Errorf("not enough disk space in work_dir %q: need ~%d bytes (estimate x1.1 + 1 GiB), have %d — free space or move work_dir", r.Cfg.WorkDir, need, free)
	}
	return nil
}

func (r *Runner) logEvent(runID, level, step, msg string) {
	msg = r.Cfg.Redactor.String(msg)
	_ = r.Store.InsertEvent(store.RunEvent{RunID: runID, TS: time.Now(), Level: level, Step: step, Message: msg})
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	r.Log.Log(context.Background(), lv, msg, "run", runID, "step", step)
}

// aggregate applies the run status rules: any fail -> fail; else any error ->
// error; else pass. Skipped checks do not affect the status.
func aggregate(results []store.CheckResult) string {
	hasError := false
	for _, c := range results {
		switch c.Status {
		case store.StatusFail:
			return store.StatusFail
		case store.StatusError:
			hasError = true
		}
	}
	if hasError {
		return store.StatusError
	}
	return store.StatusPass
}

func kindOf(err error) string {
	var be *backend.Error
	if errors.As(err, &be) {
		return be.Kind
	}
	return "other"
}

func levelForStatus(status string) string {
	switch status {
	case store.StatusFail, store.StatusError:
		return "error"
	case store.StatusSkipped:
		return "info"
	default:
		return "info"
	}
}

// effectiveChecks prepends the implicit snapshot_age check from max_age.
func effectiveChecks(drill config.Drill) []config.Check {
	var out []config.Check
	if drill.Snapshot.MaxAge > 0 {
		out = append(out, config.Check{Type: "snapshot_age", Name: "snapshot_age", MaxAge: drill.Snapshot.MaxAge})
	}
	out = append(out, drill.Checks...)
	return out
}

func drillNeedsContainerChecks(drill config.Drill) bool {
	for _, ch := range drill.Checks {
		switch ch.Type {
		case "postgres_restore", "http", "service_healthy":
			return true
		case "command":
			return true
		}
	}
	return false
}

func drillHash(d config.Drill) string {
	b, err := yaml.Marshal(d)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func walkStats(root string) (bytes, files int64, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return nil
		}
		if fi.Mode().IsRegular() {
			files++
			bytes += fi.Size()
		}
		return nil
	})
	return bytes, files, err
}

func marshalJSON(v any) ([]byte, error) {
	return jsonMarshal(v)
}