1. Purpose and goals
Restoredrill (working title) is an open-source CLI with a small web dashboard that proves backups can actually be restored. It periodically restores restic and Borg snapshots into a throwaway sandbox and checks that the data and the services built on it work.
Problem. Backup tools verify repository integrity (restic check, borg check). Nobody verifies that a restored service starts, that a Postgres dump loads, or that the backup still contains the files it should. Self-hosters write one-off bash scripts or skip restore tests entirely.
Goals for v1
• Declarative drills in YAML: what to restore, from which repository, and what "working" means.
• Strictly read-only towards repositories. The tool never modifies a backup.
• Three depths of checking: files and checksums, database integrity, full service start with HTTP probes.
• A clear status per drill (pass, fail, error) with history, durations and the age of the tested data.
• One static binary: restoredrill run for cron or systemd timers, restoredrill serve for scheduler plus dashboard.
• Reusable recipes so the community can share drills for common self-hosted apps.
Target users: self-hosters and admins of small organisations. Linux first.
Open question: the final name. Check availability on GitHub, Docker Hub and package registries before the first release.
2. Non-goals for v1
• Not a backup tool: no backup, forget, prune, compact, init or delete.
• No backends beyond restic and Borg 1.x. Design the adapter interface so Kopia and Borg 2 can be added later.
• No Kubernetes and no Windows hosts. macOS is best effort for file-only drills.
• No user accounts or roles in the dashboard.
• No remote recipe registry. Recipes are local files or bundled with the binary.
• No editing of drills in the web UI. Configuration stays in files, so it can live in Git.
3. Tech stack
Go, shipped as one static binary with the dashboard embedded. restic and Borg are called as external binaries, never embedded as libraries.
Area
Choice
Reason
Language
Latest stable Go, CGO_ENABLED=0
Single static binary, same ecosystem as restic, easy cross-compiling
CLI
spf13/cobra
Subcommands, help, shell completion
Config
YAML (goccy/go-yaml or gopkg.in/yaml.v3) plus a generated JSON Schema
Editor validation and generated config reference
Storage
SQLite via modernc.org/sqlite
Pure Go, no CGO, zero setup
Containers
Docker Engine API via the official Go client
Podman works through its Docker-compatible socket
Web
net/http, html/template, htmx, assets via go:embed
No Node build step, works without JS for reading
Scheduler
robfig/cron/v3
Standard 5-field cron with time zones
Metrics
prometheus/client_golang
/metrics endpoint
Logging
log/slog (text or JSON)
Standard library
Release
GoReleaser, cosign, SBOM
linux/amd64 and linux/arm64 binaries, Docker image with restic and Borg
License
Apache-2.0
Owner may change before the first release
Why external binaries: restic has no stable library API and Borg is written in Python. Both offer JSON output, which the adapters parse.
4. Core concepts
Concept
Meaning
Repository
A restic or Borg repository plus how to unlock it. Referenced by name.
Drill
A named restore test: repository, snapshot selector, paths to restore, optional sandbox services, ordered checks, schedule, notification policy.
Recipe
A parameterised drill template, for example service1. A drill uses it with uses: and with:.
Sandbox
A temporary restore directory, plus optionally an isolated container network with service containers. Always torn down.
Check
One assertion. Result: pass, fail, error or skipped, with message, details and duration.
Run
One execution of a drill, with its snapshot, restore stats, check results and logs.
Run status rules. The distinction between fail and error is central and must be visible everywhere (CLI, API, dashboard, notifications).
• pass: every check passed.
• fail: at least one check failed. The backup itself has a problem.
• error: the drill could not be evaluated, for example repository unreachable, Docker down or disk full. The test has a problem, not necessarily the backup.
• skipped: not run, for example because a previous run of the same drill was still active.
If a run has both failed and errored checks, its status is fail.
5. Configuration format
One main file (restoredrill.yaml) plus optional included files. Lookup order: --config, then RESTOREDRILL_CONFIG, then ./restoredrill.yaml, then /etc/restoredrill/restoredrill.yaml.
Path convention. Restored files keep their original absolute path below the restore root: /srv/app/data is restored to ${restore}/srv/app/data. Relative paths in checks are relative to ${restore}. Both adapters must produce this same layout.
Variables. Only plain substitution, no template language: ${restore}, ${run.id}, ${param.<name>} (in recipes), ${env.<NAME>}. Unknown variables are a validation error.
Secrets. Only via *_file, *_command or environment variables. A literal password in the config is a validation error unless allow_inline_secrets: true is set.
Main file
version: 1
state_dir: /var/lib/restoredrill        # SQLite database, kept sandboxes
work_dir: /var/tmp/restoredrill         # restore sandboxes, needs free space
binaries:
  restic: /usr/bin/restic
  borg: /usr/bin/borg
retention:
  runs_per_drill: 200
defaults:
  timeout: 30m
  keep_on_failure: false
  keep_ttl: 24h
  notify: [ntfy-location1]
  resources: { cpus: 2, memory: 2GiB }

repositories:
  location1-restic:
    type: restic
    repository: sftp:backup@location1:/srv/restic/location1
    password_file: /etc/restoredrill/secrets/location1-restic.pass
    env:
      RESTIC_CACHE_DIR: /var/cache/restoredrill/restic
  location2-borg:
    type: borg
    repository: ssh://borg@location2.example.org:22/./location1
    passcommand: cat /etc/restoredrill/secrets/location2-borg.pass
    env:
      BORG_RSH: ssh -i /etc/restoredrill/secrets/id_ed25519

notifiers:
  ntfy-location1:
    type: ntfy
    url: https://ntfy.example.org/backups
    token_file: /etc/restoredrill/secrets/ntfy.token
    on: [fail, error, recovered]
  matrix-ops:
    type: matrix
    homeserver: https://matrix.example.org
    room_id: '!abcdef:example.org'
    token_file: /etc/restoredrill/secrets/matrix.token
    on: [fail, error, recovered]

server:
  listen: 127.0.0.1:8484
  public_url: https://drills.example.org   # used for links in notifications
  auth: { mode: none }                     # none | basic | trusted_header
  timezone: UTC

include:
  - drills.d/*.yaml
Drill: files only
drills:
  documents:
    repository: location1-restic
    schedule: '0 3 * * 0'
    snapshot:
      select: latest          # latest | random | oldest
      within: 30d             # window for random and oldest
      tags: [documents]       # restic filters: tags, host, paths
      host: location1
      max_age: 26h            # adds an implicit snapshot_age check
    restore:
      paths: [/srv/service2/media/documents/originals]
    checks:
      - type: files_exist
        paths: ['srv/service2/media/documents/originals/**/*.pdf']
        min_count: 1000
      - type: size_trend
        max_drop_percent: 10
      - type: checksum_sample
        source_root: /
        paths: ['srv/service2/media/documents/originals/**']
        sample: 50
Drill: service start (Service1)
drills:
  service1:
    repository: location1-restic
    schedule: '30 4 * * *'
    snapshot: { select: latest, tags: [service1], max_age: 26h }
    restore:
      paths: [/srv/service1/data]
    sandbox:
      network: isolated       # internal network, no egress (default)
      services:
        app:
          image: service1/server:latest   # placeholder image — replace and pin in real use
          mounts:
            - source: ${restore}/srv/service1/data
              target: /data
          env:
            APP_MODE: 'restore-test'
          healthcheck:
            http: { port: 80, path: /alive, expect: { status: 200 } }
            timeout: 2m
    checks:
      - type: sqlite_integrity
        path: srv/service1/data/db.sqlite3
      - type: sqlite_query
        path: srv/service1/data/db.sqlite3
        query: SELECT count(*) FROM users
        expect: { min: 1 }
      - type: http
        service: app
        port: 80
        path: /api/config
        expect: { status: 200, json: { path: $.version, exists: true } }
Drill: Postgres dump from Borg
drills:
  service2-db:
    repository: location2-borg
    schedule: '0 5 * * *'
    snapshot: { select: latest, archive_glob: 'location1-*', max_age: 30h }
    restore:
      paths: [/srv/backups/service2/db.dump]
    checks:
      - type: postgres_restore
        dump: srv/backups/service2/db.dump
        format: custom        # custom | plain | directory
        image: postgres:16
        queries:
          - sql: SELECT count(*) FROM documents
            expect: { min: 1000 }
Recipes
A drill that uses a recipe:
drills:
  service1-location2:
    uses: service1         # bundled recipe, or a path to a recipe file
    with:
      repository: location2-borg
      data_path: /srv/service1/data
      image: service1/server:latest   # placeholder image
    schedule: '0 5 * * 1'
A recipe file:
recipe: service1
version: 1
description: Restore the data directory, check SQLite, start the server, probe the API
params:
  repository: { required: true }
  data_path: { required: true }
  image: { default: 'service1/server:latest' }   # placeholder image
drill:
  repository: ${param.repository}
  restore:
    paths: ['${param.data_path}']
  # sandbox and checks as in the Service1 example, using ${param.*}
Fields set on the drill override the recipe's fields (deep merge for maps, replace for lists). restoredrill plan <drill> prints the fully resolved drill.
6. Check types
Each check type lives in its own file and registers itself in a registry. Data checks run on the restored files before any service starts. Service checks run after services are healthy. Within each phase, checks run in the order listed.
Type
Phase
Verifies
Key fields
snapshot_age
data
Tested snapshot is newer than a limit. Added automatically from snapshot.max_age.
max_age
files_exist
data
Glob patterns match at least N files, optionally with a minimum total size
paths, min_count, min_bytes
size_trend
data
Restored bytes and file count did not drop by more than X% versus the last passing run
max_drop_percent
checksum_sample
data
N random restored files are identical (SHA-256) to the live source
source_root, paths, sample, seed
sqlite_integrity
data
PRAGMA integrity_check returns ok
path
sqlite_query
data
A scalar query result meets an expectation
path, query, expect
postgres_restore
data (container)
A dump loads into a temporary Postgres container and queries meet expectations
dump, format, image, queries
command
data or service
A command in a container exits as expected and prints what is expected
image or service, run, expect
service_healthy
service
A service reached its healthy state in time. Added automatically from healthcheck.
service, timeout
http
service
An HTTP request from the probe container meets expectations
service, port, path, method, headers, expect
Expectations. One shared syntax: expect: { status, min, max, equals, contains, regex, exit_code, json: { path, exists, equals } }. Only the keys relevant to a check type are allowed; others are a validation error.
checksum_sample rule. Compare only files whose live copy has the same size and an mtime at or before the snapshot time. Other files were changed after the backup and are counted as skipped, not failed. If fewer than half of the sampled files qualify, the check is error with a clear message. source_root must be mounted read-only if the tool runs in a container.
Later (not v1): mysql_restore, tcp, file_contains, custom check plugins via executables.
7. Run lifecycle
Every run follows the same steps. Teardown (step 10) runs whatever happens before it: failure, timeout, SIGINT, SIGTERM or panic.
1. Lock. Take a per-drill lock and respect the global concurrency limit (default 1 run at a time). If the drill is already running, record the run as skipped.
2. Preflight. Check binary versions, that the repository is reachable (list snapshots) and, if needed, that Docker is reachable. Pull all images now, before restoring anything.
3. Select snapshot. Apply the selector. Record snapshot ID, time, host and tags. random uses a seed stored with the run, so a run can be reproduced.
4. Space check. Estimate the restore size for the selected paths. Require estimate × 1.1 plus 1 GiB free in work_dir, otherwise error before restoring.
5. Restore. Create work_dir/<run-id>/ with mode 0700 and restore into restore/ inside it. Stream progress to the run log.
6. Record restore stats. Bytes, file count and duration.
7. Data checks in listed order.
8. Start services. Create the isolated network, start service containers with resource limits and labels, wait for health checks.
9. Service checks through the probe container.
10. Teardown. Remove containers, network and volumes. Delete the sandbox, unless keep_on_failure is set and the run did not pass. A kept sandbox is deleted after keep_ttl.
11. Persist and notify. Store run, check results and logs, update metrics, send notifications according to the policy.
Crash recovery. All containers, networks and sandbox directories carry the run ID (label restoredrill.run=<id>). On startup, run and serve remove leftovers from runs that are no longer active. restoredrill gc does the same on demand.
Timeouts. A global drill timeout (default 30 min) plus per-step timeouts. On timeout the run is error and teardown runs.
8. Safety and security requirements
A restore test handles a full copy of production data and touches the last line of defence. When in doubt, the safer behaviour wins: read-only, no network, fail closed.
Repositories stay untouched
• Adapters may only build these commands: restic version, snapshots, ls, stats, restore; Borg --version, list, info, extract. A unit test asserts this allowlist for every code path.
• restic restore and Borg extract take non-exclusive locks. That is acceptable. An optional no_lock: true per repository (restic only) supports read-only storage, with the risk documented.
• Documentation must warn not to schedule drills at the same time as prune or compact.
Secrets
• Passwords reach child processes only through the environment (RESTIC_PASSWORD_FILE, RESTIC_PASSWORD_COMMAND, BORG_PASSCOMMAND), never through argv.
• All log lines, stored events, API responses and HTML pass through a redactor that knows every configured secret value.
• validate warns if a secret file is readable by group or others.
Restored data
• The sandbox directory is 0700 and owned by the runner user. It is deleted on teardown. Kept sandboxes expire after keep_ttl.
• Documentation recommends a work_dir on an encrypted local disk.
• Ownership is only preserved when running as root. Document both modes (root, rootless Podman with user: mapping).
Containers
• Default network: a Docker network with internal: true. No egress, no published ports. Restored services cannot send mail, call webhooks or sync anywhere.
• HTTP checks run from a probe container on the same network. It uses the tool's own image with a hidden restoredrill probe subcommand, so no curl image is needed.
• Defaults: CPU and memory limits, no-new-privileges, all capabilities dropped unless the service config adds them.
• Access to the Docker socket is equivalent to root. Documentation says so clearly and describes rootless Podman.
• validate warns on images without a pinned tag or digest.
Dashboard
• Binds to 127.0.0.1 by default. Binding to any other address without an auth mode is refused at startup.
• Auth modes: basic (bcrypt hashes in the config) and trusted_header (behind a reverse proxy, with required trusted proxy CIDRs).
• CSRF tokens on all POST requests, rate limit on "Run now", strict Content Security Policy with no inline scripts.
Supply chain
• Few dependencies. govulncheck and go mod verify run in CI. Releases are signed with cosign and ship an SBOM.
9. CLI
Every command that prints results supports --json with a versioned, stable schema ("schema": 1). On a TTY, run shows live progress and ends with a summary table.
Command
Purpose
restoredrill init [--dir DIR]
Write an example config and one sample drill
restoredrill validate [--online]
Check schema and semantics, secret file permissions, binary versions. --online also contacts repositories and Docker.
restoredrill snapshots <repository>
List snapshots through the adapter, to debug selectors
restoredrill plan <drill>
Dry run: resolved drill, chosen snapshot, estimated size, checks. Restores nothing.
restoredrill run [<drill>... | --all]
Run now. Flags: --snapshot ID, --keep, --no-notify, --json
restoredrill status
Last result per drill
restoredrill history <drill> [--limit N]
Past runs of one drill
restoredrill show <run-id> [--logs]
Details and logs of one run
restoredrill serve
Scheduler, dashboard, API and metrics
restoredrill gc
Remove leftover sandboxes, containers and networks
restoredrill recipe list | show <name>
Bundled and local recipes
restoredrill version
Version, commit, detected restic and Borg versions
Global flags: --config, --log-level, --log-format text|json.
Exit codes (for cron, systemd and CI)
Code
Meaning
0
All runs passed
1
At least one run failed
2
Config or usage error
3
At least one run errored, none failed
4
Internal error
examples/ ships a systemd service and timer for running restoredrill run --all without serve.
Error messages say what to do next, for example: restic not found — install restic or set binaries.restic in restoredrill.yaml.
10. Dashboard, API, scheduler, metrics, notifications
restoredrill serve runs all of these in one process. The dashboard is read-mostly; the only action is "Run now".
Dashboard pages
Server-rendered HTML with htmx. Reading works without JavaScript. While a run is active, the page updates through server-sent events.
Page
Content
Overview
One row per drill: status badge, last run, last success, age of the tested snapshot, duration, last 30 runs as small pass/fail squares, next scheduled run, "Run now"
Drill
Resolved config with secrets redacted, run history, trend of restored size and duration
Run
Snapshot ID, time and host, step timeline with durations, every check with message and details, log tail with download, kept sandbox path if any
Live run
Progress stream of the current step
Design rules: status is shown with text and icon, not colour alone. Dark mode via prefers-color-scheme. One hand-written stylesheet, no CSS framework. The overview must stay fast with 50 drills and 200 runs each on a Raspberry Pi 4 class machine (target: under 1 s).
JSON API (/api/v1)
Method
Path
Purpose
GET
/drills
All drills with last status
GET
/drills/{name}
Resolved drill (redacted)
GET
/drills/{name}/runs
Run history, paginated
GET
/runs/{id}
Run with check results
GET
/runs/{id}/logs
Run log
POST
/drills/{name}/run
Trigger a run (CSRF token or API token)
GET
/healthz, /readyz
Liveness and readiness
GET
/metrics
Prometheus metrics
Scheduler
• 5-field cron per drill, in server.timezone. Optional jitter per drill.
• catch_up: true (default): after downtime, run once at startup if the last run is older than one schedule interval.
• Runs queue behind the global concurrency limit. Manual and scheduled runs share the queue.
Metrics
Metric
Labels
restoredrill_drill_last_run_timestamp_seconds
drill
restoredrill_drill_last_success_timestamp_seconds
drill
restoredrill_drill_status (1 for the current status)
drill, status
restoredrill_run_duration_seconds
drill
restoredrill_snapshot_age_seconds
drill
restoredrill_restored_bytes
drill
restoredrill_check_status
drill, check, status
Notifications
• v1 notifiers: ntfy, Matrix (client-server API, plain and HTML body) and a generic webhook (documented JSON payload, optional HMAC-SHA256 signature header). Email via SMTP comes later.
• Events: fail, error, recovered (first pass after a non-pass) and optionally pass.
• No repeat notification for the same ongoing failure within renotify_after (default 24h).
• Content: drill, status, failed checks with one-line messages, snapshot age, link to the run via server.public_url. Never secrets or file contents.
11. Backend adapters (restic and Borg)
Both adapters implement one interface and must produce the same restore layout (section 5). A shared test suite runs against both.
type Backend interface {
    Kind() string // "restic" | "borg"
    Version(ctx context.Context) (string, error)
    ListSnapshots(ctx context.Context, f SnapshotFilter) ([]Snapshot, error)
    EstimateRestoreSize(ctx context.Context, s Snapshot, paths []string) (int64, error)
    Restore(ctx context.Context, s Snapshot, paths []string, target string,
        progress func(Progress)) (RestoreStats, error)
}

type Snapshot struct {
    ID    string    // restic snapshot ID or Borg archive name
    Time  time.Time
    Host  string
    Tags  []string
    Paths []string
}
Operation
restic
Borg 1.x
List snapshots
restic snapshots --json with --tag, --host, --path
borg list --json REPO, filtered by archive_glob
Estimate size
Sum of file sizes from restic ls --json ID PATH...
Sum of sizes from borg list --json-lines REPO::ARCHIVE PATH...
Restore
restic restore ID --target DIR --include PATH... --json
borg extract --log-json --progress REPO::ARCHIVE PATH... with working directory DIR
Unlock
RESTIC_PASSWORD_FILE or RESTIC_PASSWORD_COMMAND
BORG_PASSCOMMAND
Extra env
RESTIC_REPOSITORY, RESTIC_CACHE_DIR, repo env
BORG_RSH, BORG_BASE_DIR under state_dir, BORG_RELOCATED_REPO_ACCESS_IS_OK=no, repo env
Rules:
• Verify every flag against the official documentation of the minimum supported version, then pin that version in CI. Define the minimum restic version by the first release with JSON output for restore.
• Support Borg 1.2 and 1.4. Detect Borg 2.x and refuse with a clear message.
• Do not use restic's ID:subfolder restore syntax, because it changes the layout.
• Borg stores paths without a leading slash. Strip the leading slash from configured paths when calling Borg.
• Run child processes in their own process group. Cancelling the context kills the whole group.
• Capture stderr into the run log after redaction. Parse JSON tolerantly: ignore unknown fields, fail on missing required ones.
• Every adapter call has a timeout.
12. Data model and project layout
SQLite in WAL mode with embedded, forward-only migrations. Run IDs are ULIDs. A retention job keeps retention.runs_per_drill runs per drill.
Table
Columns
runs
id, drill, trigger (schedule, manual, cli), status, started_at, finished_at, repository, backend, snapshot_id, snapshot_time, snapshot_host, seed, restored_bytes, restored_files, restore_seconds, error_kind, error_message, kept_path, config_hash
check_results
run_id, seq, name, type, phase, status, started_at, duration_ms, message, details_json
run_events
run_id, ts, level, step, message (redacted)
notifications
run_id, notifier, event, sent_at, ok, error
schema_migrations
version, applied_at
cmd/restoredrill/      main package
internal/config/       parsing, includes, recipes, substitution, validation, JSON Schema
internal/backend/      interface, restic/, borg/, shared test suite
internal/sandbox/      work dirs, container runtime, isolated networks, probe, GC
internal/checks/       one file per check type plus registry and expectations
internal/runner/       lifecycle, locks, timeouts, teardown, status aggregation
internal/store/        SQLite, migrations, queries, retention
internal/scheduler/    cron, catch-up, queue
internal/notify/       ntfy, matrix, webhook
internal/web/          handlers, templates, static assets (embedded), API
internal/metrics/      Prometheus collectors
internal/redact/       secret redaction used by every output path
recipes/               bundled recipes (embedded)
examples/              configs, systemd units, docker-compose demo
docs/                  user docs, decisions.md
testdata/              fixture trees and broken backups
13. Testing strategy
The tool's whole value is catching broken backups, so every way a backup can break needs a test that proves the tool reports it.
Failure-injection tests. Each row is a required test. None of them may ever produce pass.
Broken situation
Expected status
restic pack file deleted or corrupted
fail or error, never pass
Wrong repository password
error
Repository unreachable
error
Snapshot older than max_age
fail
Expected files missing, or fewer than min_count
fail
Restored size dropped by more than max_drop_percent
fail
Restored file differs from unchanged live file
fail
SQLite file truncated
fail
Postgres dump truncated
fail
Service container exits on start
fail
Health check times out
fail
HTTP probe returns the wrong status or body
fail
Docker unavailable for a drill that needs it
error
Not enough disk space
error, before any restore starts
Test levels
• Unit: config parsing and validation with golden error messages, substitution, recipe merging, expectation matching, status aggregation, adapter argv allowlist, redaction.
• Backend integration (build tag integration): CI installs pinned restic and Borg versions, creates temporary repositories, backs up fixture trees and restores them. Covers tag and host filters, random with fixed seed, size estimates within 5%, password via file and via command.
• Container (build tag docker): small images such as a BusyBox HTTP server and postgres:16-alpine.
• Teardown: kill during restore, timeout, SIGINT and SIGTERM. Afterwards no directories, containers or networks with the run's label may remain.
• Secret canary: a known fake secret is configured. Tests assert it never appears in logs, database, API responses or HTML.
• Web: handler tests with httptest, golden HTML for main pages, auth, CSRF, and refusal to bind to a non-loopback address without auth.
• CLI: golden tests for --json output and exit codes.
• End to end: a docker-compose demo in examples/ creates a restic repository and a sample service and runs drills. CI runs it as a smoke test.
CI: go vet, staticcheck or golangci-lint, govulncheck, unit tests with -race, integration and container tests on Linux.
14. Milestones and acceptance criteria
Five milestones, each ending in a working, tested and documented state.
#
Scope
Done when
M1
Config and validation, restic adapter, snapshot_age, files_exist, size_trend, checksum_sample, commands init, validate, snapshots, plan, run, status, history, show, SQLite store, JSON output, exit codes
A drill against a real temporary restic repository reports correct results, and all failure-injection tests for these checks pass
M2
Borg adapter, sqlite_integrity, sqlite_query, postgres_restore
The shared backend test suite passes for restic and Borg with identical results
M3
Isolated networks, services, health checks, probe container, http, command, gc, teardown guarantees
The Service1 example passes on a real backup, broken variants fail, and kill tests leave nothing behind
M4
serve: scheduler, dashboard, API, metrics, notifiers, auth modes
A failing drill notifies once, its recovery notifies once, history shows in the dashboard, Prometheus can scrape the metrics
M5
Recipes (uses, with), bundled recipes, docs, GoReleaser, Docker image with restic and Borg, demo compose
A new user gets from install to a first passing drill in under 15 minutes by following the README
Bundled recipes for M5: generic-files, sqlite-app, postgres-dump, service1, service2 (database dump plus original documents), service3 (config directory plus SQLite database).
Acceptance criteria for v1.0
[ ] No code path can issue a write command to a repository, enforced by a test.
[ ] No failure-injection test ever yields pass.
[ ] fail and error are distinguishable in CLI, API, dashboard and notifications.
[ ] Sandboxes, containers and networks are removed after every run, including timeouts and SIGTERM.
[ ] Restored services have no network egress.
[ ] The secret canary never appears in logs, database, API responses or HTML.
[ ] Static binaries for linux/amd64 and linux/arm64, plus a Docker image, signed and with SBOM.
[ ] Dashboard overview loads in under 1 s with 50 drills and 200 runs each on a Raspberry Pi 4 class machine.
[ ] Docs: README quickstart, config reference generated from the JSON Schema, recipe authoring guide, security notes.
15. Working rules for the coding agent
1. Treat this spec as the source of truth. If something is unclear, choose the safer option (read-only, no network, fail closed) and record the decision in docs/decisions.md.
2. Work one milestone at a time. Finish each with passing tests, updated docs and a CHANGELOG.md entry before starting the next.
3. Ask before changing the tech stack, adding a dependency not listed in section 3, or changing the config format after M1.
4. Prefer the standard library. Justify every new dependency in the commit message.
5. Never run tests against real repositories. Tests create temporary repositories only.
6. Verify restic and Borg flags against their official docs before using them. Do not rely on memory.
7. Keep commits small, with Conventional Commit messages.
8. Write user-facing errors that state the cause and the next step.
9. Keep README.md and the config reference current with every user-visible change.
