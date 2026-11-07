---
sidebar_position: 5
title: Windows Binary
---

# Windows Binary Installation

SolidPing can be run on Windows systems as a standalone executable or as a Windows Service.

## winget

The simplest way, on Windows 10 and 11:

```powershell
winget install fclairamb.SolidPing
```

This puts a `solidping` command on your `PATH` (open a new terminal first), so
the examples below work with `solidping` in place of `.\solidping.exe`.
Upgrade it later with `winget upgrade fclairamb.SolidPing`.

To install it for all users, which suits a Windows Service, add
`--scope machine` from an administrator prompt.

## Download

Download the latest Windows release from GitHub:

1. Go to the [SolidPing Releases](https://github.com/fclairamb/solidping/releases) page
2. Download `solidping-windows-amd64.zip`
3. Extract it — the archive contains a single, already-named `solidping.exe`

Or use PowerShell:

```powershell
# Download the latest release
Invoke-WebRequest -Uri "https://github.com/fclairamb/solidping/releases/latest/download/solidping-windows-amd64.zip" -OutFile "solidping-windows-amd64.zip"

# Extract it (produces solidping.exe in the current directory)
Expand-Archive -Path "solidping-windows-amd64.zip" -DestinationPath "."
```

### Verify the checksum (recommended)

Every release also publishes `solidping-checksums.txt`, covering all five
published `.gz`/`.zip` archives. Verify it against the downloaded `.zip`
itself — before extracting it, so the check covers exactly what GitHub
served:

```powershell
Invoke-WebRequest -Uri "https://github.com/fclairamb/solidping/releases/latest/download/solidping-checksums.txt" -OutFile "solidping-checksums.txt"

# The checksums file lists the sha256 for each platform's archive — find the
# solidping-windows-amd64.zip line and compare it to the hash below.
Get-FileHash solidping-windows-amd64.zip -Algorithm SHA256
Get-Content solidping-checksums.txt | Select-String "solidping-windows-amd64.zip"
```

:::note
`postgres-embedded` (the bundled PostgreSQL supervisor available on Linux and
macOS) is not available on Windows — use `sqlite` or an external `postgres`
database instead, as covered above and below.
:::

## Running

### Quick Start with SQLite

Open Command Prompt or PowerShell in the directory containing `solidping.exe`:

```powershell
# Run with SQLite (data stored in current directory)
.\solidping.exe serve
```

### With PostgreSQL

```powershell
# Set environment variables
$env:SP_DB_TYPE = "postgres"
$env:SP_DB_URL = "postgresql://user:password@localhost:5432/solidping"

# Run the server
.\solidping.exe serve
```

### Run Database Migrations

```powershell
.\solidping.exe migrate
```

## Windows Service

`solidping.exe` runs as a native Windows service, with no third-party wrapper. From an administrator PowerShell prompt:

```powershell
# Register the service: automatic start, restarted on failure
solidping service install
```

This creates the data folder `C:\ProgramData\SolidPing` (pick another one with
`--dir`, and another service name with `--name`) and registers the service to
run as `LocalSystem` from that folder. A folder created by `install` is readable
by `SYSTEM` and `Administrators` only, since it holds your secrets; an existing
folder is left as is.

Everything the service reads or writes lives in the data folder:

```
C:\ProgramData\SolidPing\
├── config.yml        (your configuration)
├── solidping.db      (the SQLite database, if you use SQLite)
├── data\files\       (file storage)
└── logs\
    ├── solidping.log (rotated at 20 MB, 2 backups kept)
    └── crash.log     (Go runtime crash output, if any)
```

Configure the service in `C:\ProgramData\SolidPing\config.yml` (see
[Configuration File](#configuration-file) below), not with environment
variables: the service registry key is readable by every local user, so
secrets such as the database URL or the SMTP password must not go there. Then
start it:

```powershell
solidping service start
```

Manage the service:

```powershell
# Check status
Get-Service SolidPing

# Restart after a config.yml change
Restart-Service SolidPing

# Stop it (waits until it has stopped)
solidping service stop

# Remove it (stops it first; the data folder is kept)
solidping service uninstall
```

Agent mode works as a service too: set `node.role: agent` in `config.yml`
(the equivalent of `SP_NODE_ROLE=agent`; see the [Configuration Guide](/configuration)).

### Switching from NSSM

If you set up SolidPing with NSSM before, from an administrator prompt:

```powershell
nssm stop SolidPing
nssm remove SolidPing confirm
solidping service install
```

Then move your `solidping.db` (and `data\files\`, if any) from the old
working directory into `C:\ProgramData\SolidPing`, turn the environment
variables you gave NSSM into `config.yml` entries, and run
`solidping service start`.

## Configuration File

Instead of environment variables, you can use a configuration file. For the
service, create `C:\ProgramData\SolidPing\config.yml`; when running
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

## Firewall

Allow incoming connections through Windows Firewall:

```powershell
# Allow inbound on port 4000
New-NetFirewallRule -DisplayName "SolidPing" -Direction Inbound -Protocol TCP -LocalPort 4000 -Action Allow
```

Or through the Windows Firewall GUI:
1. Open "Windows Defender Firewall with Advanced Security"
2. Click "Inbound Rules" → "New Rule"
3. Select "Port" → TCP → Specific port: 4000
4. Allow the connection
5. Name it "SolidPing"

## IIS Reverse Proxy

If you're using IIS, you can configure it as a reverse proxy:

1. Install the URL Rewrite and Application Request Routing modules
2. Enable proxy in ARR
3. Add a rewrite rule in `web.config`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<configuration>
    <system.webServer>
        <rewrite>
            <rules>
                <rule name="SolidPing Proxy" stopProcessing="true">
                    <match url="(.*)" />
                    <action type="Rewrite" url="http://localhost:4000/{R:1}" />
                </rule>
            </rules>
        </rewrite>
    </system.webServer>
</configuration>
```

SolidPing gzip-compresses its own responses (`SP_SERVER_COMPRESSION`, default
`true`), so IIS needs no compression module of its own for this — and turning
one on anyway is harmless, since a response that already carries
`Content-Encoding` passes through untouched.

## Logs

The service writes its logs to `C:\ProgramData\SolidPing\logs\`:

```powershell
# Follow the server log
Get-Content "C:\ProgramData\SolidPing\logs\solidping.log" -Tail 100 -Wait

# Go runtime crash output (panics), if the service died unexpectedly
Get-Content "C:\ProgramData\SolidPing\logs\crash.log"
```

## Updating

With winget:

```powershell
solidping service stop
winget upgrade fclairamb.SolidPing
solidping service start
```

With a downloaded binary, overwrite `solidping.exe` in place: the service
points at its absolute path.

```powershell
# Stop the service
solidping service stop

# Download new version
Invoke-WebRequest -Uri "https://github.com/fclairamb/solidping/releases/latest/download/solidping-windows-amd64.zip" -OutFile "solidping-windows-amd64.zip"
Expand-Archive -Path "solidping-windows-amd64.zip" -DestinationPath "C:\Program Files\SolidPing" -Force

# Start the service
solidping service start
```

The data folder is untouched by an upgrade.

## Troubleshooting

### Service Won't Start

1. Check `C:\ProgramData\SolidPing\logs\solidping.log` and `crash.log`
2. Verify `C:\ProgramData\SolidPing\config.yml`
3. Ensure the database is accessible
4. Check port 4000 is not in use: `netstat -an | findstr :4000`

`solidping service run` is what Windows launches; run by hand, it refuses to
start. To debug in a console, stop the service and run `solidping serve` from
the data folder instead.

### Permission Issues

Run PowerShell as Administrator for `solidping service install`, `uninstall`,
`start` and `stop`, and to edit files in `C:\ProgramData\SolidPing`.

## File Storage

Alongside the database, SolidPing writes a handful of blobs — org logos,
status-page assets, incident screenshots — under `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`, relative to the working directory). With the service
setup above, that resolves under the data folder (`C:\ProgramData\SolidPing`),
so there is nothing extra to configure here — this only matters for containerized
deployments. See [File Storage](/configuration/file-storage) for the S3
backend, useful once you run more than one node.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
