# Demo: zero to a passing drill

This compose stack seeds a throwaway restic repository with sample data, runs
a drill against it (using the bundled `generic-files` recipe) and serves the
dashboard. No existing backup and no host restic installation are needed —
the image bundles restic and Borg.

Requirements: Docker with the compose plugin (or Podman with
`podman compose`). About 10 minutes, most of it the image build.

## Smoke test

```sh
cd examples/demo
docker compose up --build --abort-on-container-exit --exit-code-from drill
```

What happens:

1. `seed` initializes a restic repository in a named volume and backs up 14
   sample files (12 documents, 2 photos).
2. `drill` runs `restoredrill run --all`: restores the latest snapshot into a
   throwaway sandbox and checks that at least 10 files exist and the snapshot
   is fresh.
3. Compose exits with the drill's exit code: **0 = pass**, 1 = fail, 3 = error.

A passing run ends with:

```
DRILL                    STATUS     SNAPSHOT     DURATION   MESSAGE
demo-files               pass ✔     0a1f135a…    2.2s       14 files, 691 bytes restored
```

## Interactive dashboard

```sh
docker compose up --build -d dashboard
```

Open <http://127.0.0.1:8484> and log in with **admin / change-me** (demo
credentials — the config binds all interfaces inside the container, so auth
is mandatory; compose publishes the port on host loopback only).

The overview shows the drill that already ran. **Run now** triggers another
run with live log streaming; the demo schedule (`*/15 * * * *`) also fires on
its own. `Ctrl-C`/`docker compose down` tears everything down; the drill's
sandbox is always removed, even on failure.

## Poke the failure path

Make the drill fail on purpose to see fail-closed behaviour:

```sh
# require more files than the backup contains
sed -i 's/min_count: 10/min_count: 999/' restoredrill.yaml
docker compose up --build --abort-on-container-exit --exit-code-from drill
# exit code 1, dashboard shows fail with the check message
sed -i 's/min_count: 999/min_count: 10/' restoredrill.yaml
```

## Cleanup

```sh
docker compose down -v   # removes the demo repo, state and work volumes
```

## Notes

- Until the first published release, `--build` builds the image from the
  repository's `Dockerfile`. Afterwards `docker compose pull` works and the
  build step can be dropped.
- The demo is file-only, so it needs no Docker socket inside the container.
  Service drills (Service1, Postgres) additionally need the socket — see
  `examples/service-drill.yaml`.
- All demo secrets (`secrets/repo.pass`, the dashboard password hash) are
  public placeholder values. Replace them before any real use.