---
model: sonnet
effort: medium
---

# Public /metrics leaks per-org activity to unauthenticated scrapers

## Problem

`/metrics` is mounted on the unauthenticated main group when
`Prometheus.Enabled` is true — and enabled **is the default**
(`internal/config/config.go:1750-1753`, `internal/app/server.go:2251-2263`).
The exposed metrics carry per-org labels:
`CheckExecutions{organization, checkType, status, region}`,
`IncidentsActive{organization}`, `IncidentsTotal{organization, checkType}`
(`prommetrics/recording.go`, `prommetrics/metrics.go`). On any internet-facing
instance that is unauthenticated org-slug enumeration plus an activity and
incident-volume leak. The dashboard's own memory endpoints got
`RequireSuperAdmin` gating (`server.go:2241-2249`); `/metrics` never did.

## Proposal (recommendation: bearer scrape token)

Keep `/metrics` (Prometheus scraping is a first-class feature) but make it
authenticated-by-default:

1. New system parameter `metrics.scrape_token` (env
   `SP_METRICS_SCRAPE_TOKEN`, `secret=true` like other credentials):
   - **Set** → `/metrics` requires `Authorization: Bearer <token>`
     (constant-time compare, `subtle.ConstantTimeCompare`), 401 otherwise.
   - **Unset** → `/metrics` answers 404 (same convention as
     `SP_PROMETHEUS_ENABLED=false`), and boot logs an INFO: "metrics
     endpoint disabled, set metrics.scrape_token to enable scraping".
2. `SP_PROMETHEUS_ENABLED` remains the master on/off gate on top.
3. Per-org labels can stay — once authenticated, exposing them to the
   operator's own scraper is the point. No label surgery needed.
4. Migration note in the changelog: **breaking for operators who scrape
   today** — they must set a token once and add it to their scrape config
   (`authorization: credentials: <token>`). This is a deliberate default
   flip: a security fix that stays off by default is not a fix.
5. Do not add per-IP rate limiting here (scrapers poll fast by design); the
   token is the gate. Keep the endpoint outside the `/api/v1/` limiter
   scope.

Rejected alternatives, for the record: default-disabled endpoint (breaks
every self-hosted scrape config silently) and localhost-only binding
(breaks containerized scrapers and adds network config nobody tests).

## Tests

- No token set → `/metrics` returns 404, boot log emitted (config test).
- Token set → request with correct bearer returns 200 and `text/plain`
  exposition; wrong/missing bearer → 401 with a generic body (no
  constant-time oracle difference beyond timing).
- `SP_PROMETHEUS_ENABLED=false` still wins (404 regardless of token).
- Env-var table test gains `SP_METRICS_SCRAPE_TOKEN`.
- A scrape with the token sees `CheckExecutions{organization=...}` (labels
  intact).