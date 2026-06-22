# Hyperping — API, Tooling, Pricing

## API surface

- **Base URL**: `https://api.hyperping.io`
- **Auth**: Bearer token. Header: `Authorization: Bearer YOUR_API_KEY`.
- **Token model**: project-scoped tokens with two perm levels — Read+Write or Read-only. Managed in Project Settings → Developers.
- **Errors**: 401 (invalid/missing key), 429 (rate limit). JSON shape: `{ "error": "..." }`.

### Versioning — mixed per resource

Hyperping versions individual resources rather than the whole API:

- `/v1/monitors`, `/v1/maintenance-windows`
- `/v2/outages`, `/v2/healthchecks`, `/v2/reporting/...`
- `/v3/incidents`

This signals incremental rewrites of subsystems. **Pragmatic and worth considering**: lets you evolve `/incidents` to v3 without forcing a breaking change on `/monitors`.

### Pagination

**Not documented** in the public API overview. List endpoints "do not require parameters" per the docs — likely all-at-once or undocumented internal pagination. This is a real gap and a place SolidPing already does better (we use cursor pagination with documented `limit` and `cursor` query params).

### CRUD coverage

- `/v1/monitors` — full CRUD.
- `/v2/healthchecks` — full CRUD plus pause/resume.
- `/v3/incidents` — full CRUD with localized fields.
- `/v2/outages` — list, get, manual create/delete, plus the verb endpoints `/acknowledge`, `/unacknowledge`, `/resolve`, `/escalate`. Verb endpoints are nice for token scoping (you can grant "ack-only" without exposing the full PATCH surface).
- `/v1/maintenance-windows` — full CRUD plus `/complete`.
- `/v2/reporting/...` — read-only reports.

### Outgoing webhooks

Yes — events `check.down`, `check.up`. Payload includes the full multi-region confirmation trace:

```json
{
  "event": "check.down",
  "check": {
    "monitorUuid": "mon_…",
    "url": "…",
    "status": 502,
    "down": true,
    "date": 1556506024291,
    "downtime": 1
  },
  "pings": [
    { "original": true,  "location": "london",    "status": 502, "statusMessage": "Bad Gateway" },
    { "original": false, "location": "paris",     "status": 502, "statusMessage": "Bad Gateway" },
    { "original": false, "location": "frankfurt", "status": 502, "statusMessage": "Bad Gateway" }
  ]
}
```

The `pings` array with `original: true/false` is the most valuable part — receivers can build their own correlation logic on top.

### Incoming webhooks

Only via the Healthchecks endpoint (heartbeat ingestion). No documented "create-an-incident-from-incoming-alertmanager" endpoint analogous to PagerDuty Events API v2.

## Terraform / config-as-code

**No first-party Terraform provider.** The community ecosystem is maintained by Develeap:

- **Terraform**: `develeap/terraform-provider-hyperping` on the Terraform Registry. Resources: monitors, status pages, incidents, healthchecks, maintenance windows.
- **Go SDK**: `develeap/hyperping-go` — shared API client with rate limiting, retries, error handling.
- **Python SDK**: `develeap/hyperping-python` (`pip install hyperping`). Sync + async, Pydantic v2 models.
- **Prometheus exporter**: `develeap/hyperping-exporter` on Docker Hub. Surfaces uptime/response-time as Prometheus metrics → Grafana.

No documented YAML import, GitHub Action, or first-party config-bundle export.

## Pricing snapshot (May 2026)

| Tier | Price/mo (annual) | Monitors | Min interval | Seats | Status pages | Browser checks | Notable |
|---|---|---|---|---|---|---|---|
| Free | $0 | 20 | 5 min | 1 | 1 (basic) | 0 | HTTP/Port/Ping/Keyword only; no on-call |
| Essentials | $24 | 50 | 30 s | 2 | 1 | 3 | DNS, all integrations, on-call & escalation |
| Pro | $74 | 100 | 30 s | 5 | 3 | 10 | Phone-call alerts, 1k status-page subscribers |
| Business | $249 | 1000 | 20 s | 15 (+$12/seat) | 10 | 25 | Audit logs, white-label, custom email domain, SAML SSO, IP allowlist |
| Enterprise | custom | — | 10 s | — | — | — | Per the API enum allowing 10-s frequency |

### Plan gates
- On-call & escalation: Essentials+
- Phone-call alerts: Pro+
- DNS monitoring: Essentials+
- Custom domain status page: Essentials+

## Sources

- https://hyperping.com/docs/api/overview
- https://hyperping.com/docs/api/monitors
- https://hyperping.com/docs/api/incidents
- https://hyperping.com/docs/api/outages
- https://hyperping.com/docs/api/maintenance
- https://hyperping.com/docs/api/healthchecks
- https://hyperping.com/docs/api/reports
- https://hyperping.com/docs/integrations/webhooks
- https://hyperping.com/blog/hyperping-community-developer-tools
- https://hyperping.com/pricing
