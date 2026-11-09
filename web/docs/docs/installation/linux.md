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

For RISC-V 64 systems, use `solidping-linux-riscv64.gz` the same way.

### Verify the checksum (recommended)

Every release also publishes `solidping-checksums.txt`, covering all six
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

See the [macOS guide](./macos.md) (Homebrew, binary download, Gatekeeper, launchd).

## Checking the installed version

`solidping --version` prints the version of the binary, whichever way you
installed it. `solidping version` adds the commit and build time (`-o json`
for a script). Neither starts the server or reads its configuration:

```bash
$ solidping --version
solidping version 0.44.0
$ solidping version
Version:    0.44.0
Commit:     1a2b3c4d
Git Time:   2026-11-01T10:00:00Z
```

A running server reports the same fields at `GET /api/mgmt/version`.

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

## systemd Service

The downloaded binary registers itself with systemd, no unit to write by hand.
Put it in a system location first, since the service runs it from there:

```bash
sudo mv solidping /usr/local/bin/
sudo solidping service install
sudo solidping service start
```

`install` writes `/etc/systemd/system/solidping.service` and enables it, so it
starts at boot. If needed, it creates the `solidping` system user (no login,
no home) that runs the server. The service is restarted 5 seconds after a
failed run (not after you stop it). `install` also creates the data folder
`/var/lib/solidping`, owned by `solidping` and readable by it only, since it
holds your configuration and database. An existing folder is left as is.
The service runs from that folder, so the SQLite database lands there:

```
/var/lib/solidping/
├── config.yml        (your configuration)
├── solidping.db      (the SQLite database, if you use SQLite)
└── data/files/       (file storage)
```

Logs go to the journal (see [Logs](#logs)).

Configure it in `config.yml` in that folder (see
[Configuration File](#configuration-file) below), for example with
`sudo -u solidping nano /var/lib/solidping/config.yml`.

The unit is hardened: the whole system is read-only to the server except its
data folder, and it gets a private `/tmp`. It gets only two capabilities:
`CAP_NET_RAW` for ICMP checks, and `CAP_NET_BIND_SERVICE` to listen on a port
below 1024.

Manage it:

```bash
systemctl status solidping                # status
sudo solidping service stop               # waits until it has stopped
sudo systemctl restart solidping          # restart, after a config.yml change
sudo solidping service uninstall          # stops and removes it, keeps the data folder
```

A stopped service starts again at the next boot, as long as it is installed.
`install` needs a host booted with systemd. On another init system (OpenRC,
runit, a container), run `solidping serve` from it instead.

### As a user unit

Without `sudo`, the same commands install a systemd unit of the current user
instead: `~/.config/systemd/user/solidping.service`, data in
`~/.local/share/solidping`. It runs as you, without the hardening above, and
starts when you log in. To start it at boot, run `loginctl enable-linger`.
Add `--user` to the `systemctl` and `journalctl` commands on this page.

### Options

`service install` takes `--dir` to use another data folder and `--name` to
pick another unit name (to run two instances, for example). Pass the same
`--name` to `start`, `stop` and `uninstall`. The data folder path can't
contain spaces, quotes or backslashes.

The service runs the `solidping` that installed it, at its absolute path. If
you move that file, run `service uninstall` and `service install` again.

Agent mode works as a service too: set `node.role: agent` in `config.yml`
(the equivalent of `SP_NODE_ROLE=agent`; see the [Configuration Guide](/configuration)).

### Customizing the unit

Change the unit with a drop-in, which `install` and `uninstall` leave alone:

```bash
sudo systemctl edit solidping
```

```ini
[Service]
# Environment variables rather than config.yml (keep the file private: chmod 600)
EnvironmentFile=/var/lib/solidping/.env
# Order the start after a local service
After=docker.service
```

Then `sudo systemctl restart solidping`. Never put secrets in an
`Environment=` line: unit files are readable by every user.

### Coming from a hand-written unit

Earlier versions of this page had you write `/etc/systemd/system/solidping.service`
yourself. `service install` refuses to overwrite it, so stop and remove it first:

```bash
sudo systemctl disable --now solidping
sudo rm /etc/systemd/system/solidping.service
sudo systemctl daemon-reload
sudo solidping service install --dir /opt/solidping
```

`--dir /opt/solidping` keeps your existing data folder, still owned by the
`solidping` user. Turn the `Environment=` lines of the old unit into
`config.yml` entries (or an `EnvironmentFile=` drop-in, see above), then run
`sudo solidping service start`.

## Configuration File

Instead of environment variables, you can use a configuration file. For the
service, create `config.yml` in its data folder; when running
`solidping serve` by hand, put it in the current directory:

```yaml
db:
  type: postgres
  url: postgresql://solidping:password@localhost:5432/solidping

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

The service logs to the journal (add `--user` for a user unit):

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
sudo solidping service stop

# Download new version
curl -L -o /tmp/solidping.gz https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-amd64.gz
gunzip -c /tmp/solidping.gz > /tmp/solidping
chmod +x /tmp/solidping

# Replace binary
sudo mv /tmp/solidping /usr/local/bin/solidping

# Start the service
sudo solidping service start
```

The unit keeps pointing at `/usr/local/bin/solidping`, so there is nothing to
reinstall. The new version runs its database migrations at startup.

## File Storage

Alongside the database, SolidPing writes a handful of blobs — org logos,
status-page assets, incident screenshots — under `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`, relative to the working directory). For the systemd
service, that resolves under its data folder (`/var/lib/solidping/data/files`),
the one folder the unit can write to, so there is nothing to configure here.
If you point it elsewhere, add that folder to `ReadWritePaths=` with a drop-in
(see [Customizing the unit](#customizing-the-unit)). See
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
