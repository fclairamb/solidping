---
sidebar_position: 4.5
title: macOS
---

# macOS Installation

SolidPing runs natively on macOS, on Apple Silicon and Intel.

## Homebrew

The simplest way:

```bash
brew install fclairamb/tap/solidping
```

Upgrade it later with `brew upgrade solidping`.

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

The binary is unsigned and not notarized. macOS quarantines it on download
and refuses to run it. Clear the quarantine attribute once, before the first
run:

```bash
xattr -d com.apple.quarantine solidping
```

Then move it to a directory on your `PATH` (optional):

```bash
sudo mv solidping /usr/local/bin/
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

## Run at boot (launchd)

macOS uses launchd instead of systemd. Create a data directory, then
`/Library/LaunchDaemons/io.solidping.plist`:

```bash
sudo mkdir -p /usr/local/var/solidping
```

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>io.solidping</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/solidping</string>
    <string>serve</string>
  </array>
  <key>WorkingDirectory</key>
  <string>/usr/local/var/solidping</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>SP_SERVER_LISTEN</key>
    <string>:4000</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/usr/local/var/log/solidping.log</string>
  <key>StandardErrorPath</key>
  <string>/usr/local/var/log/solidping.log</string>
</dict>
</plist>
```

With Homebrew on Apple Silicon, the binary is `/opt/homebrew/bin/solidping`:
adjust `ProgramArguments` accordingly. Add any other `SP_*` variable under
`EnvironmentVariables`.

Load and start it:

```bash
sudo mkdir -p /usr/local/var/log
sudo launchctl bootstrap system /Library/LaunchDaemons/io.solidping.plist
sudo launchctl print system/io.solidping | grep state
```

Restart it with `sudo launchctl kickstart -k system/io.solidping`, stop and
unload it with `sudo launchctl bootout system/io.solidping`.

## Logs

```bash
tail -f /usr/local/var/log/solidping.log
```

## Updating

With Homebrew, `brew upgrade solidping`. With a downloaded binary, download it
again as above, clear the quarantine attribute, replace
`/usr/local/bin/solidping`, then restart the service:

```bash
sudo launchctl kickstart -k system/io.solidping
```

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
