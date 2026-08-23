---
sidebar_position: 3
title: Migrate from Uptime Kuma
description: Import your Uptime Kuma backup JSON into SolidPing — every monitor type, groups included.
---

# Migrate from Uptime Kuma

SolidPing imports an [Uptime Kuma](https://github.com/louislam/uptime-kuma) backup
file: every entry of `monitorList` becomes a check, and Kuma's monitor groups become
SolidPing check groups.

## Where to find your backup

In Uptime Kuma **1.x**:

1. Open **Profile → Settings → Backup**.
2. Click **Export** and save the JSON file.

:::warning Uptime Kuma 2.x removed the JSON backup export
2.x dropped the Settings → Backup screen. If you are on 2.x, export from a 1.x
instance (or a 1.x snapshot of your database) — the importer needs the
`monitorList` array that the 1.x backup produces. It rejects a file without one
and tells you so.
:::

## Import it

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

Checks created this way carry the label `solidping.io/managed=uptime-kuma`, so you
can filter on them and re-run the import while both tools run side by side.
