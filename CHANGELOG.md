# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Conventional Commits](https://www.conventionalcommits.org/).

## [Unreleased]

### Added — Milestone 4 (serve: scheduler, dashboard, API, metrics, notifications)

- `restoredrill serve`: one process running the cron scheduler, web dashboard,
  JSON API, Prometheus metrics and notifications. `--concurrency` (default 1)
  bounds simultaneous runs; manual and scheduled runs share the queue.
- Server-rendered, embedded dashboard (no JS required to read): overview with
  per-drill status / last run / last success / snapshot age / duration / last-30
  run squares, drill pages with trends and resolved (redacted) config, run pages
  with steps, checks and full log. Live log streaming and overview refresh via
  server-sent events; **Run now** manual trigger.
- Scheduler: 5-field cron per drill in `server.timezone`, optional `jitter`,
  `catch_up` (default true) runs once at startup when the last run is older than
  one interval.
- Auth modes: `none` (loopback only), `basic` (bcrypt), `trusted_header`
  (identity header trusted only from `trusted_proxies`). Non-loopback binds
  without auth are refused at startup. Session-bound CSRF tokens, strict CSP,
  and rate-limited run triggers (`429` + `Retry-After`).
- JSON API under `/api/v1` with the stable `"schema": 1` envelope; Bearer API
  token (`server.auth.api_token_file`) accepted on the API only. Run triggers
  are async (`202 queued`). `/healthz`, `/readyz` always open; `/metrics`
  requires auth when configured.
- Prometheus metrics: last run / last success timestamps, drill status, run
  duration, snapshot age, restored bytes and per-check status; seeded from
  history on startup.
- Notifications: ntfy, Matrix (plain + HTML) and generic webhook (optional
  HMAC-SHA256 signature). Ongoing failures de-duplicated via `renotify_after`
  (default 24h); recovery always notifies. Secrets never leave in a message.
- Web/API/SSE/scheduler/metrics/notify tests, golden HTML pages, a secret-canary
  test across every output path, and a 50-drill × 200-run overview performance
  test (<1 s).

### Fixed

- Backend command runner now drains stdout before `cmd.Wait()`, fixing a race
  where `Wait` closed the `StdoutPipe` before the reader finished and lost
  output under load (empty/truncated JSON).
- `server.listen: ":8484"` (all interfaces) is no longer treated as loopback, so
  it correctly requires an auth mode.

### Added — Milestone 3 (containers, services, probes, GC)

- Container runtime over a minimal, standard-library Docker Engine API client
  (Docker or rootless Podman socket). Each run gets an isolated **no-egress**
  network; service containers are hardened (`no-new-privileges`, `CapDrop: ALL`,
  CPU/memory limits) and labelled for recovery.
- Service startup from restored data, with `${restore}` mounts, env, per-service
  resources and user. HTTP healthchecks become an implicit `service_healthy`
  check that polls the probe until healthy or timed out.
- `http` and `command` checks, plus a **probe container** running the tool's own
  image via a hidden `restoredrill probe` subcommand (no curl image needed). The
  probe is created lazily, only when an HTTP probe is required.
- Guaranteed teardown of containers, network and sandbox on failure, timeout,
  cancellation and panic. `restoredrill gc` and an automatic startup sweep in
  `run` remove leftovers from crashed runs; kept sandboxes expire after
  `keep_ttl`. GC only touches directories named after a run ULID.
- Container integration tests behind the `docker` build tag (full service drill,
  `postgres_restore`, egress blocking, teardown and GC), skipped when no runtime
  is reachable. Service-drill example and README/decisions documentation.

### Added — Milestone 2 (Borg, SQLite and Postgres checks)

- Borg 1.2/1.4 adapter (refuses 2.x) sharing the same interface, restore layout
  and read-only allowlist as restic. The shared backend test suite passes with
  identical results for restic and Borg.
- `sqlite_integrity` (PRAGMA integrity_check) and `sqlite_query` (scalar query
  with min/max/equals expectations), both opening the restored database
  strictly read-only.
- `postgres_restore`: loads a custom/plain/directory dump into a temporary
  isolated Postgres container and checks expectation queries. Requires the
  container runtime (M3); errors cleanly when it is unavailable.
- Failure-injection tests: truncated SQLite file fails; Borg + SQLite drill
  passes end-to-end and a too-high query expectation fails.

### Added — Milestone 1 (config, restic, file checks, CLI, store)

- Declarative YAML config with includes, recipe expansion, variable
  substitution (`${restore}`, `${run.id}`, `${env.*}`, `${param.*}`), strict
  per-check validation and a generated JSON Schema.
- Secret handling via `*_file` / `*_command` / environment only; inline secrets
  rejected by default; a redactor that knows every configured secret value.
- Read-only restic adapter (>= 0.17) with an enforced command allowlist,
  process-group isolation and classified errors (auth / unreachable / locked).
- Data checks: `snapshot_age`, `files_exist`, `size_trend`, `checksum_sample`
  (with the changed-after-backup skip rule and the <half-qualify error rule).
- Run lifecycle: per-drill lock + global concurrency, preflight, snapshot
  selection (latest/random/oldest with reproducible seed), space check,
  restore, stats, ordered checks, guaranteed teardown, keep_on_failure.
- SQLite store (pure Go, WAL, embedded forward-only migrations, ULID run IDs,
  retention).
- CLI: `init`, `validate`, `snapshots`, `plan`, `run`, `status`, `history`,
  `show`, `version`; stable `--json` schema (`"schema": 1`) and exit codes
  (0 pass / 1 fail / 2 usage / 3 error / 4 internal).
- Failure-injection integration tests against real temporary restic repositories.
- README quickstart, security notes, design decisions, systemd examples.

### Notes

- Recipes and release tooling land in milestone 5. `postgres_restore` and
  service drills need a Docker/Podman socket and are exercised by the
  `docker`-tagged container tests (run `go test -tags docker ./...` on a host
  with a reachable socket).