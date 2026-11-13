# Restoredrill

**Prove your backups can actually be restored.**

Restoredrill is an open-source CLI with a small web dashboard that periodically
restores [restic](https://restic.net) and [Borg](https://www.borgbackup.org)
snapshots into a throwaway sandbox and checks that the data — and the services
built on it — actually work.

Backup tools verify repository integrity (`restic check`, `borg check`).
Restoredrill verifies the thing that matters: that a restored service
starts, that a Postgres dump loads, that the backup still contains the files it
should.

> **Working title.** The final name is not decided yet; availability on GitHub,
> Docker Hub and package registries will be checked before the first release.

## Safety first

Restoredrill is **strictly read-only towards your repositories**. It never
backs up, prunes, compacts, inits or deletes. Every backend command is checked
against a read-only allowlist, and a test asserts this for every code path.
Restored services run on an **isolated network with no egress**. Secrets reach
child processes only through the environment and are redacted from every log,
API response and page.

See [docs/security.md](docs/security.md) for the full security model.

## Install

Requires `restic` (>= 0.17) and/or `borg` (1.2 / 1.4) on the host that runs
drills. Container-based checks additionally need Docker or a Docker-compatible
Podman socket.

- **Release binaries** (from the first tagged release): linux/amd64 and
  linux/arm64 tarballs on GitHub Releases, with SHA-256 checksums, cosign
  signatures and an SBOM per archive.
- **Docker image**: `ghcr.io/restoredrill/restoredrill` — Alpine with restic
  and Borg bundled, binary at `/usr/local/bin/restoredrill`. This is also the
  probe image used by HTTP checks.
- **From source** (Go 1.24+):

```sh
go build -o restoredrill ./cmd/restoredrill
```

A single static binary ships the dashboard and all bundled recipes; no Node
build step is required.

## Quickstart A: demo with Docker (~10 minutes, no backup needed)

The fastest way to see a passing drill: a compose stack seeds a throwaway
restic repository, restores it and checks the result.

```sh
cd examples/demo
docker compose up --build --abort-on-container-exit --exit-code-from drill
# exit code 0 = the drill passed

docker compose up --build -d dashboard
# open http://127.0.0.1:8484 — admin / change-me (demo credentials)
```

See [examples/demo/README.md](examples/demo/README.md) for the walkthrough,
including how to inject a failure and watch the drill catch it.

## Quickstart B: your own backup (file-only drill, ~5 minutes)

```sh
# 1. Write an example config and a sample drill.
restoredrill init

# 2. Edit restoredrill.yaml: point the repository at a real restic/Borg repo
#    and set password_file / passcommand. Edit drills.d/example.yaml: set the
#    paths to restore and what "working" means.

# 3. Check the config.
restoredrill validate

# 4. Debug snapshot selection.
restoredrill snapshots my-restic

# 5. Dry run: resolved drill, chosen snapshot, estimated size, checks.
restoredrill plan example-files

# 6. Run it.
restoredrill run example-files

# 7. Inspect results.
restoredrill status
restoredrill history example-files
restoredrill show <run-id> --logs
```

Exit codes are stable for cron / systemd / CI:

| Code | Meaning |
|------|---------|
| 0 | All runs passed |
| 1 | At least one run failed |
| 2 | Config or usage error |
| 3 | At least one run errored, none failed |
| 4 | Internal error |

## The three depths of checking

1. **Files & checksums** — `files_exist`, `size_trend`, `checksum_sample`.
2. **Database integrity** — `sqlite_integrity`, `sqlite_query`, `postgres_restore`.
3. **Full service start** — sandbox containers, `service_healthy`, `http`, `command`.

Every drill reports a clear status:

- **pass** — every check passed.
- **fail** — at least one check failed; the *backup* has a problem.
- **error** — the drill could not be evaluated (repo unreachable, Docker down,
  disk full); the *test* has a problem, not necessarily the backup.
- **skipped** — not run, e.g. a previous run was still active.

If a run has both failed and errored checks, its status is **fail**.

## Service drills (containers)

A drill can start the restored data as real services and probe them. Add a
`sandbox` block with `services`, then use `http` / `command` checks:

```yaml
drills:
  service1:
    repository: location1-restic
    snapshot: { select: latest, tags: [service1], max_age: 26h }
    restore:
      paths: [/srv/service1/data]
    sandbox:
      services:
        app:
          image: service1/server:latest   # placeholder image — replace and pin in real use
          mounts:
            - { source: "${restore}/srv/service1/data", target: /data }
          env: { APP_MODE: "restore-test" }
          healthcheck:
            http: { port: 80, path: /alive, expect: { status: 200 } }
            timeout: 2m
    checks:
      - { type: sqlite_integrity, path: srv/service1/data/db.sqlite3 }
      - { type: sqlite_query, path: srv/service1/data/db.sqlite3,
          query: "SELECT count(*) FROM users", expect: { min: 1 } }
      - { type: http, service: app, port: 80, path: /api/config,
          expect: { status: 200, json: { path: $.version, exists: true } } }
```

How it stays safe and deterministic:

- Each run gets an **isolated network with no egress**; services cannot reach
  the internet. Containers run with `no-new-privileges`, all capabilities
  dropped, and optional CPU/memory limits (`defaults.resources`).
- A service's `healthcheck` becomes an implicit `service_healthy` check that
  polls the HTTP probe until it succeeds or times out.
- HTTP checks run from a **probe container** using restoredrill's own image
  (hidden `probe` subcommand) — no curl image needed. Set
  `RESTOREDRILL_PROBE_IMAGE` to use a locally built image.
- Containers, the network and the sandbox are **always torn down**, even on
  failure, timeout or Ctrl-C. `--keep` / `keep_on_failure` retain only the
  sandbox directory for debugging; it expires after `keep_ttl`.
- `restoredrill gc` (and automatic startup sweep in `run`/`serve`) removes
  leftovers from crashed runs.

See [examples/service-drill.yaml](examples/service-drill.yaml) for a runnable
template.

## Dashboard, API & scheduler (`serve`)

`restoredrill serve` runs everything in one process: the cron scheduler, the web
dashboard, a JSON API, Prometheus metrics and notifications.

```sh
restoredrill serve                 # binds 127.0.0.1:8484 by default
restoredrill serve --concurrency 2 # allow two drills to run at once
```

Open `http://127.0.0.1:8484`. The dashboard is server-rendered and embedded in
the binary — reading it works without JavaScript. The overview lists every drill
with its status, last run, last success, snapshot age, duration and the last 30
runs as squares; drill and run pages show trends, check results and the full
log. A running drill streams its log live (server-sent events), and **Run now**
triggers a manual run.

### Authentication

Binding to anything other than loopback **requires** an auth mode; `serve`
refuses to start otherwise (fail closed). Configure under `server.auth`:

- `mode: none` — only allowed on `127.0.0.1` / `localhost` / `::1`.
- `mode: basic` — HTTP Basic against bcrypt `password_hash` values under
  `server.auth.users`.
- `mode: trusted_header` — trust an identity header (default
  `X-Forwarded-User`) **only** from `trusted_proxies` CIDRs; anything else is
  rejected. Put it behind your reverse proxy.

```yaml
server:
  listen: 127.0.0.1:8484
  timezone: UTC
  auth:
    mode: basic
    users:
      admin: { password_hash: "$2a$10$..." }   # bcrypt hash
    api_token_file: /run/secrets/restoredrill-api-token
```

`/healthz` and `/readyz` are always open. `/metrics` requires auth whenever an
auth mode is configured.

### JSON API

All endpoints live under `/api/v1` and return a versioned envelope
(`{"schema":1,"data":...}`); errors use `{"schema":1,"error":"..."}`. Secrets are
redacted from every response.

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/drills` | All drills with last status / next run |
| GET | `/api/v1/drills/{name}` | Resolved drill config (redacted) |
| GET | `/api/v1/drills/{name}/runs` | Paginated run history |
| POST | `/api/v1/drills/{name}/run` | Trigger a run (async, returns `202`) |
| GET | `/api/v1/runs/{id}` | One run with its check results |
| GET | `/api/v1/runs/{id}/logs` | Redacted run log |

Authenticate API calls with `Authorization: Bearer <token>` (the token is read
from `server.auth.api_token_file`). The token is accepted on `/api/v1/*` only.
Browser "Run now" uses a session-bound CSRF token instead; manual triggers are
rate-limited (per drill and per client) and return `429` with `Retry-After` when
you go too fast. A triggered run executes asynchronously — poll the run history
for the result.

### Scheduler

Each drill may set a 5-field cron `schedule` (evaluated in `server.timezone`),
an optional `jitter`, and `catch_up` (default true: after downtime, run once at
startup if the last run is older than one interval). Manual and scheduled runs
share the same concurrency queue.

```yaml
drills:
  nightly:
    repository: location1-restic
    schedule: "30 4 * * *"   # 04:30 in server.timezone
    jitter: 10m
    catch_up: true
```

### Metrics & notifications

`/metrics` exposes Prometheus gauges per drill: last run / last success
timestamps, current status, run duration, snapshot age, restored bytes, and a
per-check status series. Metrics are seeded from history on startup, so a scrape
is correct immediately after a restart.

Drills can notify on failure and recovery via `notify:` (ntfy, Matrix or a
generic webhook with optional HMAC-SHA256 signature). Ongoing failures are
de-duplicated (`renotify_after`, default 24h); a recovery always notifies. See
the [config reference](docs/config-reference.md) for notifier fields.

> **Do not schedule drills alongside backup `prune`/`compact` jobs.**

## Recipes

Recipes are parameterised drill templates for common self-hosted apps. Use one
with `uses:` + `with:` instead of writing a drill from scratch:

```yaml
drills:
  documents:
    uses: generic-files
    with:
      repository: location1-restic
      data_path: /srv/documents
      min_count: 100
```

Bundled recipes: `generic-files`, `sqlite-app`, `postgres-dump`, `service1`,
`service2`, `service3`. Drill-level fields override the recipe
(maps deep-merge, lists replace).

```sh
restoredrill recipe list               # bundled + local recipes
restoredrill recipe show service1   # parameters and drill template
```

Local recipes live in a `recipes/` directory next to your config or are
referenced by path. See the [recipe authoring guide](docs/recipes.md) for the
format, parameter rules and path conventions.

## Commands

```
restoredrill init [--dir DIR]          Write an example config + sample drill
restoredrill validate [--online]       Check schema/semantics, secrets, binaries
restoredrill snapshots <repository>    List snapshots through the adapter
restoredrill plan <drill>              Dry run (restores nothing)
restoredrill run [<drill>... | --all]  Run now (--snapshot, --keep, --no-notify, --json)
restoredrill status                    Last result per drill
restoredrill history <drill> [--limit] Past runs of one drill
restoredrill show <run-id> [--logs]    Details and logs of one run
restoredrill serve [--concurrency N]   Scheduler + dashboard + API + metrics
restoredrill gc                        Remove leftover sandboxes/containers
restoredrill recipe list | show <name> Bundled and local recipes
restoredrill version                   Version + detected restic/Borg versions
```

Global flags: `--config`, `--log-level`, `--log-format text|json`. Every command
that prints results supports `--json` with a stable, versioned schema
(`"schema": 1`).

## Configuration

Configuration is declarative YAML and lives in files (never edited in the web
UI), so it can live in Git. See the [config reference](docs/config-reference.md)
and the [recipe authoring guide](docs/recipes.md).

## Development

```sh
go test ./...                          # unit tests
go test -tags integration ./...        # + real temporary restic/Borg repos
go test -tags docker ./...             # + container checks (needs Docker)
go run ./cmd/gendocs                   # regenerate schema + config reference
```

Integration tests **only ever create temporary repositories**; they never touch
real ones. Container tests need a Docker/Podman socket and are skipped
otherwise. CI (`.github/workflows/ci.yml`) runs vet, staticcheck, govulncheck,
`-race` unit tests, integration and container tests, and checks that the
generated docs are current.

Releases are cut by pushing a `v*` tag: GoReleaser builds linux/amd64 + arm64
binaries, signs the checksums and image manifests with cosign (keyless),
attaches SBOMs and publishes the multi-arch image to GHCR. Dry run locally with
`goreleaser release --snapshot --clean` (no publish, no signing).

## License

Apache-2.0. See [LICENSE](LICENSE).