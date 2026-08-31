---
sidebar_position: 1
title: Docker
---

# Docker Installation

Docker is the recommended way to run SolidPing. It provides isolation, easy updates, and consistent behavior across environments.

## Quick Start

### With SQLite (Simplest)

For testing or small deployments, you can use SQLite as the database:

```bash
docker run -d \
  --name solidping \
  -p 4000:4000 \
  -v solidping-data:/data \
  -e SP_DB_TYPE=sqlite \
  -e SP_DB_DIR=/data \
  -e SP_FILESTORAGE_LOCAL_ROOT=/data/files \
  ghcr.io/fclairamb/solidping:latest
```

### With PostgreSQL (Recommended for Production)

For production deployments, PostgreSQL is recommended:

```bash
docker run -d \
  --name solidping \
  -p 4000:4000 \
  -v solidping-files:/data/files \
  -e SP_DB_TYPE=postgres \
  -e SP_DB_URL="postgresql://user:password@host:5432/solidping" \
  -e SP_FILESTORAGE_LOCAL_ROOT=/data/files \
  ghcr.io/fclairamb/solidping:latest
```

:::warning Don't skip the volume
SolidPing also stores a handful of blobs (org logos, status-page assets,
incident screenshots) outside the database, at `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`, relative to the container's working directory).
Without a mounted volume there, every upload is destroyed on the next
`docker stop` / `docker run` cycle — silently. See
[File Storage](/configuration/file-storage) for the S3 alternative, which has
no such constraint.
:::

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_DB_TYPE` | `sqlite` | Database type: `postgres`, `sqlite`, `sqlite-memory` |
| `SP_DB_URL` | - | PostgreSQL connection string (required if `SP_DB_TYPE=postgres`) |
| `SP_DB_DIR` | `.` | Directory for SQLite database file |
| `SP_SERVER_LISTEN` | `:4000` | Server listen address and port |
| `SP_FILESTORAGE_LOCAL_ROOT` | `./data/files` | Where uploaded blobs (org logos, status-page assets, screenshots) are written — must be a mounted volume, see [File Storage](/configuration/file-storage) |

## Docker Compose Example

Create a `docker-compose.yml` file:

```yaml
version: '3.8'

services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: solidping
      POSTGRES_PASSWORD: solidping
      POSTGRES_DB: solidping
    volumes:
      - postgres-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U solidping"]
      interval: 5s
      timeout: 5s
      retries: 5

  solidping:
    image: ghcr.io/fclairamb/solidping:latest
    ports:
      - "4000:4000"
    environment:
      SP_DB_TYPE: postgres
      SP_DB_URL: postgresql://solidping:solidping@postgres:5432/solidping?sslmode=disable
      SP_FILESTORAGE_LOCAL_ROOT: /data/files
    volumes:
      - solidping-files:/data/files
    depends_on:
      postgres:
        condition: service_healthy

volumes:
  postgres-data:
  solidping-files:
```

Start the services:

```bash
docker-compose up -d
```

## Accessing the Dashboard

Once running, access the dashboard at [http://localhost:4000](http://localhost:4000).

**Default credentials:**
- Email: `admin@solidping.io`
- Password: `solidpass`

:::warning First login sets a new password
That password is published in the SolidPing repository, so it buys you exactly
one login. SolidPing lands you on a "set a new password" screen and the account
can do nothing else until you complete it — over the API, every endpoint except
`POST /api/v1/auth/change-password`, `GET /api/v1/auth/me` and
`POST /api/v1/auth/logout` answers `403` with code `PASSWORD_CHANGE_REQUIRED`.

This applies to any fresh database, self-hosted or not. There is no setting
that turns it off.
:::

## Updating

To update to the latest version:

```bash
docker pull ghcr.io/fclairamb/solidping:latest
docker stop solidping
docker rm solidping
# Run the docker run command again
```

Or with Docker Compose:

```bash
docker-compose pull
docker-compose up -d
```

## Health Check

The server exposes a health check endpoint:

```bash
curl http://localhost:4000/api/mgmt/health
```

## Next Steps

- [Configuration Guide](/configuration) - All environment variables
- [File Storage](/configuration/file-storage) - Local volume vs. S3 for uploaded blobs
- [Kubernetes Deployment](/installation/kubernetes) - For orchestrated deployments
