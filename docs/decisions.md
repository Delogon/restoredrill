# Design decisions

This file records decisions made where the plan was ambiguous, plus the
rationale for each. The guiding rule: when in doubt, choose the safer option
(read-only, no network, fail closed).

## Naming and module path

- Working title **restoredrill**; Go module `github.com/restoredrill/restoredrill`.
  The final name will be checked for availability before the first release.

## Dependencies

- Only dependencies listed in plan §3 are used, plus `golang.org/x/crypto/bcrypt`,
  which is required by plan §8 (basic-auth bcrypt hashes) and is the canonical
  Go implementation. Justified here per working rule #4.
- **ULIDs are implemented in `internal/ulid` with the standard library** rather
  than adding `oklog/ulid`. The encoder is cross-checked against an independent
  `math/big` reference in a test. This keeps the dependency list minimal.
- The Docker Engine API is accessed through a small client over the
  Docker-compatible socket (see M3) rather than pulling the full official
  client's large transitive tree; the wire protocol is plain HTTP.

## Config format

- A `Check` is a single flat struct carrying every check type's fields. Custom
  YAML unmarshalling records which keys were present so validation can reject
  fields that do not belong to the check type (plan §6: "others are a validation
  error"). This keeps merging, serialization and the JSON Schema simple.
- Recipe expansion and variable substitution operate on the YAML node tree
  before strict decoding: maps deep-merge, lists replace, drill fields override
  recipe fields. `${restore}` and `${run.id}` are resolved at run time;
  `${env.*}` and `${param.*}` at load time. Unknown variables are errors.
- Default `work_dir` is `/var/tmp/restoredrill` (conventional large-temp
  location, usually on disk rather than a small tmpfs). Default `state_dir` is
  `~/.local/state/restoredrill`.

## Secrets and redaction

- `*_command` secret helpers (e.g. `password_command`, `passcommand`) are
  executed locally at config load so their output can be registered with the
  redactor. They are local helper commands such as `cat /path/to/pass`. If a
  helper fails, a warning is recorded (the value cannot be redacted) and the
  adapter surfaces the real error later.
- The redactor also learns secret-looking environment values
  (`*PASSWORD*`, `*PASSPHRASE*`, `*TOKEN*`, `*SECRET*`) and the process's own
  `RESTIC_PASSWORD` / `BORG_PASSPHRASE`.

## Backend adapters

- **Minimum restic version is 0.17.0**, the first release with JSON output for
  `restore`. Flags and JSON shapes were verified against the installed binaries
  (restic 0.19.1, borg 1.4.0) before use, per working rule #6. CI pins these.
- **Snapshot filtering is done client-side** after `restic snapshots --json` /
  `borg list --json`, for deterministic semantics across versions (tags = AND,
  host = exact, paths = prefix either direction, archive_glob = `path.Match`).
- **Borg timestamps are naive local time** and are parsed with
  `time.ParseInLocation(..., time.Local)`.
- **Borg archive hostname** is not in `borg list --json`; it is fetched on demand
  via `borg info --json` through the optional `SnapshotDetailer` interface.
- Borg extract runs with the restore target as its working directory and paths
  stripped of their leading slash, producing the same absolute-path-below-root
  layout as restic.
- Every child process runs in its own process group; context cancellation kills
  the whole group. Every command is checked against the read-only allowlist
  (`restic: version, snapshots, ls, stats, restore`; `borg: --version, list,
  info, extract`). A recording-fake-binary test asserts this for every adapter
  code path.

## Run lifecycle and status

- Restore stats are recomputed by walking the restored tree (authoritative and
  identical across backends), rather than trusting adapter-specific summaries.
- A corrupt repository that makes `restic restore` fail is classified as
  **error** (the test could not complete). This satisfies "fail or error, never
  pass". Check failures are **fail**.
- `size_trend` is **skipped** when there is no previous passing run to compare
  against (nothing to baseline).
- The space check requires `estimate × 1.1 + 1 GiB` free in `work_dir` before
  any restore starts. Note: on hosts where `work_dir` is a small tmpfs this
  legitimately errors; point `work_dir` at a disk with room.

## Containers and sandbox (M3)

- **Minimal Docker Engine API client, standard library only.** `internal/sandbox`
  speaks plain HTTP over the Docker-compatible socket (`DOCKER_HOST`, default
  `/var/run/docker.sock`; works with rootless Podman). No Docker SDK dependency.
- **Isolated network, no egress.** Each run gets a bridge network created with
  `Internal: true`, so restored services cannot reach the internet or publish
  ports. Service containers are hardened: `no-new-privileges`, `CapDrop: ALL`,
  optional CPU/memory limits from `defaults.resources` / per-service `resources`,
  and every container/network is labelled `restoredrill.run=<id>` +
  `restoredrill.managed=true`.
- **Probe container uses the tool's own image** with a hidden `restoredrill
  probe` subcommand (`probe http`, `probe wait`), so HTTP checks need no curl
  image (plan §8). The probe container is created **lazily** on the first HTTP
  probe: drills without HTTP checks (e.g. `postgres_restore` only) never pull the
  probe image. Override the image with `RESTOREDRILL_PROBE_IMAGE`.
- **Healthchecks are HTTP-based** (the only kind in v1). The implicit
  `service_healthy` check polls the configured HTTP probe from the probe
  container until the expectation is met or the timeout elapses; it does not
  rely on a Docker `HEALTHCHECK` instruction. A service that never becomes
  healthy is a **failed check**, not a lifecycle error.
- **Teardown is guaranteed.** Containers, the network and the sandbox directory
  are removed on failure, timeout, cancellation and panic (deferred teardown +
  panic recovery). `keep_on_failure` / `--keep` retain only the sandbox directory
  (containers are always removed); kept sandboxes expire after `keep_ttl`.
- **Crash recovery / GC.** `run` and `serve` sweep leftovers at startup and
  `restoredrill gc` does it on demand. GC removes labelled containers/networks
  and sandbox directories for runs that are no longer active. **Only directories
  whose name is a valid run ULID are touched** — anything else in `work_dir` is
  left alone. If the container runtime is unreachable, directory cleanup still
  happens and the container part is skipped with a warning.
- **Container tests are behind the `docker` build tag** and are skipped when no
  runtime is reachable. They build a tiny probe image (the tool binary on
  busybox) and a busybox `httpd` image, then exercise a full service drill,
  `postgres_restore`, egress blocking, teardown and GC. The busybox `httpd` test
  image is built with a default `CMD` so no `command` field had to be added to
  `Service` (the config format is unchanged after M1, per working rule #3).

## Web dashboard (M4)

- htmx is replaced by a small hand-written JavaScript file (SSE updates + the
  "Run now" action). Reading works without JavaScript. This keeps the supply
  chain minimal and the build fully offline (no CDN/npm fetch). The functional
  requirements of plan §10 are unchanged.