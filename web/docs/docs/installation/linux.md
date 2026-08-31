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
curl -L -o solidping https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-amd64

# Make it executable
chmod +x solidping

# Move to a system location (optional)
sudo mv solidping /usr/local/bin/
```

For ARM64 systems:

```bash
curl -L -o solidping https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-arm64
chmod +x solidping
```

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
LOG_LEVEL=info
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

## Updating

```bash
# Stop the service
sudo systemctl stop solidping

# Download new version
curl -L -o /tmp/solidping https://github.com/fclairamb/solidping/releases/latest/download/solidping-linux-amd64
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

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [Check Types](/features/check-types) - Configure your first checks
