---
sidebar_position: 27
title: Run now
---

# Run now

After you fix an outage you do not have to wait for the next period. "Run now"
runs a check once, right away, in every region.

## From the dashboard

Open the check and click **Run now** in the page header. The button shows a
spinner until a fresh result has arrived for each region, then a toast shows the
new status. If nothing arrives within 2 minutes it tells you it is still
waiting. Viewers do not see the button.

## From the API

```bash
curl -X POST -H "Authorization: Bearer $TOKEN" \
  https://your-host/api/v1/orgs/default/checks/$CHECK_UID/run-now
```

```json
{
  "requestedAt": "2026-10-04T10:15:00.123456Z",
  "regions": [
    { "region": "eu", "status": "queued" },
    { "region": "us", "status": "running" }
  ]
}
```

- `queued`: the region's run starts now.
- `running`: the region is already running the check (or a multi-step run is in
  progress). That result answers your request.

A result with a `periodStart` at or after `requestedAt` is the answer.

## What it does to incidents

The result is a normal result. It can open or resolve an incident like any
scheduled one, and confirmation and recovery periods still apply.

The next scheduled run keeps its regular period.

## Limits

| Check type | Per check | Per organization |
|---|---|---|
| Most types | 3 per minute | 60 per hour |
| `browser`, `js`, `rdp`, `vnc` | 1 per minute | 20 per hour |

A request that queues nothing (every region is already running) answers `200`
and spends no budget.

A refused request answers `429` with a `Retry-After` header. A disabled check
answers `409`.

## From MCP

The `run_check` tool takes a check UID or slug and returns the same response.
It needs the `mcp` scope (not `mcp:read`).

A newly created check already runs at once, with no request needed.
