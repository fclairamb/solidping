---
model: sonnet
effort: medium
---

# No UptimeRobot importer — add the missing member of the planned migration trio

## Problem

The importers shipped for Uptime Kuma, Better Stack, and Gatus
([importers/](server/internal/handlers/checks/importers/handler.go) —
converters registered via `converterFor`/`SupportedSources`, shared
normalization rules and mapping tables in
[mapping.md](server/internal/handlers/checks/importers/mapping.md), golden
tests with `testdata/` fixtures, and `migrate-from-*.md` guides on the docs
site). The July roadmap's planned order was Uptime Kuma → **UptimeRobot** →
BetterStack; UptimeRobot — the largest SaaS monitoring user base — is the one
still missing.

## Proposal

A new `uptimerobot` converter following the existing pattern exactly.

### Input format

The UptimeRobot API v2 `getMonitors` response JSON (users fetch it with a
read-only API key; document the exact `curl` in the migration guide). Accept:

- the raw response object (`{"stat": "ok", "pagination": {...}, "monitors": [...]}`),
- a bare `monitors` array,
- an array of page objects (concatenated paginated responses),

so users can paste whatever their export produced.

### Mapping

| UptimeRobot | SolidPing |
|---|---|
| `type: 1` HTTP(s) | `http` check on `url` |
| `type: 2` Keyword | `http` + content assertion (`keyword_type: 1` exists → contains, `2` not-exists → notContains, `keyword_value`) |
| `type: 3` Ping | ping/ICMP check |
| `type: 4` Port | `tcp` check (`sub_type` well-known ports 1–6 → their port, `99` → `port` field) |
| `type: 5` Heartbeat | `heartbeat` check |
| `interval` (seconds) | `period` (clamped by `normalizeChecks`) |
| `timeout` | uniform `timeout`, clamped 1–30s |
| `status: 0` (paused) | check disabled |
| `http_auth_type` basic + username/password | HTTP basic-auth config |

Not imported, surfaced as warnings per the shared rules (warnings never block
an import): alert contacts, maintenance windows, custom HTTP statuses/headers
beyond what the check config supports, PSP settings. Slugs derive from monitor
names via the shared `slugify` dedup so re-imports upsert idempotently.

### Deliverables

- `uptimerobot.go` + `uptimerobot_test.go` with golden tests and `testdata/`
  fixtures (cover each monitor type, a paused monitor, keyword both ways,
  port sub_types, and a paginated multi-page input).
- New section in `mapping.md`.
- Source registered in `SupportedSources()`; dash0 import page gains the
  source entry (+ `checks.json` locale strings for en/fr/de/es).
- Docs: `web/docs/docs/features/migrate-from-uptimerobot.md` following
  `migrate-from-uptime-kuma.md` (how to export, what maps, what warns).
