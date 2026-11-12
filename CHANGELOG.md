# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/), and this project adheres to
[Conventional Commits](https://www.conventionalcommits.org/).

## [Unreleased]

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

- Borg adapter, SQLite/Postgres checks, containers, serve/dashboard, recipes and
  release tooling land in milestones 2–5.