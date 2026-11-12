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

More examples (docker-compose demo, service drills) arrive with later
milestones.