# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Conventional Commits](https://www.conventionalcommits.org/).

## [Unreleased]

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

- Serve/dashboard, recipes and release tooling land in milestones 4–5.
  `postgres_restore` and service drills need a Docker/Podman socket and are
  exercised by the `docker`-tagged container tests (run
  `go test -tags docker ./...` on a host with a reachable socket).