# Hyperping — Competitor Analysis

Hyperping (https://hyperping.com) is a SaaS uptime + status-page + on-call platform aimed at the small/mid-market. It positions itself as "BetterStack at half the price" and is interesting to SolidPing for two reasons: (1) several monitoring/alerting design choices we have not seen elsewhere, and (2) a clean separation between the **operational alert object** (outages) and the **customer-comms object** (incidents) that we should consider adopting.

## Files in this directory

- [monitoring.md](monitoring.md) — Monitor types, HTTP configuration, intervals, detection / multi-region confirmation logic, region selection.
- [alerting.md](alerting.md) — Notification channels, escalation policies, on-call rotations, the outage-vs-incident split, manual ack/resolve, maintenance windows.
- [platform.md](platform.md) — Heartbeats (Healthchecks), browser checks (Playwright), status pages with subscribers.
- [api.md](api.md) — API surface, auth, versioning quirks, outgoing webhooks, community Terraform/SDKs, pricing snapshot.
- [sources.md](sources.md) — All source URLs grouped by topic.

## At a glance

| Aspect | Hyperping |
|---|---|
| Founded | 2018 (independent / bootstrapped) |
| Pricing | Free → $24 → $74 → $249 → Enterprise |
| Free tier | 20 monitors, 5-min interval, 1 status page |
| Min interval | 30 s (Pro), 20 s (Business), 10 s (Enterprise) |
| Monitor types | HTTP, port (TCP), ICMP ping, DNS, healthchecks (cron), browser (Playwright), server agents |
| Probe regions | 18 documented (`sanfrancisco, nyc, london, paris, frankfurt, seoul, mumbai, bangalore, saopaulolocal, california, virginia, sydney, toronto, amsterdam, singapore, tokyo, bahrain, capetown`) |
| Confirmation | Built-in: failure replayed across **all** other selected regions; alert only when ≥3 regions confirm |
| API surface | `https://api.hyperping.io` — mixed `/v1`, `/v2`, `/v3` per resource |
| Terraform | Community provider (`develeap/terraform-provider-hyperping`); no first-party |
| Notable | Two-table outage/incident model · webhook payload includes full multi-region trace · Core Web Vitals auto-collected on browser checks · localized i18n on incidents and maintenance · status-page-from-Statuspage importer |

## What's worth borrowing

The full list of design ideas distilled from this research lives in [wiki/research/alerting-patterns.md](../../research/alerting-patterns.md). The headline items:

1. **Outages vs Incidents as separate objects** — operational alerting (auto, ack/escalate, internal) vs customer-facing communication (status page, public updates, localized).
2. **Webhook payloads carry the full multi-region confirmation trace** (`pings[]` with `original: true/false`) so receivers can do their own correlation.
3. **`alerts_wait` per-monitor field** (discrete dropdown: `0/1/2/3/5/10/30/60` s/min) as a per-monitor confirmation knob.
4. **Concurrent shifts** in on-call rotations as a first-class field (N people on at once).
5. **Maintenance windows split into orthogonal flags** — pause monitors / post status-page notice — independent of each other.
6. **Healthcheck `/start` endpoint** to measure job duration without bolted-on metadata.
7. **Browser checks auto-emit Core Web Vitals** (LCP, CLS, TBT, FCP, TTFB) without script instrumentation.
8. **Localized titles + updates** on incidents and maintenance windows as a first-class data-model concept (`{en,fr,de,ru,nl,pl,se}`).

## Where Hyperping is weak

These are areas where SolidPing already does better, or could differentiate cheaply:

- No regex / JSON-path body assertions (only literal substring keyword match).
- No native basic-auth or bearer-auth fields on HTTP monitors (must be done via raw `Authorization` header).
- No SSL-verification toggle (always on).
- No native UDP, gRPC, multi-step API check, or database/queue protocols.
- No real-user monitoring (despite collecting Web Vitals from synthetic).
- No documented anomaly detection or AI tooling.
- No documented pagination on list endpoints.
- No first-party Terraform provider (relies on a community port).
- No alert-suppression-only maintenance mode (pause = stop collecting).
