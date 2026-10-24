---
sidebar_position: 9
title: Railway
---

# Railway Installation

[Railway](https://railway.com/) runs Docker images with a generated HTTPS domain, persistent
volumes and managed Postgres. SolidPing runs there as one service from the published image,
with SQLite on a volume.

## What you get

One service, SQLite storage, a volume at `/data` for the database and uploaded files (org
logos, status-page assets, incident screenshots). See [Switch to Postgres](#switch-to-postgres)
to use Railway's managed Postgres instead.

## Deploy from the dashboard

1. Create a new project, then **Deploy a Docker Image** with
   `ghcr.io/fclairamb/solidping:0.38`.
2. Right-click the service, **Attach volume**, and mount it at `/data`.
3. In **Variables**, add:

   | Variable | Value |
   |---|---|
   | `RAILWAY_RUN_UID` | `0` |
   | `SP_BASE_URL` | `https://${{RAILWAY_PUBLIC_DOMAIN}}` |

4. In **Settings > Networking**, click **Generate Domain** (or add your own).
5. In **Settings > Deploy**, set the healthcheck path to `/api/mgmt/health`.
6. Deploy.

`RAILWAY_RUN_UID=0` is required. Railway mounts volumes as root, and the image runs as an
unprivileged user that cannot write the SQLite file otherwise.

There is no port to configure. Railway sets `PORT` and SolidPing listens on it.

## Deploy from the CLI

The same setup is kept as Railway Infrastructure as Code in
[`deploy/railway/.railway/railway.ts`](https://github.com/fclairamb/solidping/blob/main/deploy/railway/.railway/railway.ts).
It needs the [Railway CLI](https://docs.railway.com/cli) 5.42.1 or newer and Node.js 22.

```bash
git clone https://github.com/fclairamb/solidping.git
cd solidping/deploy/railway
npm install railway
railway login
railway config plan
railway config apply
railway domain
```

`railway config plan` asks which project and environment to target and shows what it will
create. `railway domain` generates the public domain, which the file cannot declare.

## First login

Sign in with the seeded admin account:

| Email | Password | Org |
|---|---|---|
| `admin@solidping.io` | `solidpass` | `default` |

The first login forces a password change before anything else in the dashboard is
reachable.

## Switch to Postgres

Add a Postgres database to the project, then set these variables on the `solidping` service:

| Variable | Value |
|---|---|
| `SP_DB_TYPE` | `postgres` |
| `SP_DB_URL` | `${{Postgres.DATABASE_URL}}` |

Keep the `/data` volume: uploaded files are still stored there. In
`deploy/railway/.railway/railway.ts`, the same change is a `postgres("postgres")` resource
and `SP_DB_URL: db.env.DATABASE_URL` in the service `env`.

## Upgrading

Change the image tag (`0.38` follows patch releases of 0.38) and redeploy. Database
migrations run on startup.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [File Storage](/configuration/file-storage) - Local volume vs. S3 for uploaded blobs
