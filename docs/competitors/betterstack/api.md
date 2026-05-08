# BetterStack — REST API Reference

Behavior is in [monitoring.md](monitoring.md), [alerting.md](alerting.md), and [platform.md](platform.md). This page is the field-and-endpoint reference.

## Auth, base URL, versioning

- **Auth**: Bearer token. `Authorization: Bearer $TOKEN`.
- **Two token classes**:
  - **Global API tokens** — cross-team. Most write paths require an explicit `team_name` field.
  - **Team-scoped Uptime API tokens** — no `team_name` needed.
- **Base URL**: `https://uptime.betterstack.com/api/`
- **Versioning is mixed per resource**: `v2` for most resources, `v3` for incident operations (acknowledge / resolve / list), `v1` for heartbeat ingest.
- **Spec**: JSON:API style envelope (`data`, `included`, `relationships`, `attributes`).
- **CORS**: not documented; the API is server-to-server only by design.
- **Errors**: standard HTTP status codes. Body shape is not formally documented — empirically `{"errors": [{"detail": "..."}]}`.

### HTTP status codes

| Code | Meaning |
|---|---|
| 200 | OK (GET) |
| 201 | Created (POST) |
| 204 | No Content (DELETE) |
| 400 | Bad Request (e.g. invalid dates) |
| 401 | Missing/invalid token |
| 404 | Resource not found |
| 422 | Validation errors |

## Pagination — quirky

- **Default**: `?per_page=50`, max **250**. `?page=` starts at **1**. Offset-based; no cursor pagination.
- Response embeds `pagination.first | last | prev | next` as full URLs (`first`/`last` are absolute).
- **The incidents endpoint is special-cased**: defaults to **10/page**, max **50/page**. No public reason given.

## Rate limits — undocumented

The "Getting started with the API" page does not specify quotas. No `X-RateLimit-*` header is documented. Expect to discover them empirically.

## Monitor types (the `monitor_type` enum)

| Value | Purpose |
|---|---|
| `status` | 2XX HTTP status (default) |
| `expected_status_code` | Specific HTTP status codes |
| `keyword` | Verify text presence |
| `keyword_absence` | Verify text NOT in response |
| `ping` | ICMP ping |
| `tcp` | TCP port |
| `udp` | UDP port |
| `smtp` | SMTP server |
| `pop` | POP3 server |
| `imap` | IMAP server |
| `dns` | DNS query |
| `playwright` | Browser-based scenario |

## Endpoint summary

| Endpoint | Method | Purpose |
|---|---|---|
| **Monitors** | | |
| `/api/v2/monitors` | GET | List (filter by `url`, `pronounceable_name`) |
| `/api/v2/monitors` | POST | Create |
| `/api/v2/monitors/{id}` | GET | Read |
| `/api/v2/monitors/{id}` | PATCH | Partial update |
| `/api/v2/monitors/{id}` | DELETE | Delete |
| `/api/v2/monitors/{id}/response-times` | GET | Time series, grouped by region |
| `/api/v2/monitors/{id}/sla` | GET | Availability summary (`from`, `to` dates) |
| **Monitor Groups** | | |
| `/api/v2/monitor-groups` | GET / POST | List / create |
| `/api/v2/monitor-groups/{id}` | GET / PATCH / DELETE | CRUD |
| **Heartbeats — management** | | |
| `/api/v2/heartbeats` | GET / POST | List / create |
| `/api/v2/heartbeats/{id}` | GET / PATCH / DELETE | CRUD |
| `/api/v2/heartbeats/{id}/availability` | GET | Availability summary |
| **Heartbeats — ingest (v1!)** | | |
| `/api/v1/heartbeat/{token}` | GET/POST | Success ping |
| `/api/v1/heartbeat/{token}/fail` | GET/POST | Failure ping |
| `/api/v1/heartbeat/{token}/{exit_code}` | GET/POST | Exit-code-coded ping |
| **Incidents (v3 for operations!)** | | |
| `/api/v3/incidents` | GET | List (filter by monitor) |
| `/api/v3/incidents/{id}` | GET | Read |
| `/api/v3/incidents/{id}/acknowledge` | POST | Acknowledge |
| `/api/v3/incidents/{id}/resolve` | POST | Resolve |
| **Status Pages** | | |
| `/api/v2/status-pages` | GET / POST | List / create |
| `/api/v2/status-pages/{id}/reports` | GET | Reports |
| `/api/v2/status-updates` | POST | Create status update |
| `/api/v2/status-updates/{id}` | DELETE | Delete |
| **Escalation policies** | | |
| `/api/v2/policies` | GET / POST | List / create (full CRUD) |
| **On-call calendars** | | |
| `/api/v2/on-calls` | GET | List schedules |
| `/api/v2/on-calls/{id}/events` | POST | Create override events |

## Monitor — request fields

Grouped by purpose. All fields except the three required (`monitor_type`, `url`, `pronounceable_name`) are optional.

### Required
- `monitor_type` — see enum above
- `url` — target host or URL
- `pronounceable_name` — used for voice-call TTS

### Detection / behavior
- `check_frequency` — seconds between checks (default 30)
- `request_timeout` — **seconds for HTTP/DNS, milliseconds for TCP/UDP/SMTP/POP/IMAP, discrete-seconds for Playwright** (footgun)
- `confirmation_period` — seconds before opening an incident
- `recovery_period` — seconds monitor must stay up to auto-resolve (flap resets)
- `verify_ssl` — boolean
- `follow_redirects` — boolean
- `ip_version` — `ipv4` or `ipv6` (added 2024)
- `regions` — subset of `["us", "eu", "as", "au"]`

### HTTP-specific
- `http_method` — `GET`, `HEAD`, `POST`, `PUT`, `PATCH`
- `request_headers` — `[{ name, value }]`
- `request_body` — POST/PUT/PATCH payload; *required* for DNS monitors (target domain)
- `expected_status_codes` — array
- `required_keyword` — for `keyword` / `keyword_absence` types

### Protocol-specific
- `port` — required for TCP/UDP/SMTP/POP/IMAP
- `playwright_script` — JS source for `playwright` type
- `environment_variables` — encrypted env-var map for Playwright

### Alerting (legacy dual-surface — see [alerting.md](alerting.md#quick-monitor-level-shortcuts-legacy-dual-surface))
- `email`, `sms`, `call`, `push`, `critical_alert` — booleans
- `policy_id` — escalation policy ID (overrides booleans for routing)
- `expiration_policy_id` — separate policy for SSL/domain expiration alerts
- `team_wait` — seconds before escalating to entire team

### Expiration
- `ssl_expiration` — days advance notice (1–60, or null)
- `domain_expiration` — days advance notice (1–60, or null)

### Maintenance windows
- `maintenance_days` — `["mon"..."sun"]`
- `maintenance_from`, `maintenance_to` — `HH:MM:SS`
- `maintenance_timezone` — defaults UTC

### Organization
- `monitor_group_id` — parent group
- `team_name` — required for global API tokens

## Monitor — response fields

Includes all the request fields plus:

| Field | Type | Description |
|---|---|---|
| `id` | String | Monitor ID (string-of-number) |
| `type` | String | Always `monitor` |
| `status` | String | `up` · `down` · `validating` · `paused` · `pending` · `maintenance` |
| `last_checked_at` | ISO 8601 | Most recent check timestamp |
| `paused_at` | ISO 8601 | Null if active |
| `created_at` / `updated_at` | ISO 8601 | Lifecycle |

## Heartbeat — fields

Request:
- `name` (required), `period` (≥30 s), `grace`
- `call`, `sms`, `email`, `push`, `critical_alert`
- `team_wait`, `heartbeat_group_id`, `policy_id`
- `paused`, `maintenance_*`
- `team_name` (when global token)

Response adds: `id`, `type: "heartbeat"`, `url` (the ping URL with embedded token), `status` (`paused` · `pending` · `up` · `down`), `created_at`, `updated_at`, `paused_at`, `sort_index`.

## Monitor group — fields

`id`, `type: "monitor_group"`, `name`, `sort_index` (nullable), `paused`, `team_name`, `created_at`, `updated_at`. Bulk operations apply to all monitors in the group.

## Availability/SLA response

```json
{
  "availability": "99.98%",
  "total_downtime": 1234,
  "number_of_incidents": 5,
  "longest_incident": 600,
  "average_incident": 247
}
```

All durations in seconds. Query params: `from`, `to` (`YYYY-MM-DD`); omit both for "since creation".

## Pagination response shape

```json
{
  "data": [...],
  "pagination": {
    "first": "https://uptime.betterstack.com/api/v2/monitors?page=1",
    "last":  "https://uptime.betterstack.com/api/v2/monitors?page=5",
    "prev":  null,
    "next":  "https://uptime.betterstack.com/api/v2/monitors?page=2"
  }
}
```

## Common query parameters

| Param | Endpoints | Purpose |
|---|---|---|
| `url` | List monitors | Filter by URL |
| `pronounceable_name` | List monitors | Filter by name |
| `from` / `to` | Availability endpoints | Date range (`YYYY-MM-DD`) |
| `page` | All list endpoints | Page number (offset) |
| `per_page` | All list endpoints | 50 default, 250 max (10/50 for incidents) |

## Sources

- https://betterstack.com/docs/uptime/api/
- https://betterstack.com/docs/uptime/api/getting-started-with-uptime-api/
- https://betterstack.com/docs/uptime/api/pagination/
- https://betterstack.com/docs/uptime/api/list-all-existing-monitors/
- https://betterstack.com/docs/uptime/api/get-a-single-monitor/
- https://betterstack.com/docs/uptime/api/create-a-new-monitor/
- https://betterstack.com/docs/uptime/api/update-an-existing-monitor/
- https://betterstack.com/docs/uptime/api/delete-an-existing-monitor/
- https://betterstack.com/docs/uptime/api/get-monitors-response-times/
- https://betterstack.com/docs/uptime/api/get-a-monitors-availability-summary/
- https://betterstack.com/docs/uptime/api/monitors-api-response-params/
- https://betterstack.com/docs/uptime/api/list-all-existing-hearbeats/
- https://betterstack.com/docs/uptime/api/create-a-hearbeat/
- https://betterstack.com/docs/uptime/api/get-a-heartbeats-availability-summary/
- https://betterstack.com/docs/uptime/api/list-all-incidents/
- https://betterstack.com/docs/uptime/api/list-all-existing-status-pages/
