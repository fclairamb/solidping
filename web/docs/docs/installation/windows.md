---
sidebar_position: 5
title: Windows Binary
---

# Windows Binary Installation

SolidPing can be run on Windows systems as a standalone executable or as a Windows Service.

## Download

Download the latest Windows release from GitHub:

1. Go to the [SolidPing Releases](https://github.com/fclairamb/solidping/releases) page
2. Download `solidping-windows-amd64.exe`
3. Rename to `solidping.exe` for convenience

Or use PowerShell:

```powershell
# Download the latest release
Invoke-WebRequest -Uri "https://github.com/fclairamb/solidping/releases/latest/download/solidping-windows-amd64.exe" -OutFile "solidping.exe"
```

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

### Using NSSM (Recommended)

[NSSM](https://nssm.cc/) (Non-Sucking Service Manager) is the easiest way to run SolidPing as a Windows Service.

1. Download NSSM from https://nssm.cc/download
2. Extract and add to your PATH, or use the full path

Install the service:

```powershell
# Install the service
nssm install SolidPing "C:\Program Files\SolidPing\solidping.exe" serve

# Set environment variables
nssm set SolidPing AppEnvironmentExtra SP_DB_TYPE=postgres
nssm set SolidPing AppEnvironmentExtra +SP_DB_URL=postgresql://user:password@localhost:5432/solidping
nssm set SolidPing AppEnvironmentExtra +SP_SERVER_LISTEN=:4000

# Set working directory
nssm set SolidPing AppDirectory "C:\Program Files\SolidPing"

# Configure logging
nssm set SolidPing AppStdout "C:\Program Files\SolidPing\logs\stdout.log"
nssm set SolidPing AppStderr "C:\Program Files\SolidPing\logs\stderr.log"

# Start the service
nssm start SolidPing
```

Manage the service:

```powershell
# Check status
nssm status SolidPing

# Stop service
nssm stop SolidPing

# Restart service
nssm restart SolidPing

# Remove service
nssm remove SolidPing confirm
```

### Using sc.exe (Native)

You can also use the native Windows Service Control Manager:

```powershell
# Create the service
sc.exe create SolidPing binPath= "C:\Program Files\SolidPing\solidping.exe serve" start= auto

# Note: Environment variables must be set system-wide or use a wrapper script
```

## Directory Structure

Recommended directory structure:

```
C:\Program Files\SolidPing\
├── solidping.exe
├── config.yml (optional)
├── data\
│   └── solidping.db (if using SQLite)
└── logs\
    ├── stdout.log
    └── stderr.log
```

Create the directories:

```powershell
New-Item -ItemType Directory -Path "C:\Program Files\SolidPing\data" -Force
New-Item -ItemType Directory -Path "C:\Program Files\SolidPing\logs" -Force
```

## Configuration File

Instead of environment variables, you can use a configuration file:

Create `C:\Program Files\SolidPing\config.yml`:

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

## Logs

View logs:

```powershell
# View stdout log
Get-Content "C:\Program Files\SolidPing\logs\stdout.log" -Tail 100 -Wait

# View stderr log
Get-Content "C:\Program Files\SolidPing\logs\stderr.log" -Tail 100 -Wait
```

## Updating

```powershell
# Stop the service
nssm stop SolidPing

# Download new version
Invoke-WebRequest -Uri "https://github.com/fclairamb/solidping/releases/latest/download/solidping-windows-amd64.exe" -OutFile "C:\Program Files\SolidPing\solidping.exe"

# Start the service
nssm start SolidPing
```

## Troubleshooting

### Service Won't Start

1. Check the logs in `C:\Program Files\SolidPing\logs\`
2. Verify environment variables are set correctly
3. Ensure the database is accessible
4. Check port 4000 is not in use: `netstat -an | findstr :4000`

### Permission Issues

Run PowerShell as Administrator when installing the service or modifying Program Files.

## File Storage

Alongside the database, SolidPing writes a handful of blobs — org logos,
status-page assets, incident screenshots — under `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`, relative to the working directory). With the service
setup above, that resolves under the persistent `AppDirectory`, so there is
nothing extra to configure here — this only matters for containerized
deployments. See [File Storage](/configuration/file-storage) for the S3
backend, useful once you run more than one node.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
