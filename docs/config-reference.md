# Configuration reference

This reference is generated from the config JSON Schema (`docs/schema.json`).
Regenerate with `go run ./cmd/gendocs`. The schema can be used for editor
validation of `restoredrill.yaml`.

Top-level keys of `restoredrill.yaml`:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `allow_inline_secrets` | boolean | no |  |
| `binaries` | [Binaries](#binaries) | no |  |
| `defaults` | [Defaults](#defaults) | no |  |
| `drills` | map of [Drill](#drill) | no |  |
| `include` | list of string | no |  |
| `notifiers` | map of [Notifier](#notifier) | no |  |
| `repositories` | map of [Repository](#repository) | no |  |
| `retention` | [Retention](#retention) | no |  |
| `server` | [Server](#server) | no |  |
| `state_dir` | string | no |  |
| `version` | integer | no |  |
| `work_dir` | string | no |  |

## Auth

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `api_token_file` | string | no |  |
| `header` | string | no |  |
| `mode` | string | no |  |
| `trusted_proxies` | list of string | no |  |
| `users` | map of [AuthUser](#authuser) | no |  |

## AuthUser

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `password_hash` | string | no |  |

## Binaries

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `borg` | string | no |  |
| `restic` | string | no |  |

## Check

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `dump` | string | no |  |
| `expect` | [Expect](#expect) | no |  |
| `format` | string | no |  |
| `headers` | map of string | no |  |
| `image` | string | no |  |
| `max_age` | string | no | Duration, e.g. 30m, 24h, 30d |
| `max_drop_percent` | number | no |  |
| `method` | string | no |  |
| `min_bytes` | integer | no |  |
| `min_count` | integer | no |  |
| `name` | string | no |  |
| `path` | string | no |  |
| `paths` | list of string | no |  |
| `port` | integer | no |  |
| `queries` | list of [SQLQuery](#sqlquery) | no |  |
| `query` | string | no |  |
| `run` | list of string | no |  |
| `sample` | integer | no |  |
| `seed` | integer | no |  |
| `service` | string | no |  |
| `source_root` | string | no |  |
| `timeout` | string | no | Duration, e.g. 30m, 24h, 30d |
| `type` | string | no | checksum_sample, command, files_exist, http, postgres_restore, service_healthy, size_trend, snapshot_age, sqlite_integrity, sqlite_query |

## Defaults

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `keep_on_failure` | boolean | no |  |
| `keep_ttl` | string | no | Duration, e.g. 30m, 24h, 30d |
| `notify` | list of string | no |  |
| `resources` | [Resources](#resources) | no |  |
| `timeout` | string | no | Duration, e.g. 30m, 24h, 30d |

## Drill

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `catch_up` | boolean | no |  |
| `checks` | list of [Check](#check) | no |  |
| `jitter` | string | no | Duration, e.g. 30m, 24h, 30d |
| `keep_on_failure` | boolean | no |  |
| `keep_ttl` | string | no | Duration, e.g. 30m, 24h, 30d |
| `notify` | list of string | no |  |
| `repository` | string | no |  |
| `resources` | [Resources](#resources) | no |  |
| `restore` | [Restore](#restore) | no |  |
| `sandbox` | [Sandbox](#sandbox) | no |  |
| `schedule` | string | no |  |
| `snapshot` | [Snapshot](#snapshot) | no |  |
| `timeout` | string | no | Duration, e.g. 30m, 24h, 30d |
| `uses` | string | no |  |
| `with` | map of any | no |  |

## Expect

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `contains` | string | no |  |
| `equals` | string | no |  |
| `exit_code` | integer | no |  |
| `json` | [JSONExpect](#jsonexpect) | no |  |
| `max` | number | no |  |
| `min` | number | no |  |
| `regex` | string | no |  |
| `status` | integer | no |  |

## HTTPProbe

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `expect` | [Expect](#expect) | no |  |
| `headers` | map of string | no |  |
| `method` | string | no |  |
| `path` | string | no |  |
| `port` | integer | no |  |

## Healthcheck

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `http` | [HTTPProbe](#httpprobe) | no |  |
| `timeout` | string | no | Duration, e.g. 30m, 24h, 30d |

## JSONExpect

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `equals` | string | no |  |
| `exists` | boolean | no |  |
| `path` | string | no |  |

## Mount

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `read_only` | boolean | no |  |
| `source` | string | no |  |
| `target` | string | no |  |

## Notifier

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `homeserver` | string | no |  |
| `on` | list of string | no |  |
| `renotify_after` | string | no | Duration, e.g. 30m, 24h, 30d |
| `room_id` | string | no |  |
| `secret` | string | no |  |
| `secret_file` | string | no |  |
| `token` | string | no |  |
| `token_file` | string | no |  |
| `type` | string | no |  |
| `url` | string | no |  |

## Repository

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `env` | map of string | no |  |
| `no_lock` | boolean | no |  |
| `passcommand` | string | no |  |
| `passphrase` | string | no |  |
| `password` | string | no |  |
| `password_command` | string | no |  |
| `password_file` | string | no |  |
| `repository` | string | no |  |
| `type` | string | no |  |

## Resources

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cpus` | number | no |  |
| `memory` | string | no |  |

## Restore

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `paths` | list of string | no |  |

## Retention

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `runs_per_drill` | integer | no |  |

## SQLQuery

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `expect` | [Expect](#expect) | no |  |
| `sql` | string | no |  |

## Sandbox

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `network` | string | no |  |
| `services` | map of [Service](#service) | no |  |

## Server

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `auth` | [Auth](#auth) | no |  |
| `listen` | string | no |  |
| `public_url` | string | no |  |
| `timezone` | string | no |  |

## Service

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cap_add` | list of string | no |  |
| `env` | map of string | no |  |
| `healthcheck` | [Healthcheck](#healthcheck) | no |  |
| `image` | string | no |  |
| `mounts` | list of [Mount](#mount) | no |  |
| `resources` | [Resources](#resources) | no |  |
| `user` | string | no |  |

## Snapshot

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `archive_glob` | string | no |  |
| `host` | string | no |  |
| `max_age` | string | no | Duration, e.g. 30m, 24h, 30d |
| `paths` | list of string | no |  |
| `select` | string | no |  |
| `tags` | list of string | no |  |
| `within` | string | no | Duration, e.g. 30m, 24h, 30d |
