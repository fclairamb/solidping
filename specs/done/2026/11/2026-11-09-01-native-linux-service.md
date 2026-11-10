---
model: opus
effort: high
pr: own
automerge: false
---

# Native Linux service: `solidping service install` with systemd

## Problem

The Linux install page (`web/docs/docs/installation/linux.md`) has you write a systemd unit by hand. That recipe:

- has you create the `solidping` user and `/opt/solidping` yourself;
- puts the database password in `Environment=` lines of a unit file every user can read;
- uses `Restart=always`, so a clean stop by the server restarts it too.

Windows (spec 2026-11-07-04) and macOS (spec 2026-11-08-06) got `solidping service install`. Linux should get the
same commands, so the binary sets itself up correctly.

## Proposal

### Commands

The `service` command works on Linux too, with the same subcommands and flags. It stays hidden and unsupported on
the remaining platforms (BSD): the error becomes `services are only supported on Windows, macOS and Linux`.

| Command | Linux behaviour |
|---|---|
| `service install [--name solidping] [--dir <data dir>]` | Creates the service user (root) and the data folder, writes the unit, `daemon-reload`, `enable`. Does not start it. |
| `service uninstall [--name ...]` | `disable --now`, removes the unit, `daemon-reload`. Leaves the data folder and the user. |
| `service start [--name ...]` | `systemctl start`. |
| `service stop [--name ...]` | `systemctl stop`, which waits until the server has stopped. The unit stays enabled. |
| `service run --name ... --dir ...` | Hidden. What systemd launches. Fails with a clear message without `INVOCATION_ID` (set by systemd for every unit it starts). |

On Linux the `--name` default is `solidping`, the unit `solidping.service`. It must match `[A-Za-z0-9:_.-]+` (no
templates, nothing that leaves the unit folder). `install` refuses to overwrite an existing unit, and fails
clearly on a host not booted with systemd (no `/run/systemd/system`).

### System or user unit: chosen by `sudo`

- As root: a **system unit**, `/etc/systemd/system/<name>.service`, `WantedBy=multi-user.target`. Default data
  folder: `/var/lib/solidping`.
- As a user: a **user unit**, `$XDG_CONFIG_HOME/systemd/user/<name>.service` (`~/.config/...`),
  `WantedBy=default.target`, every `systemctl` call with `--user`. Default data folder:
  `$XDG_DATA_HOME/solidping` (`~/.local/share/solidping`). It starts at login; `install` points at
  `loginctl enable-linger` to start it at boot.

### The system unit runs as `solidping`

Unlike macOS, creating a system account is one portable command, so the system unit does not run as root:

- `install` creates `solidping` when missing: `useradd --system --user-group --no-create-home --home-dir <dir>
  --shell /usr/sbin/nologin`.
- `User=solidping` (no `Group=`: the user's primary group), `Environment=HOME=<dir>`, `UMask=0077`.
- `AmbientCapabilities=CAP_NET_RAW CAP_NET_BIND_SERVICE`: the raw-socket ICMP fallback and traceroute work even
  when `net.ipv4.ping_group_range` excludes the user, and `server.listen` may use a port below 1024.
- Hardening: `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict` with `ReadWritePaths=<dir>`.
- `install` warns when `solidping` cannot reach the binary (a private parent folder, e.g. `/root` or a `0750`
  home), suggesting `/usr/local/bin`.

A user unit gets none of this (a user manager cannot grant capabilities or set up the namespaces).

### The unit

`ExecStart` is the absolute path of the installing binary, then `service run --name <name> --dir <absolute dir>`,
as on Windows and macOS, each argument double-quoted with `\`, `"`, `%` and `$` escaped. Then:

- `WorkingDirectory`: the data folder.
- `Restart=on-failure`, `RestartSec=5`: restarted after a failed run, not after a clean stop.
- `TimeoutStopSec=30`: the time the server gets to close after SIGTERM, as on the other platforms.
- System unit: `Wants=`/`After=network-online.target`, and `After=postgresql.service` (ordering only, ignored
  when absent).
- No secrets: configuration goes in `config.yml` in the data folder. Unit files are world-readable.

The data folder path may not contain whitespace, quotes or backslashes (`WorkingDirectory=`, `ReadWritePaths=`
and `Environment=` would split or unescape it); `%` is escaped. The unit is written `0644`.

### Data folder

- Created `0700`, handed to `solidping` for a system unit. An existing folder is left as is, with a warning and
  the `chown` command when `solidping` does not own it.
- No `logs/` folder: see below.

### Logs

journald collects stdout and stderr, rotates and timestamps them, so `service run` does not write log files on
Linux: it enters the data folder and runs `serveTo(ctx, os.Stdout)`. Go crash output reaches the journal through
stderr. `journalctl [--user] -u solidping`.

### Docs

`web/docs/docs/installation/linux.md`: the hand-written unit gives way to `service install` / `config.yml` /
`service start`, the user-unit variant, options, customizing through `systemctl edit` drop-ins (an
`EnvironmentFile=` for those who prefer variables), a migration note for the old hand-written unit
(`--dir /opt/solidping` keeps its data), updating, and file storage.

## Non-goals

- Other init systems (OpenRC, runit, s6): `install` refuses, the docs point at `solidping serve`.
- `Type=notify` (sd_notify readiness): `Type=simple` works with every systemd still in support.
- A `--user` flag to run the system unit as another account: a drop-in with `User=` does it.
- Removing the `solidping` user on uninstall: it may own data folders.

## Acceptance Criteria

- [x] `solidping service install|uninstall|start|stop|run` work on Linux; on BSD `service` is hidden and each
      subcommand returns the unsupported error.
- [x] Root installs an enabled system unit in `/etc/systemd/system` running as `solidping`, created when missing;
      a user gets a user unit in `~/.config/systemd/user`, with the matching default data folder.
- [x] The unit carries the absolute binary path and data folder, restarts on failure only, holds no secret, and
      passes `systemd-analyze verify`.
- [x] A data folder created by install is `0700` and owned by the service user.
- [x] `service run` outside systemd fails with a message pointing at `serve`.
- [x] Logs reach the journal; stop is the SIGTERM graceful shutdown and waits until the server is gone.
- [x] Unit rendering, quoting, name and path checks, unit paths and default folder/name have platform-neutral
      unit tests.
- [x] `golangci-lint run` is clean for linux, darwin, windows and freebsd.
- [x] Docs cover `service install`, the user unit, drop-ins, and the migration from the old unit.

Checked by hand in an Ubuntu 24.04 container booted with systemd: system unit (user created, folder `solidping
700`, `systemd-analyze verify`, start, health, `CapAmb` = `NET_RAW` + `NET_BIND_SERVICE`, stop with the graceful
shutdown lines in the journal, a broken `config.yml` restarted every 5 s with exit code 1, started again after a
reboot, uninstall), the user unit with lingering, and the warnings (binary in `/root`, foreign-owned folder, path
with a space, `--name ../x`). The `linux-service` CI job (`.github/workflows/ci.yml`) repeats the system unit
cycle on every PR and gates `ci`.
