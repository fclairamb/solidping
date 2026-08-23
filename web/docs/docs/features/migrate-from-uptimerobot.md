---
sidebar_position: 17
title: Migrate from UptimeRobot
description: Import your UptimeRobot monitors into SolidPing via the API v2 getMonitors response.
---

# Migrate from UptimeRobot

SolidPing imports the JSON returned by
[UptimeRobot's API v2 `getMonitors`](https://uptimerobot.com/api/) endpoint: every
entry of `monitors` becomes a check.

## Fetch your monitors

Create a **read-only** API key in UptimeRobot (**My Settings → API Settings → Add
Read-Only API Key**), then fetch every monitor:

```bash
curl -s -X POST https://api.uptimerobot.com/v2/getMonitors \
  -d "api_key=$UPTIMEROBOT_API_KEY" \
  -d "format=json" \
  -d "alert_contacts=1" \
  -d "mwindows=1" \
  -d "custom_http_statuses=1" \
  -d "custom_http_headers=1" \
  > uptimerobot-monitors.json
```

The `alert_contacts`, `mwindows`, `custom_http_statuses` and `custom_http_headers`
flags are optional — the importer surfaces those settings as warnings when
present, so including them just makes the warnings more complete. If your account
has more monitors than fit in one page, repeat the call with `offset` and
concatenate the responses into a JSON array — the importer accepts that shape too
(see below).

## Import it

1. Open **Checks** in the dashboard and click **Import**.
2. Pick **UptimeRobot (getMonitors JSON)** as the source.
3. Paste the JSON, or click **Upload a file**.
4. Click **Import preview** — nothing is written yet.
5. Review the warnings, then confirm.

Via the API:

```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  --data-binary @uptimerobot-monitors.json \
  'https://your-instance/api/v1/orgs/myorg/checks/import/convert?source=uptimerobot&dryRun=true' | jq '.'
```

Drop `&dryRun=true` to apply. The endpoint requires an **organization admin** token.

### Accepted input shapes

The importer accepts whatever your export produced:

- the raw response object — `{"stat": "ok", "pagination": {...}, "monitors": [...]}`;
- a bare `monitors` array — just the list itself, no wrapper;
- an array of page objects — concatenate multiple paginated `getMonitors` responses
  into a single JSON array and paste that.

## What maps

| UptimeRobot monitor type | SolidPing |
|---|---|
| HTTP(s) | `http` |
| Keyword | `http`, body must contain the keyword (keyword type "not exists" → must not contain) |
| Ping | `icmp` |
| Port | `tcp` — well-known sub-types (HTTP, HTTPS, FTP, SMTP, POP3, IMAP) resolve to their standard port; a custom port monitor uses its configured port |
| Heartbeat | `heartbeat` |

| UptimeRobot field | SolidPing |
|---|---|
| `friendly_name` | check name (the slug is derived from it) |
| `interval` | check period |
| `timeout` | checker timeout |
| `status: 0` (paused) | disabled check |
| `http_auth_type` basic + `http_username`/`http_password` | HTTP basic-auth credential |

:::info Basic auth is imported, unlike SolidPing's other importers
Every other SolidPing importer deliberately drops credentials found in a foreign
export, so you re-enter them by hand. UptimeRobot is the one exception: a
**basic** HTTP auth credential is carried straight into the check's encrypted
credential field. **Digest** auth is not imported — re-enter it on the check.
:::

:::warning Heartbeat monitors get a new push URL
An UptimeRobot Heartbeat monitor becomes a SolidPing heartbeat check with a
**new** ping URL. Open the imported check, copy its URL, and repoint whatever job
or agent pushes to it.
:::

## What does not map

Reported as warnings on the import preview:

- **Alert contacts and maintenance windows** — not imported. Wire up SolidPing
  [integrations](/docs/features/incidents) and maintenance windows instead.
- **Custom HTTP status rules** — the check uses the default 2xx expectation.
- **Custom HTTP headers** — not imported.
- **Public Status Page (PSP) settings** — SolidPing has its own
  [status pages](/docs/features/status-pages); recreate them there.
- **Monitor types with no SolidPing counterpart** — skipped, with a warning naming
  the type.
- **Status history** — monitoring results are not portable between tools.

## After the import

Checks created this way carry the label `solidping.io/managed=uptimerobot`, so you
can filter on them and re-run the import while both tools run side by side.
