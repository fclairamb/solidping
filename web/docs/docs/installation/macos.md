---
sidebar_position: 4.5
title: MacOs Binary
---

# macOS Installation

SolidPing runs on macOS (Apple Silicon and Intel) as a standalone binary,
and as a launchd service that starts at boot and restarts after a failure.

## Homebrew

The simplest way:

```bash
brew install fclairamb/tap/solidping
```

Run it as a background service with `brew services`:

```bash
brew services start solidping        # starts at login, restarted if it crashes
sudo brew services start solidping   # or: starts at boot, runs as root
```

The service runs `solidping serve` from `$(brew --prefix)/var/solidping`, so
put your `config.yml` there; the SQLite database lands there too. Its output
goes to `$(brew --prefix)/var/log/solidping.log`, which is not rotated.

```bash
brew services info solidping
brew services restart solidping      # after a config.yml change
brew services stop solidping
brew upgrade solidping && brew services restart solidping
```

## Binary download

Every release publishes a compressed binary for each architecture:

```bash
# Apple Silicon (M1 and later)
curl -L -o solidping-darwin-arm64.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-darwin-arm64.gz
gunzip -c solidping-darwin-arm64.gz > solidping

# Intel
curl -L -o solidping-darwin-amd64.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-darwin-amd64.gz
gunzip -c solidping-darwin-amd64.gz > solidping

chmod +x solidping
```

`uname -m` prints `arm64` on Apple Silicon and `x86_64` on Intel.

### Gatekeeper

The binary is unsigned and not notarized, so macOS Gatekeeper quarantines it
on download and refuses to run it. Clear the quarantine attribute once, then
install it root-owned in a stable location (a service runs it from there):

```bash
xattr -d com.apple.quarantine solidping
sudo install -o root -m 0755 solidping /usr/local/bin/
```

### Verify the checksum (recommended)

Check the downloaded `.gz` file against `solidping-checksums.txt`. macOS ships
`shasum` rather than `sha256sum`:

```bash
curl -L -o solidping-checksums.txt https://github.com/fclairamb/solidping/releases/latest/download/solidping-checksums.txt
shasum -a 256 -c --ignore-missing solidping-checksums.txt
```

## Running

```bash
# SQLite, data stored in the current directory
solidping serve
```

Open http://localhost:4000. On first start, macOS may ask whether SolidPing
may accept incoming network connections: allow it if other machines need to
reach the dashboard.

`solidping --version` prints the installed version. PostgreSQL and the other
database options work the same as on Linux, see the
[Linux guide](./linux.md#with-postgresql).

## launchd Service

The downloaded binary registers itself with launchd, no plist to write by
hand:

```bash
sudo solidping service install
sudo solidping service start
```

`install` writes `/Library/LaunchDaemons/io.solidping.plist`: a daemon that
starts at boot, runs as root and is restarted 5 seconds after a failed run
(not after you stop it). It also creates the data folder
`/Library/Application Support/SolidPing`, readable by root only, since it
holds your configuration and database. The
service runs from that folder, so the SQLite database and the logs land there:

```
/Library/Application Support/SolidPing/
├── config.yml        (your configuration)
├── solidping.db      (the SQLite database, if you use SQLite)
├── data/files/       (file storage)
└── logs/
    ├── solidping.log (rotated at 20 MB, 2 backups kept)
    ├── crash.log     (Go runtime crash output, if any)
    └── launchd.log   (errors before the server starts, if any)
```

Configure it in `config.yml` in that folder (see
[Configuration File](#configuration-file) below), for example with
`sudo nano "/Library/Application Support/SolidPing/config.yml"`.

Manage it:

```bash
sudo launchctl print system/io.solidping | head -20    # status
sudo solidping service stop                             # waits until it has stopped
sudo launchctl kickstart -k system/io.solidping         # restart, after a config.yml change
sudo solidping service uninstall                        # stops and removes it, keeps the data folder
```

A stopped service starts again at the next boot, as long as it is installed.

### As a user agent

Without `sudo`, the same commands install a launchd agent of the current user
instead: `~/Library/LaunchAgents/io.solidping.plist`, data in
`~/Library/Application Support/SolidPing`. It starts when you log in, not at
boot, and runs as you. Use `gui/$(id -u)/io.solidping` in place of
`system/io.solidping` in the `launchctl` commands above. Network checks,
ICMP ping included, work the same either way.

### Options

`service install` takes `--dir` to use another data folder and `--name` to
pick another launchd label (to run two instances, for example). Pass the same
`--name` to `start`, `stop` and `uninstall`.

The service runs the `solidping` that installed it, at its absolute path. If
you move that file, run `service uninstall` and `service install` again.

A daemon runs as root, so `sudo solidping service install` refuses a binary
that another account could replace: the file and every folder above it must be
owned by root, and none of them may be writable by its group or by others.
`sudo install -o root -m 0755 solidping /usr/local/bin/` puts it right; `sudo
mv` keeps your ownership. A Homebrew binary is owned by the user who installed
Homebrew, so it cannot back a root daemon: install a root-owned copy of the
downloaded binary instead. A user agent also accepts files you own.

If the data folder already exists, `service install` only reuses it when it is
still the way a previous install left it: a real folder (not a link), owned by
root (you, for an agent), not writable by its group or by others, with no link
inside and nothing owned by another account. Its parents must be owned by root
(or you) and not writable by others. Otherwise it stops with an error naming
the problem: delete the folder (after saving what you need from it) or pick
another one with `--dir`. The service checks the binary, the data folder and
its parents again each time it starts; the reason of a failed check goes to
`logs/launchd.log` in the data folder.

Agent mode works as a service too: set `node.role: agent` in `config.yml`
(the equivalent of `SP_NODE_ROLE=agent`; see the [Configuration Guide](/configuration)).

### Coming from a hand-written plist

Earlier versions of this page had you write `/Library/LaunchDaemons/io.solidping.plist`
yourself. `service install` refuses to overwrite it, so unload and remove it first:

```bash
sudo launchctl bootout system/io.solidping
sudo rm /Library/LaunchDaemons/io.solidping.plist
sudo solidping service install
```

Then move `solidping.db` (and `data/files/`, if any) from `/usr/local/var/solidping`
into `/Library/Application Support/SolidPing`, turn the `EnvironmentVariables` of
the old plist into `config.yml` entries, and run `sudo solidping service start`.

## Configuration File

Instead of environment variables, you can use a configuration file. For the
service, create `config.yml` in its data folder; when running
`solidping serve` by hand, put it in the current directory:

```yaml
db:
  type: postgres
  url: postgresql://user:password@localhost:5432/solidping

server:
  listen: ":4000"

email:
  enabled: true
  host: smtp.example.com
  port: 587
  username: noreply@example.com
  password: smtp-password
  from: noreply@example.com
```

Without a `config.yml`, it uses SQLite and listens on port 4000. Every `SP_*`
variable has a `config.yml` equivalent, see the
[Configuration Guide](/configuration).

## Logs

```bash
# Follow the server log
sudo tail -f "/Library/Application Support/SolidPing/logs/solidping.log"

# Crash output and launchd errors, if any
sudo cat "/Library/Application Support/SolidPing/logs/crash.log" \
         "/Library/Application Support/SolidPing/logs/launchd.log"
```

## Updating

Replace the binary in place: the service points at its absolute path.

```bash
sudo solidping service stop
curl -L https://github.com/fclairamb/solidping/releases/latest/download/solidping-darwin-arm64.gz | gunzip > solidping
chmod +x solidping
sudo install -o root -m 0755 solidping /usr/local/bin/solidping
sudo solidping service start
```

The data folder is untouched by an upgrade.

## Troubleshooting

### Service won't start

1. Check `logs/solidping.log`, `logs/crash.log` and `logs/launchd.log` in the data folder
2. Check `config.yml` in the data folder
3. Run `sudo launchctl print system/io.solidping` and look at `last exit code`
4. Check port 4000 is not in use: `lsof -nP -iTCP:4000 -sTCP:LISTEN`
5. Check the binary is not quarantined: `xattr /usr/local/bin/solidping` must not list `com.apple.quarantine`

`solidping service run` is what launchd launches; run from a terminal, it
refuses to start. To debug in a terminal, stop the service and run
`sudo solidping serve` from the data folder instead.

macOS may show a "Background Items Added" notification after `install`: it
lists the new launchd job under System Settings → General → Login Items &
Extensions, where it can also be turned off.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
