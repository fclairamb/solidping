---
sidebar_position: 10
title: Fly.io
---

# Fly.io Installation

[Fly.io](https://fly.io/) runs Docker images as Machines with a generated HTTPS domain and
persistent volumes. SolidPing runs there as one Machine from the published image, with SQLite
on a volume.

## What you get

One Machine, SQLite storage, a volume at `/data` for the database and uploaded files (org
logos, status-page assets, incident screenshots). See [Switch to Postgres](#switch-to-postgres)
to use Postgres instead.

## Deploy

You need the [flyctl CLI](https://fly.io/docs/flyctl/install/) and a Fly account.

```bash
git clone https://github.com/fclairamb/solidping.git
cd solidping/deploy/fly/server
```

Edit `fly.toml`: set `app` to a unique name and `SP_BASE_URL` to `https://<app>.fly.dev` (or
your own domain). Then:

```bash
fly apps create my-solidping
fly volumes create solidping_data --app my-solidping --region cdg --size 1
fly deploy
```

The volume must be in the same region as the Machine (`primary_region` in `fly.toml`).

Why a `Dockerfile`: the published image runs as an unprivileged user (uid 65532). The
two-line `deploy/fly/server/Dockerfile` switches to root so the process can write the volume at
`/data`, the same reason Railway needs `RAILWAY_RUN_UID=0`.

`fly.toml` keeps one Machine always running (`auto_stop_machines = "off"`). A monitoring tool
that is stopped when idle does not run its checks. Fly sets no `PORT`: SolidPing listens on
`:4000`, which is `internal_port`. The health check uses `/api/mgmt/health`.

A volume lives on one host and is not replicated, so keep a single Machine.

## First login

Sign in with the seeded admin account:

| Email | Password | Org |
|---|---|---|
| `admin@solidping.io` | `solidpass` | `default` |

The first login forces a password change before anything else in the dashboard is
reachable.

## Switch to Postgres

Set the database on the app, with any Postgres you run (Fly's or an external one):

```bash
fly secrets set --app my-solidping \
  SP_DB_TYPE=postgres \
  SP_DB_URL='postgresql://user:password@host:5432/solidping?sslmode=require'
```

Keep the `/data` volume: uploaded files are still stored there.

## Upgrading

Change the tag in `deploy/fly/server/Dockerfile` (`0.38` follows patch releases of 0.38) and run
`fly deploy`. Database migrations run on startup.

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [File Storage](/configuration/file-storage) - Local volume vs. S3 for uploaded blobs
