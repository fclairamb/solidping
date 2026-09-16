---
sidebar_position: 3
title: Migrate from Uptime Kuma
description: Import your Uptime Kuma 2.x SQLite database or 1.x backup JSON into SolidPing — every monitor type, groups included.
---

# Migrate from Uptime Kuma

SolidPing imports from [Uptime Kuma](https://github.com/louislam/uptime-kuma): every
monitor becomes a check, and Kuma's monitor groups become SolidPing check groups.

## Uptime Kuma 2.x (current) — the `sp` CLI

Kuma 2.0 removed the JSON backup export that older guides for this page relied on,
so this is now the primary path: the [`sp` CLI](/docs/cli) reads your Kuma SQLite
database (`kuma.db`) directly, on your own machine, and sends only the handful of
fields each monitor needs. **The database file never leaves your machine** — `sp`
opens it read-only and reads exactly three small tables (`monitor`, `tag`,
`monitor_tag`), never the `heartbeat`/`stat_*` history, and never the `notification`
or `user` tables, which is where Kuma keeps integration tokens and password hashes.
That matters because `kuma.db` is usually large (the heartbeat history dominates it —
gigabytes are normal) and holds secrets no migration tool should ever ask you to
upload.

### Find `kuma.db`

| Install | Location |
|---|---|
| Bare metal | `data/kuma.db` under Kuma's working directory |
| Docker | Inside the named volume mounted at `/app/data` — copy it out with `docker cp <container>:/app/data/kuma.db .` |

Kuma runs in WAL mode, so a live instance may also have `kuma.db-wal` and
`kuma.db-shm` sitting next to `kuma.db`. **Copy all three files together** (or stop
Kuma first) — without the `-wal` file, monitors created since Kuma's last WAL
checkpoint would be silently missing.

### Convert

```bash
sp checks import --from uptime-kuma-db ./kuma.db            # preview only
sp checks import --from uptime-kuma-db ./kuma.db --apply    # create/update
```

Preview is the default — review the created/updated counts and the warning list,
then re-run with `--apply` once it looks right. See [the CLI docs](/docs/cli) for
the full `--from` reference.

:::info Kuma on MariaDB
Kuma 2.x can run against MariaDB/MySQL instead of SQLite. There is no `kuma.db` in
that setup, so this importer does not apply — it reads a SQLite file specifically.
:::

## Uptime Kuma 1.x — the backup JSON

If you are still on 1.x, export from **Profile → Settings → Backup → Export** and
either upload it in the dashboard or post it directly:

1. Open **Checks** in the dashboard and click **Import**.
2. Pick **Uptime Kuma (backup JSON)** as the source.
3. Paste the JSON, or click **Upload a file**.
4. Click **Import preview** — nothing is written yet.
5. Review the warnings, then confirm.

Via the API:

```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data-binary @uptime-kuma-backup.json \
  'https://your-instance/api/v1/orgs/myorg/checks/import/convert?source=uptime-kuma&dryRun=true' | jq '.'
```

Drop `&dryRun=true` to apply. The endpoint requires an **organization admin** token.
This is the same endpoint the CLI's `--from uptime-kuma-db` posts the SQLite-derived
JSON to — one converter, two ways to produce the input.

## What maps

| Uptime Kuma type | SolidPing |
|---|---|
| `http` | `http` |
| `keyword` | `http`, body must contain the keyword (inverted → must not contain) |
| `json-query` | `http` with a JSONPath assertion |
| `port` | `tcp` |
| `ping` | `icmp` |
| `dns` | `dns` |
| `docker` | `docker` |
| `push` | `heartbeat` |
| `grpc-keyword` | `grpc` |
| `mqtt` | `mqtt` |
| `postgres` | `postgresql` |
| `mysql` | `mysql` |
| `redis` | `redis` |
| `sqlserver` | `mssql` |
| `mongodb` | `mongodb` |
| `steam` | `a2s` (Source-engine game servers) |
| `real-browser` | `browser` |
| `group` | a SolidPing **check group** — its children are assigned to it |

| Uptime Kuma field | SolidPing |
|---|---|
| `name`, `description` | check name (the slug is derived from it), description |
| `active` | enabled flag |
| `interval` | check period |
| `maxretries` × `retryInterval` | the incident confirmation period |
| `timeout` | checker timeout |
| `accepted_statuscodes` | expected status codes (`200-299` becomes `2XX`) |
| `headers` | HTTP headers |
| `databaseConnectionString` | host, port, username and database name |
| `dns_resolve_server`, `dns_resolve_type` | nameserver and record type |

:::warning Push monitors get new URLs
A Kuma `push` monitor becomes a SolidPing heartbeat check with a **new** ping URL.
Open each imported heartbeat check, copy its URL, and repoint the job that pushes
to it.
:::

## What does not map

Reported as warnings on the import preview:

- **Passwords** — database, MQTT and basic-auth credentials are deliberately
  never imported. SolidPing has fields for all of them, but an import must not
  silently re-persist secrets copied out of a backup file. Re-enter them on the
  check.
- **Notifications** — Kuma notification bindings are not imported. Wire up SolidPing
  [integrations](/docs/features/incidents) instead.
- **Tags** — add SolidPing labels manually.
- **`ignoreTls`** — SolidPing verifies certificates.
- **`upsideDown`** — no equivalent; the check reports normally.
- **`gamedig`, `radius`, `tailscale-ping` monitors** — no SolidPing counterpart yet;
  they are skipped.
- **The Docker host** — imported Docker checks default to the worker's local Docker
  socket. Set a custom host on the check if yours differs.
- **Status history** — monitoring results are not portable between tools.

## After the import

Checks created this way carry the label `solidping-managed=uptime-kuma`, so you
can filter on them and re-run the import while both tools run side by side.
