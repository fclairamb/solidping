---
model: opus
effort: high
pr: own
automerge: false
---

# Native Windows service: `solidping service install`, no NSSM

## Problem

The Windows install page (`web/docs/docs/installation/windows.md`) runs SolidPing as a service through
[NSSM](https://nssm.cc/), a third-party download with no release since 2017, whose site is often down and
which some antivirus tools flag. Every Windows service install depends on it.

The "sc.exe (Native)" alternative on the same page does not work. A Windows service binary must talk to the
Service Control Manager (SCM); `solidping.exe` does not (nothing imports `golang.org/x/sys/windows/svc`), so
`sc.exe create ... serve` fails to start with error 1053 after 30 seconds. NSSM is only there to fill that gap.

## Proposal

Make `solidping.exe` a native Windows service. `golang.org/x/sys` is already a dependency.

### Commands

`solidping service` with five subcommands. On other platforms the command exists (hidden) and every subcommand
fails with `windows services are only supported on Windows`.

| Command | Does |
|---|---|
| `service install [--name SolidPing] [--dir <data dir>]` | Creates the data folder, registers the service (automatic start, restart on failure), prints the next steps. Needs an administrator prompt. |
| `service uninstall [--name SolidPing]` | Stops the service if it runs, then deletes it. Leaves the data folder. |
| `service start [--name SolidPing]` | Starts it. |
| `service stop [--name SolidPing]` | Stops it and waits until it is stopped. |
| `service run --name ... --dir ...` | Hidden. What the SCM launches. Fails with a clear message when not started by the SCM. |

The registered command line is `"<absolute path of solidping.exe>" service run --name "<name>" --dir "<dir>"`,
so the service does not depend on the current directory or `PATH` at install time.

### Data folder and configuration

- Default `--dir`: `%ProgramData%\SolidPing` (`C:\ProgramData\SolidPing`).
- `service run` changes into that folder first. Everything already relative to the working directory then lands
  there: `config.yml` / `config.local.yml`, the SQLite database (`db.dir` defaults to `.`), the embedded
  Postgres data.
- Configuration is `config.yml` in that folder. No environment variables in the service registry key: any local
  user can read `HKLM\SYSTEM\CurrentControlSet\Services\<name>`, and secrets (DB URL, JWT secret, SMTP password)
  must not land there.
- When `install` creates the folder, it gets a protected ACL: full control for `SYSTEM` and `Administrators`,
  nothing else, no inheritance from `ProgramData` (which grants `Users` read). An existing folder is left as is.
- The service runs as `LocalSystem`.

### Logs

- `service run` writes slog output to `<dir>\logs\solidping.log`, size-rotated: 20 MB, 2 backups. The rotating
  writer in `cmd/devloop/rotate.go` moves to `internal/rotatelog` so both use it.
- Go runtime crash output (panics, fatal errors) goes to `<dir>\logs\crash.log` via `debug.SetCrashOutput`.
  A service has no console, so without this a panic leaves no trace.

### Lifecycle

- The handler reports `Running` once the server goroutine starts, and accepts Stop and Shutdown.
- Stop / Shutdown cancels the server context: the same graceful path as SIGTERM. It reports `StopPending` with
  a 30 s wait hint while the server closes.
- If the server exits on its own with an error, the service stops with a service-specific exit code 1.
- Recovery actions: restart after 5 s, 3 times, reset after one day, also on non-crash failures, so an exit
  with an error restarts too.
- `serve` keeps its current behaviour. It gets an `io.Writer` for its logs, so `service run` reuses it unchanged
  (agent mode included: `SP_NODE_ROLE=agent` in `config.yml` works as a service too).

### Docs

`web/docs/docs/installation/windows.md`: the "Windows Service" section becomes `service install` /
`config.yml` / `service start`, plus `Get-Service` / `Restart-Service` / `service uninstall`. NSSM and the
broken `sc.exe` recipe are removed. The directory-structure and upgrade sections follow (data under
`ProgramData`, upgrade = stop, `winget upgrade`, start). A short note for existing NSSM users on how to
switch (`nssm remove SolidPing confirm`, then `service install`; move `solidping.db` into the data folder).

## Non-goals

- Windows event log output (needs a registered message file to be readable; the log file is enough).
- Running under a dedicated or virtual service account.
- An MSI or any installer. winget keeps shipping the bare `solidping.exe`.
- Changing anything on Linux or macOS beyond the `rotatelog` move.

## Acceptance Criteria

- [ ] `solidping service install|uninstall|start|stop|run` exist on Windows; on other platforms `service` is
      hidden and each subcommand returns the "only supported on Windows" error.
- [ ] `service run` outside the SCM fails with a message pointing at `serve`.
- [ ] Install registers the quoted absolute exe path, automatic start, recovery actions on non-crash failures.
- [ ] A folder created by install has the protected SYSTEM + Administrators ACL.
- [ ] Logs rotate in `<dir>\logs`; crash output goes to `crash.log`.
- [ ] Stop drives the same graceful shutdown as SIGTERM.
- [ ] The rotating writer lives in `internal/rotatelog` with its tests; devloop uses it, behaviour unchanged.
- [ ] Platform-neutral helpers (command line building, default data folder) have unit tests.
- [ ] `GOOS=windows go build` and `GOOS=windows golangci-lint run` are clean (CI lints only on Linux).
- [ ] The Windows install page no longer installs through NSSM; only a short migration note names it.
