---
sidebar_position: 2
title: Docker Compose
---

# Docker Compose Installation

Docker Compose is ideal for local development or small production deployments where you want to run SolidPing with its database in a single configuration.

## Development Setup

This setup includes hot-reload for development:

```yaml
version: '3.8'

services:
  postgres:
    image: postgres:16-alpine
    ports:
      - "55432:5432"
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: postgres
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 5s
      timeout: 5s
      retries: 5

  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    ports:
      - "4000:4000"
    environment:
      SP_DB_TYPE: postgres
      SP_DB_URL: postgresql://postgres:postgres@postgres:5432/postgres?sslmode=disable
      SP_FILESTORAGE_LOCAL_ROOT: /data/files
      LOG_LEVEL: debug
    volumes:
      - solidping-files:/data/files
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  postgres-data:
  solidping-files:
```

:::warning Don't skip the `solidping-files` volume
SolidPing also stores a handful of blobs (org logos, status-page assets,
incident screenshots) outside the database, at `SP_FILESTORAGE_LOCAL_ROOT`.
Without a mounted volume there, every upload is destroyed the next time the
container is recreated — silently. See
[File Storage](/configuration/file-storage) for the S3 alternative, which has
no such constraint and is the only option once you run more than one
`solidping` replica.
:::

## Production Setup

For production, use stronger credentials and persistent storage:

```yaml
version: '3.8'

services:
  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: solidping
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: solidping
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U solidping"]
      interval: 10s
      timeout: 5s
      retries: 5

  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    restart: unless-stopped
    ports:
      - "4000:4000"
    environment:
      SP_DB_TYPE: postgres
      SP_DB_URL: postgresql://solidping:${POSTGRES_PASSWORD}@postgres:5432/solidping?sslmode=disable
      SP_EMAIL_ENABLED: "true"
      SP_EMAIL_HOST: ${SMTP_HOST}
      SP_EMAIL_PORT: "587"
      SP_EMAIL_USERNAME: ${SMTP_USERNAME}
      SP_EMAIL_PASSWORD: ${SMTP_PASSWORD}
      SP_EMAIL_FROM: ${SMTP_FROM}
      SP_FILESTORAGE_LOCAL_ROOT: /data/files
      LOG_LEVEL: info
    volumes:
      - solidping-files:/data/files
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  postgres-data:
  solidping-files:
```

Create a `.env` file with your secrets:

```bash
POSTGRES_PASSWORD=your-secure-database-password
SMTP_HOST=smtp.example.com
SMTP_USERNAME=your-smtp-username
SMTP_PASSWORD=your-smtp-password
SMTP_FROM=noreply@example.com
```

## Running

```bash
# Start in background
docker-compose up -d

# View logs
docker-compose logs -f solidping

# Stop
docker-compose down

# Stop and remove volumes (WARNING: deletes data)
docker-compose down -v
```

## With Reverse Proxy (Traefik)

Example with Traefik for automatic HTTPS:

```yaml
version: '3.8'

services:
  traefik:
    image: traefik:v2.10
    command:
      - "--api.insecure=true"
      - "--providers.docker=true"
      - "--entrypoints.web.address=:80"
      - "--entrypoints.websecure.address=:443"
      - "--certificatesresolvers.letsencrypt.acme.httpchallenge=true"
      - "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web"
      - "--certificatesresolvers.letsencrypt.acme.email=admin@example.com"
      - "--certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json"
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - letsencrypt:/letsencrypt

  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: solidping
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: solidping
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U solidping"]
      interval: 10s
      timeout: 5s
      retries: 5

  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    restart: unless-stopped
    environment:
      SP_DB_TYPE: postgres
      SP_DB_URL: postgresql://solidping:${POSTGRES_PASSWORD}@postgres:5432/solidping?sslmode=disable
      SP_FILESTORAGE_LOCAL_ROOT: /data/files
    volumes:
      - solidping-files:/data/files
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.solidping.rule=Host(`monitoring.example.com`)"
      - "traefik.http.routers.solidping.entrypoints=websecure"
      - "traefik.http.routers.solidping.tls.certresolver=letsencrypt"
      - "traefik.http.services.solidping.loadbalancer.server.port=4000"
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  postgres-data:
  solidping-files:
  letsencrypt:
```

## Browser checks (headless Chrome) {#browser-checks-headless-chrome}

The SolidPing image is distroless and ships no browser. To run
[browser checks](/features/check-types#browser), add a headless Chrome service
and point SolidPing at it over the Chrome DevTools Protocol:

```yaml
services:
  browser:
    image: chromedp/headless-shell:151.0.7922.109
    restart: unless-stopped
    command:
      - --remote-debugging-address=0.0.0.0
      - --remote-debugging-port=9222
      # Required from Chrome 111 on for a non-browser websocket client.
      - --remote-allow-origins=*
      - --disable-gpu
      - --no-sandbox
    # Chrome mounts /dev/shm; Docker's 64 MB default makes tabs crash.
    shm_size: 1gb

  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    environment:
      SP_CHECKERS_BROWSER_CDP_URL: ws://browser:9222
    depends_on:
      - browser
```

Notes:

- **Pin the tag.** Every worker should run the same Chrome version, so a page
  that renders in one region renders in all of them.
- **Do not publish port 9222.** A reachable CDP endpoint is remote control of a
  browser; keep it on the compose network only.
- The repository's own `docker-compose.yml` carries this service behind a
  profile — `docker compose --profile browser up -d`.
- Without `SP_CHECKERS_BROWSER_CDP_URL`, SolidPing falls back to a locally
  installed Chrome (`SP_CHECKERS_BROWSER_CHROME_PATH`, or the usual binary
  names). It never downloads one.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [File Storage](/configuration/file-storage) - Local volume vs. S3 for uploaded blobs
- [Kubernetes Deployment](/installation/kubernetes) - For larger deployments
