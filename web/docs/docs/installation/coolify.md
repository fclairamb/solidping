---
sidebar_position: 6
title: Coolify
---

# Coolify Installation

[Coolify](https://coolify.io/) is a self-hostable PaaS that deploys apps and services
from a Docker Compose file, with automatic HTTPS and a generated domain per service.
SolidPing isn't in Coolify's one-click catalog yet (the catalog requires 1,000 GitHub
stars on the service's repository), but its "Docker Compose Empty" resource type
deploys any compose file, so the template below works today.

## What you get

One container, SQLite storage, a named volume for the database and uploaded files
(org logos, status-page assets, incident screenshots). No separate database service to
manage — see [Switch to Postgres](#switch-to-postgres) below if you outgrow it.

## Deploy with "Docker Compose Empty"

1. In Coolify, create a new resource and pick **Docker Compose Empty**.
2. Paste in the compose file below. It's kept in the SolidPing repository at
   [`deploy/coolify/solidping.yaml`](https://github.com/fclairamb/solidping/blob/main/deploy/coolify/solidping.yaml) —
   import it from there rather than retyping it, so your deployment can't drift from
   what's tested in CI.

   ```yaml
   # documentation: https://solidping.io/docs/installation/coolify
   # slogan: Uptime monitoring, alerting and status pages, self-hosted in one container.
   # category: monitoring
   # tags: monitoring,uptime,status-page,alerting,ssl,cron
   # logo: svgs/solidping.svg
   # port: 4000

   services:
     solidping:
       image: ghcr.io/fclairamb/solidping:0.31
       environment:
         - SERVICE_URL_SOLIDPING_4000
       volumes:
         - solidping-data:/data
       # ICMP checks need a raw socket; drop this block if you never use ping checks.
       cap_add:
         - NET_RAW
       healthcheck:
         test: ["CMD", "/app/solidping", "healthcheck"]
         interval: 30s
         timeout: 5s
         retries: 3

   volumes:
     solidping-data:
   ```

   :::info Keep this in sync
   This block must match `deploy/coolify/solidping.yaml` in the repository exactly. CI
   validates that file on every change (`docker compose config`); this copy is not
   independently checked, so if you're editing it, edit the source file first.
   :::

3. `SERVICE_URL_SOLIDPING_4000` is a Coolify magic variable, not a SolidPing setting —
   it tells Coolify to generate a domain for the service and route it to port 4000. You
   don't need to fill in anything for it.
4. Set your own domain under the service's **Domains** setting, or keep the generated
   one.
5. Deploy. Coolify pulls the image, starts the container, and waits on the
   `HEALTHCHECK` before marking the service healthy.

## First login

Sign in with the seeded admin account:

| Email | Password | Org |
|---|---|---|
| `admin@solidping.io` | `solidpass` | `default` |

The first successful login forces a password change — SolidPing redirects you to a
change-password screen before anything else in the dashboard is reachable. Pick a new
password there before doing anything else.

## Switch to Postgres

The template uses SQLite so there's nothing else to provision. If you'd rather run
Postgres, add a `postgres` service to the compose file (see the
[Docker Compose](./docker-compose.md) page for a worked example with a healthcheck and
`depends_on`), then set `SP_DB_TYPE: postgres` and `SP_DB_URL` on the `solidping`
service to point at it. The `solidping-data` volume is still needed for uploaded files
even when the database itself is external.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [File Storage](/configuration/file-storage) - Local volume vs. S3 for uploaded blobs
- [Docker Compose](./docker-compose.md) - Multi-container setups, Postgres, Traefik
