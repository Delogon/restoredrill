# Examples

## systemd (run without `serve`)

`systemd/restoredrill.service` and `systemd/restoredrill.timer` run
`restoredrill run --all` nightly without the dashboard/scheduler process. This
is the recommended setup for cron-style verification on a server.

```sh
# Install the binary and config first, then:
sudo cp examples/systemd/restoredrill.service /etc/systemd/system/
sudo cp examples/systemd/restoredrill.timer   /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now restoredrill.timer
```

Notes:

- Create a dedicated `restoredrill` user that can read the repositories. Only
  grant it Docker/Podman socket access if you use service drills (socket access
  is root-equivalent; prefer rootless Podman).
- The service treats exit codes 1 (fail) and 3 (error) as meaningful results,
  not as service failures.
- **Do not schedule this timer alongside backup `prune`/`compact` jobs.**
- Adjust `ReadWritePaths` if your `state_dir`/`work_dir` differ from the
  defaults, and add the restic cache / Borg base dir if you set them.

A docker-compose demo arrives with milestone 5.

## Serve (dashboard + scheduler)

`serve.yaml` configures `restoredrill serve`: a scheduled file-only drill, the
dashboard with basic auth, and an ntfy notifier. It needs only restic/Borg (no
Docker socket). Point the repository and password file at your own, replace the
placeholder bcrypt hash, then:

```sh
restoredrill serve --config examples/serve.yaml
# open http://127.0.0.1:8484  (admin / change-me until you change the hash)
```

See the README's "Dashboard, API & scheduler" section for auth modes, the JSON
API, metrics and notifications.

## Service drill (containers)

`service-drill.yaml` is a template for a full service drill: it restores a
snapshot, starts the restored data as a service on an isolated no-egress
network, waits for it to become healthy and probes it over HTTP. It needs a
Docker/Podman socket and the probe image (see the comments at the top of the
file for building one locally).