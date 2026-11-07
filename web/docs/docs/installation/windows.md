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

`solidping.exe` runs as a native Windows service. Nothing else to install.
From an administrator PowerShell:

```powershell
solidping service install
```

This registers a `SolidPing` service that starts with Windows and restarts
after a failure. It also creates the data folder `C:\ProgramData\SolidPing`,
readable only by `SYSTEM` and Administrators, since it holds your configuration
and database. The service runs from that folder, so the SQLite database and the
logs land there.

Configure it in `C:\ProgramData\SolidPing\config.yml` (open it from the same
administrator prompt, e.g. `notepad C:\ProgramData\SolidPing\config.yml`):

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

Then start it:

```powershell
solidping service start
```

Manage it:

```powershell
Get-Service SolidPing          # status
solidping service stop
Restart-Service SolidPing
solidping service uninstall    # stops and removes it, keeps C:\ProgramData\SolidPing
```

`service install` takes `--dir` to use another data folder and `--name` to
pick another service name (to run two instances, for example). Pass the same
`--name` to `start`, `stop` and `uninstall`.

The service runs the `solidping.exe` that installed it. If you move that file,
run `service uninstall` and `service install` again.

### Coming from NSSM

Earlier versions of this page used NSSM. To switch:

```powershell
nssm stop SolidPing
nssm remove SolidPing confirm
solidping service install
```

Then move your `config.yml` and, with SQLite, `solidping.db` from the old
`AppDirectory` into `C:\ProgramData\SolidPing`, turn any environment
variables you set with `AppEnvironmentExtra` into `config.yml` entries, and run
`solidping service start`.

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

The service writes its logs to `C:\ProgramData\SolidPing\logs\solidping.log`,
rotated at 20 MB with two older files kept (`solidping.log.1`, `.2`). A crash
goes to `crash.log` in the same folder.

```powershell
Get-Content C:\ProgramData\SolidPing\logs\solidping.log -Tail 100 -Wait
```

## Updating

Stop the service first: Windows does not let you replace a running `.exe`.

With winget:

```powershell
solidping service stop
winget upgrade fclairamb.SolidPing
solidping service start
```

With a downloaded binary, extract the new `solidping.exe` over the old one:

```powershell
solidping service stop
Invoke-WebRequest -Uri "https://github.com/fclairamb/solidping/releases/latest/download/solidping-windows-amd64.zip" -OutFile "solidping-windows-amd64.zip"
Expand-Archive -Path "solidping-windows-amd64.zip" -DestinationPath "C:\Program Files\SolidPing" -Force
solidping service start
```

## Troubleshooting

### Service Won't Start

1. Check `C:\ProgramData\SolidPing\logs\solidping.log` and `crash.log`
2. Check `C:\ProgramData\SolidPing\config.yml`
3. Ensure the database is accessible
4. Check port 4000 is not in use: `netstat -an | findstr :4000`

`solidping service run` is what Windows launches. It refuses to start from a
terminal: use `solidping serve` there.

### Permission Issues

`service install`, `uninstall`, `start` and `stop` need an administrator
prompt, and so does editing anything in `C:\ProgramData\SolidPing`.

## File Storage

Alongside the database, SolidPing writes a handful of blobs — org logos,
status-page assets, incident screenshots — under `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`, relative to the working directory). The service runs
from `C:\ProgramData\SolidPing`, so that resolves to
`C:\ProgramData\SolidPing\data\files` and there is
nothing extra to configure here — this only matters for containerized
deployments. See [File Storage](/configuration/file-storage) for the S3
backend, useful once you run more than one node.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
