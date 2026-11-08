---
model: opus
effort: high
pr: own
automerge: false
---

# Native macOS service: `solidping service install` with launchd

## Problem

The macOS install page (`web/docs/docs/installation/macos.md`) has you write a launchd plist by hand. That recipe
runs `solidping serve` with:

- the data in `/usr/local/var/solidping`, created world-readable by `mkdir`, though it holds `config.yml` and the
  database;
- secrets in the plist's `EnvironmentVariables`, in a world-readable file under `/Library/LaunchDaemons`;
- every log line in one unrotated `/usr/local/var/log/solidping.log`;
- `KeepAlive: true`, so `launchctl kill` or a clean exit restarts it too.

Windows got `solidping service install` in spec 2026-11-07-04 (PR #802). macOS should get the same commands, so
the binary sets itself up correctly. The Homebrew formula has no `service do` block either, so
`brew services start solidping` does not work.

## Proposal

### Commands

The `service` command of spec 2026-11-07-04 works on macOS too, with the same subcommands and flags. It stays
hidden and unsupported on other platforms (Linux, BSD): the error becomes `services are only supported on
Windows and macOS`.

| Command | macOS behaviour |
|---|---|
| `service install [--name io.solidping] [--dir <data dir>]` | Creates the data folder and its `logs/`, writes the plist. Does not load it. |
| `service uninstall [--name ...]` | Stops it if loaded, removes the plist. Leaves the data folder. |
| `service start [--name ...]` | `launchctl enable`, then `bootstrap` (it starts on load); `kickstart` if already loaded. |
| `service stop [--name ...]` | `launchctl bootout` (SIGTERM), waits until launchd has let it go (60 s at most). |
| `service run --name ... --dir ...` | Hidden. What launchd launches. Fails with a clear message when its parent is not launchd (PID 1). |

On macOS the `--name` default is `io.solidping`, the launchd label (reverse-DNS, as launchd expects). It is the
same label the old hand-written recipe used. `install` refuses to overwrite an existing plist.

### Daemon or agent: chosen by `sudo`

- As root (`sudo solidping service install`): a **daemon**, `/Library/LaunchDaemons/<label>.plist`, domain
  `system`. It starts at boot and runs as root. Default data folder: `/Library/Application Support/SolidPing`.
- As a user: an **agent** of that user, `~/Library/LaunchAgents/<label>.plist`, domain `gui/<uid>`. It starts at
  login and runs as the user. Default data folder: `~/Library/Application Support/SolidPing`.

The other subcommands pick the same domain the same way, so `sudo` must match between `install` and the rest.
`install` prints which kind it created. ICMP checks do not need root on macOS (unprivileged datagram ICMP
sockets), so the agent is fully functional.

### The plist

`ProgramArguments` is the absolute path of the installing binary, then `service run --name <label> --dir
<absolute dir>`, as on Windows. Then:

- `WorkingDirectory`: the data folder.
- `RunAtLoad`: true. Installed plists load at boot or login, so the service starts with the machine.
- `KeepAlive` `{SuccessfulExit: false}`: restarted after a failed run (non-zero exit, crash), not after a clean
  stop. `ThrottleInterval` 5 s, the Windows restart delay.
- `ExitTimeOut` 30 s: the time the server gets to close after SIGTERM, the Windows stop wait hint.
- `StandardOutPath` / `StandardErrorPath`: `<dir>/logs/launchd.log`. It only gets what happens before
  `service run` takes over the logs (a `chdir` failure, for example).
- No `EnvironmentVariables`: configuration goes in `config.yml` in the data folder, never in the world-readable
  plist.

The plist is written `0644` (launchd ignores a daemon plist that is group- or world-writable). Values are XML
escaped.

### Data folder

- Created `0700`, so owned by root and readable only by it for a daemon. An existing folder is left as is.
- `logs/` is created by `install` in all cases, since launchd opens `launchd.log` before solidping runs.

### Run, logs, lifecycle

- `service run` shares the Windows set-up, moved to `enterServiceDir` (`cmd_service_run.go`, built on Windows and
  macOS): enter the data folder, crash output to `logs/crash.log`, slog to `logs/solidping.log` rotated by
  `internal/rotatelog` (20 MB, 2 backups). launchd does not rotate files.
- It then runs `serveTo`, which already stops gracefully on SIGTERM. A failed run returns an error, so the process
  exits 1 and launchd restarts it.
- `serviceRun` takes the command's context on every platform (the macOS one passes it to `serveTo`).

### Gatekeeper

The release binary is unsigned. `install` warns when the binary still carries `com.apple.quarantine`, with the
`xattr -d` command to clear it, since launchd may refuse to start it. A warning, not an error: the user approved
it in some way to run `install` at all.

### Homebrew

Separate change in `fclairamb/homebrew-tap`: a `service do` block running `solidping serve` from
`var/solidping`, logs in `var/log/solidping.log`, restarted on crash. It works with released versions as they
are (no dependency on this change). The tap's daily bump workflow edits the formula in place, so the block
survives.

### Docs

`web/docs/docs/installation/macos.md`: Homebrew with `brew services`, the binary download, then a "launchd
Service" section on `service install` / `config.yml` / `service start`, the agent variant, options, a migration
note for the old hand-written plist, logs, updating and troubleshooting (quarantine, `launchctl print`, the
"Background Items Added" notification).

## Non-goals

- Linux (systemd). The same command could cover it later.
- A dedicated `_solidping` user for the daemon (creating users on macOS needs `dscl`/`sysadminctl`).
- Code signing or notarization.
- Unified logging (`os_log`): the rotated file is enough.

## Acceptance Criteria

- [x] `solidping service install|uninstall|start|stop|run` work on macOS; on other non-Windows platforms `service`
      is hidden and each subcommand returns the unsupported error.
- [x] Root installs a daemon in `/Library/LaunchDaemons`, a user an agent in `~/Library/LaunchAgents`, with the
      matching default data folder.
- [x] The plist carries the absolute binary path and data folder, restarts on failure only, and no environment.
- [x] A data folder created by install is `0700`; `logs/` always exists after install.
- [x] `service run` outside launchd fails with a message pointing at `serve`.
- [x] Logs rotate in `<dir>/logs`; crash output goes to `crash.log`.
- [x] Stop is the SIGTERM graceful shutdown and waits until the job is gone.
- [x] The plist rendering, domains, paths and default folder/name have platform-neutral unit tests.
- [x] `golangci-lint run` is clean for darwin, windows and linux.
- [x] Docs cover `brew services` and `service install`, with a migration note for the old plist.

Checked by hand on macOS 27.2 as an agent: install, start, health, start again (kickstart), stop, log check,
uninstall, `service run` refused from a terminal, and a broken `config.yml` restarted every 5 s with exit code 1.
The daemon path (`sudo`) is checked on a real macOS host by the `macos-service` CI job
(`.github/workflows/ci.yml`), which gates `ci`.
