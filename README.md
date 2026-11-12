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

Requires Go 1.24+ to build, and `restic` (>= 0.17) and/or `borg` (1.2 / 1.4) on
the host that runs drills. Container-based checks additionally need Docker or a
Docker-compatible Podman socket.

```sh
go build -o restoredrill ./cmd/restoredrill
```

A single static binary ships the dashboard; no Node build step is required.

## Quickstart (file-only drill, ~5 minutes)

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
restoredrill serve                     Scheduler + dashboard + API + metrics
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
```

Integration tests **only ever create temporary repositories**; they never touch
real ones. Container tests need a Docker/Podman socket and are skipped
otherwise.

## License

Apache-2.0. See [LICENSE](LICENSE).