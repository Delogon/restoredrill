# Security model

A restore test handles a full copy of production data and touches the last line
of defence. When in doubt, restoredrill chooses the safer behaviour: read-only,
no network, fail closed.

## Repositories stay untouched

- Adapters may only build these commands:
  - restic: `version`, `snapshots`, `ls`, `stats`, `restore`
  - Borg: `--version`, `list`, `info`, `extract`
  A unit test asserts this allowlist for every code path.
- `restic restore` and `borg extract` take non-exclusive locks. An optional
  `no_lock: true` (restic only) supports read-only storage; the risk of racing
  with a concurrent write is documented and yours to accept.
- **Do not schedule drills at the same time as `prune` or `compact`.** Those
  take exclusive locks and rewrite packs; a concurrent restore can fail or read
  inconsistent data.

## Secrets

- Passwords reach child processes only through the environment
  (`RESTIC_PASSWORD_FILE`, `RESTIC_PASSWORD_COMMAND`, `BORG_PASSCOMMAND`), never
  through argv.
- A literal password in the config is a validation error unless
  `allow_inline_secrets: true` is set. Prefer `*_file`, `*_command` or env vars.
- All log lines, stored events, API responses and HTML pass through a redactor
  that knows every configured secret value.
- `validate` warns if a secret file is readable by group or others.

## Restored data

- The sandbox directory is `0700`, owned by the runner user, and deleted on
  teardown. Kept sandboxes (`keep_on_failure`) expire after `keep_ttl`.
- **Use a `work_dir` on an encrypted local disk.** Restored data is a full copy
  of production data.
- Ownership is only preserved when running as root. Rootless Podman with a
  `user:` mapping is supported for service containers.

## Containers (service drills)

- Default network is a Docker network with `internal: true`: **no egress, no
  published ports**. Restored services cannot send mail, call webhooks or sync
  anywhere.
- HTTP checks run from a probe container on the same network.
- Defaults: CPU and memory limits, `no-new-privileges`, all capabilities dropped
  unless the service config adds them.
- **Access to the Docker socket is equivalent to root.** Prefer rootless Podman
  with a Docker-compatible socket. Only grant socket access to the runner user.

## Dashboard

- Binds to `127.0.0.1` by default. Binding to any other address without an auth
  mode is refused at startup.
- Auth modes: `basic` (bcrypt hashes) and `trusted_header` (behind a reverse
  proxy, with required trusted-proxy CIDRs).
- CSRF tokens on all POST requests, rate limiting on "Run now", and a strict
  Content Security Policy with no inline scripts.

## Supply chain

- Few dependencies; `govulncheck` and `go mod verify` run in CI. Releases are
  signed with cosign and ship an SBOM.