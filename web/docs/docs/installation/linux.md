---
sidebar_position: 4
title: Linux Binary
---

# Linux Binary Installation

SolidPing can be run directly as a binary on Linux systems. This is useful for environments where Docker is not available or when you want maximum control over the deployment.

## Download

Download the latest release from GitHub:

```bash
# Download the latest release
curl -L -o solidping-linux-amd64.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-amd64.gz

# Decompress and make it executable
gunzip -c solidping-linux-amd64.gz > solidping
chmod +x solidping

# Move to a system location (optional)
sudo mv solidping /usr/local/bin/
```

For ARM64 systems:

```bash
curl -L -o solidping-linux-arm64.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-arm64.gz
gunzip -c solidping-linux-arm64.gz > solidping
chmod +x solidping
```

### Verify the checksum (recommended)

Every release also publishes `solidping-checksums.txt`, covering all five
published `.gz`/`.zip` archives. Verify it against the downloaded `.gz` file
itself — `gunzip -c` above leaves that file in place, so this works whether
you run it before or after decompressing:

```bash
curl -L -o solidping-checksums.txt https://github.com/fclairamb/solidping/releases/latest/download/solidping-checksums.txt
sha256sum -c --ignore-missing solidping-checksums.txt
```

`--ignore-missing` skips the checksum lines for archives you didn't download,
so this works no matter which platform's file you fetched.

## macOS

The same release publishes archives for macOS — Apple Silicon and Intel:

```bash
# Apple Silicon
curl -L -o solidping-darwin-arm64.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-darwin-arm64.gz
gunzip -c solidping-darwin-arm64.gz > solidping

# Intel
curl -L -o solidping-darwin-amd64.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-darwin-amd64.gz
gunzip -c solidping-darwin-amd64.gz > solidping

chmod +x solidping
```

The binary is unsigned and not notarized, so macOS Gatekeeper quarantines it
on download and refuses to run it. Clear the quarantine attribute once
before first run:

```bash
xattr -d com.apple.quarantine solidping
```

There is no dedicated macOS page (launchd service, Homebrew tap) yet — once
the binary is in place, the "Running" section below and the rest of this
guide apply the same way on macOS as on Linux.

## Running

### Quick Start with SQLite

```bash
# Run with SQLite (data stored in current directory)
./solidping serve
```

### With PostgreSQL

```bash
# Set environment variables
export SP_DB_TYPE=postgres
export SP_DB_URL="postgresql://user:password@localhost:5432/solidping"

# Run the server
./solidping serve
```

### Run Database Migrations

```bash
./solidping migrate
```

## Systemd Service

Create a systemd service for automatic startup:

```bash
sudo nano /etc/systemd/system/solidping.service
```

Add the following content:

```ini
[Unit]
Description=SolidPing Monitoring Platform
After=network.target postgresql.service

[Service]
Type=simple
User=solidping
Group=solidping
WorkingDirectory=/opt/solidping
ExecStart=/usr/local/bin/solidping serve
Restart=always
RestartSec=5

# Environment variables
Environment=SP_DB_TYPE=postgres
Environment=SP_DB_URL=postgresql://solidping:password@localhost:5432/solidping
Environment=SP_SERVER_LISTEN=:4000

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/solidping

[Install]
WantedBy=multi-user.target
```

Create the user and directories:

```bash
# Create system user
sudo useradd -r -s /bin/false solidping

# Create working directory
sudo mkdir -p /opt/solidping
sudo chown solidping:solidping /opt/solidping
```

Enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable solidping
sudo systemctl start solidping
sudo systemctl status solidping
```

## Environment File

For easier configuration, use an environment file:

```bash
sudo nano /opt/solidping/.env
```

```bash
# Database
SP_DB_TYPE=postgres
SP_DB_URL=postgresql://solidping:password@localhost:5432/solidping

# Server
SP_SERVER_LISTEN=:4000

# Email (optional)
SP_EMAIL_ENABLED=true
SP_EMAIL_HOST=smtp.example.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=noreply@example.com
SP_EMAIL_PASSWORD=smtp-password
SP_EMAIL_FROM=noreply@example.com

# Logging
SP_LOG_LEVEL=info
```

Update the systemd service to use the environment file:

```ini
[Service]
EnvironmentFile=/opt/solidping/.env
```

## Nginx Reverse Proxy

Configure Nginx as a reverse proxy:

```nginx
server {
    listen 80;
    server_name monitoring.example.com;
    return 301 https://$server_name$request_uri;
}

server {
    listen 443 ssl http2;
    server_name monitoring.example.com;

    ssl_certificate /etc/letsencrypt/live/monitoring.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/monitoring.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:4000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection 'upgrade';
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_cache_bypass $http_upgrade;
    }
}
```

## Firewall

Allow traffic on port 4000 (or your configured port):

```bash
# UFW
sudo ufw allow 4000/tcp

# firewalld
sudo firewall-cmd --permanent --add-port=4000/tcp
sudo firewall-cmd --reload

# iptables
sudo iptables -A INPUT -p tcp --dport 4000 -j ACCEPT
```

## Logs

View logs with journalctl:

```bash
# Follow logs
sudo journalctl -u solidping -f

# View last 100 lines
sudo journalctl -u solidping -n 100

# View logs since today
sudo journalctl -u solidping --since today
```

At the default `info` level, startup ends with a `SolidPing is ready` line that gives the URL to open, the version and the database. On a fresh install a second line gives the first login. After that, a check run is only logged when its check changes status, for example `Check down name="Local Redis" slug=redis-localhost previous=validating error="connection refused"` once the confirmation period has passed. Set `LOG_LEVEL=debug` to see every check run and job.

## Updating

```bash
# Stop the service
sudo systemctl stop solidping

# Download new version
curl -L -o /tmp/solidping.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-amd64.gz
gunzip -c /tmp/solidping.gz > /tmp/solidping
chmod +x /tmp/solidping

# Replace binary
sudo mv /tmp/solidping /usr/local/bin/solidping

# Start the service
sudo systemctl start solidping
```

## File Storage

Alongside the database, SolidPing writes a handful of blobs — org logos,
status-page assets, incident screenshots — under `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`, relative to the working directory). With the systemd
setup above, that resolves under the persistent `WorkingDirectory`
(`/opt/solidping`), which `ReadWritePaths` already covers, so there is nothing
extra to mount here — this only matters for containerized deployments. See
[File Storage](/configuration/file-storage) for the S3 backend, useful once
you run more than one node.

## Small hosts (1 GB and less)

SolidPing runs on a 1 GB Raspberry Pi or a small VPS. A fresh instance idles
with about 40 MB of its own memory. Logins are the only bursts: each password
check allocates the argon2id memory and gives it back about a second later. On
a host with 2 GiB of memory or less, that is 19 MiB per login by default (64 MiB
on a larger host, see below).

**Read the right number.** `ps` and `top` report RSS, which includes 70-85 MB of
binary pages. Those pages are shared with the page cache and reclaimable. The
number that matters is `RssAnon` in `/proc/<pid>/status`:

```bash
grep -E 'VmRSS|RssAnon|RssFile|VmHWM' /proc/$(pidof solidping)/status
```

These knobs help, with what each one was measured to do:

| Setting | Effect |
|---|---|
| `SP_RUNTIME_MEMORY_LIMIT=128MiB` (or the native `GOMEMLIMIT=128MiB`) | Soft cap on the Go heap. At 96 MiB the first-boot peak (VmHWM) dropped from 229-245 MB to 169 MB. It is a soft cap: the runtime collects more often near it, it never refuses memory. |
| Nothing (default) | With no cgroup limit and no cap set, a Linux host with at most 2 GiB of RAM gets half of it as the soft cap (512 MiB on a 1 GB Pi). `SP_RUNTIME_AUTO_MEMORY_LIMIT=false` turns this off. |
| `SP_SERVER_CHECK_WORKER_NB` | Only worth lowering if the host runs many slow checks at once. Idle runners cost almost nothing (all goroutines together use about 3 MB of stack). |
| Nothing (default) | On a host with 2 GiB or less (the cgroup limit when one is set, else the host RAM), a single-process install (`SP_NODE_ROLE=all`, the default) hashes passwords with the OWASP minimum argon2id profile `m=19456 t=2 p=1`. Each login's transient drops from 64 MiB to 19 MiB, and four concurrent logins peak at about 76 MiB instead of 256 MiB. The server logs `password hashing: argon2id small-host profile` once at boot when it applies. |
| `SP_AUTH_PASSWORD_ARGON2_AUTO_PROFILE=false` | Keeps the 64 MiB profile on a small host. Setting any of `SP_AUTH_PASSWORD_ARGON2_MEMORY`, `_TIME` or `_THREADS` (or the same keys in `config.yml` or in Server Settings) also keeps your values. |

:::caution Security trade-off
The smaller argon2id profile makes each stolen password hash cheaper to brute
force. It is still the OWASP minimum and still memory-hard. If memory is not
the constraint on your small host, force the 64 MiB profile back with
`SP_AUTH_PASSWORD_ARGON2_AUTO_PROFILE=false`. Existing hashes are upgraded to
the active profile on the next login. See
[Authentication](/configuration/authentication#password-hashing).
:::

A fresh install seeds no browser check. Each browser check run starts a headless
Chrome (40-120 MB). The browser samples (`browser-example-com` and
`js-browser-login`) stay available in the create-check sample picker. Test mode
seeds them disabled.

See [Runtime memory](/configuration#runtime-memory) for every `SP_RUNTIME_*` knob.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
