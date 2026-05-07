# BetterStack — Heartbeats, Playwright, Status Pages, Maintenance

Behavior of the non-monitor surfaces. The API CRUD shapes for these resources are in [api.md](api.md); this page is about how they actually work.

## Heartbeats

Endpoint root: `https://uptime.betterstack.com/api/v1/heartbeat/<TOKEN>` (note: **v1**, distinct from the v2 management API).

### Configuration
- `period` (integer, seconds) — minimum **30 seconds**.
- `grace` (integer, seconds) — minimum 0; doc recommends "approx. 20% of period."

### Alerting trigger
Alert fires when `now - last_heartbeat > period + grace`.

### Pending state
A freshly created heartbeat is `pending` until the first ping. The clock starts only with the first received heartbeat — there is no "you have N seconds from creation."

### Endpoints

| Endpoint | Purpose |
|---|---|
| `POST/GET /api/v1/heartbeat/<TOKEN>` | Success ping |
| `POST /api/v1/heartbeat/<TOKEN>/fail` | Explicit failure ping |
| `POST /api/v1/heartbeat/<TOKEN>/<exit_code>` | Pass exit code as path segment; non-zero counts as failure |

Idiomatic curl (worth borrowing):

```bash
output=$(./my-job 2>&1)
curl -d "$output" "https://uptime.betterstack.com/api/v1/heartbeat/<TOKEN>/$?"
```

The body of any heartbeat ping is captured and shown on the incident detail page as the cause.

### What's missing
**No `/start` endpoint.** Heartbeats only mark task *completion*, not task *entry*. SolidPing should ship `/start` — see [Hyperping platform](../hyperping/platform.md#start-endpoint) for the prior art and the synthesis doc.

## Playwright monitors

### Configuration
- `monitor_type: "playwright"`
- `playwright_script` — raw JS source
- `scenario_name`
- `environment_variables` — `Map of String, Sensitive` in Terraform; stored encrypted at the BetterStack side, accessed via `process.env.NAME` in the script

### Framework
Official `@playwright/test` API (`test`, `expect`, `page` fixture). Browser is implicitly **Chromium only** (marketing says "a real Chrome browser instance"); no browser-selector field exposed.

### Execution
BetterStack-side managed runners. Scripts are stateless ("Playwright monitors are stateless"); persistence requires the separate Telemetry/Warehouse product.

### Timeout — three units, same field name
This is a known footgun in BetterStack's API:

| Monitor type | `request_timeout` unit | Range |
|---|---|---|
| HTTP / status / keyword / DNS / etc. | seconds | 2–60 |
| TCP / UDP / SMTP / POP / IMAP | **milliseconds** | 500–5000 |
| Playwright | seconds, **discrete values only** | 15, 30, 45, 60, 120, 180, 240, 300, 360, 480, 600, 900 |

**Don't replicate this.** SolidPing should use `timeoutSeconds` everywhere or split into two clearly-named fields.

### Failure artifacts
HTTP monitors capture **screenshots** when the response body is non-blank. Playwright monitors show **step-by-step screenshots** of each `page.*` call (step timeline) in the dashboard. Not exposed in the API.

No video capture is documented — Hyperping does video, SolidPing's browser monitoring already does both.

### Versioning
No git-versioned scripts; the JS lives as a string in the monitor record (or via Terraform). Migrating across runtime versions is manual.

## Status pages

### Subscriber model

- **Per-page master switch**: `subscribable` (boolean) on the status page resource.
- **Subscriber types**: email, outgoing webhook, RSS, JSON API endpoint. **No native SMS or Slack subscription** despite SMS being a paid alerting channel for the team itself.
- **Verification**: email and webhook subscriptions both require click-to-confirm via emailed link. RSS needs no verification (no PII). Every email contains an RFC 8058-style one-click unsubscribe link.
- **Granularity**: subscribers can pick the whole page or **specific components**. Component-level filtering does *not* apply to RSS — RSS gets all updates.

### Reports
`automatic_reports` (boolean) — auto-generated weekly/monthly summary reports emailed to subscribers. Enterprise tickbox, not finely configurable.

### Access control
- `password_enabled` / `password` — public password gate
- `require_sso` — SSO-gated private pages
- `ip_allowlist` — CIDR-supported, **billable**

### Modernization (2024-2025)
- `design: v2` with `theme` (light/dark), `layout` (vertical/horizontal), `navigation_links`
- `require_sso` field added

## Maintenance windows

Two distinct surfaces, with different semantics behind the same field names. **This is messy in BetterStack — do better in SolidPing.**

### Per-monitor recurring window

Fields on the monitor:
- `maintenance_days` — `["mon"..."sun"]` subset
- `maintenance_from` (`HH:MM:SS`)
- `maintenance_to` (`HH:MM:SS`)
- `maintenance_timezone` (defaults UTC)

Recurrence is **weekday-time only** — no calendar-date one-offs, no monthly/quarterly, no Nth-of-month patterns.

### Per-monitor vs per-heartbeat semantics

- **Monitor maintenance window**: "We won't check your website during this window." Checks are **paused** (network call skipped entirely). Status badge shows `maintenance`.
- **Heartbeat maintenance window**: "We won't create incidents during this window." Heartbeats are still expected, but missed ones don't page.

**Same field names, two different behaviors.** SolidPing should pick one explicit semantic — recommendation in the synthesis doc is **alert-suppression-only** (keep checking, mark results as expected-failure) for both.

### Team-wide maintenance

UI-only feature: "All monitors, heartbeats, and integrations will be temporarily or indefinitely paused, and any incidents occurring within the timeframe will be ignored." Admin-only; no API, no Terraform resource.

### Pausing — third axis

`paused: true` stops checks indefinitely. Differs from maintenance:
- Paused monitors don't show the `maintenance` status badge
- Paused monitors aren't surfaced to status-page subscribers as scheduled

So BetterStack effectively has three off-switches: paused, monitor-maintenance, team-wide-maintenance. All disjoint, none cleanly composable.

### Webhook events
Outgoing webhooks support `maintenance_started` and `maintenance_completed` (alongside `created` / `updated`).

## Sources

- https://betterstack.com/docs/uptime/cron-and-heartbeat-monitor/
- https://betterstack.com/docs/uptime/api/heartbeats-api-response-params/
- https://betterstack.com/docs/uptime/playwright-monitor/
- https://betterstack.com/docs/uptime/incident-details/
- https://betterstack.com/docs/uptime/subscribing-to-status-updates/
- https://betterstack.com/docs/uptime/status-pages/subscribing-to-status-updates/subscribing-to-rss/
- https://betterstack.com/docs/uptime/status-pages/subscribing-to-status-updates/subscribing-with-webhooks/
- https://betterstack.com/docs/uptime/team-wide-maintenance/
- https://betterstack.com/docs/uptime/pausing-monitors-and-maintenances/
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_monitor.md
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_heartbeat.md
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_status_page.md
