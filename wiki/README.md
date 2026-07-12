# SolidPing Documentation

## Roadmap

- [roadmap.md](roadmap.md) — Current feature roadmap and priorities (refreshed May 2026)

## Architecture & Design

- [architecture.md](architecture.md) — System architecture: handler-service pattern, multi-tenancy, distributed checks, data model, scalability
- [api-specification.md](api-specification.md) — Complete REST API specification: auth, orgs, users, workers, checks, results, config
- [database-model.md](database-model.md) — Database schema with all 28 tables, columns, foreign keys, and design patterns
- [terraform-provider-api-audit.md](terraform-provider-api-audit.md) — API completeness audit for the out-of-tree Terraform provider: per-resource lifecycle/secret/import coverage and gaps

## Features

End-to-end pages for individual subsystems — read these before touching
the relevant code.

- [features/notifications-and-escalation.md](features/notifications-and-escalation.md) — How a check failure becomes a page: incident lifecycle, channel fan-out, escalation policies, on-call resolution, suppression layers (maintenance windows, cascade rollup, ack/snooze).
- [features/check-dependencies.md](features/check-dependencies.md) — Hard vs soft dependency edges, cascade rollup walk, parent-resolve re-evaluation, correlation windows, edge cases.
- [features/entitlements.md](features/entitlements.md) — Per-org limits and feature toggles: defaults seed, three-layer resolution (defaults → row → live usage), sources (default / self-hosted / admin / billing-service), stale fallback, audit log.
- [features/email-inbox-checks.md](features/email-inbox-checks.md) — Passive checks that succeed when an email arrives. JMAP supervisor, per-check token, status resolution priority, mailbox retention, distinction from email-as-channel.
- [features/mcp.md](features/mcp.md) — Model Context Protocol surface: endpoint, scopes (`mcp` / `mcp:read`), tool inventory, prompts, sessions, protocol version negotiation, how to add a new tool.
- [features/browser-monitoring.md](features/browser-monitoring.md) — Headless-Chrome (chromedp) checks: when to pick browser over http, execution model, capabilities & limits, worker requirements, security model.
- [features/config-as-code.md](features/config-as-code.md) — Declarative checks: export → edit → `sp apply` loop, the `solidping.io/managed` scope, reconcile plan (create/update/delete/unmanaged/rename), `${env:}`/`${param:}` secret references, prune + deletion cap, admin gating.

## Conventions

Project-wide standards and naming rules.

- [conventions/database.md](conventions/database.md) — Table naming, soft deletes, audit trails
- [conventions/checker-config.md](conventions/checker-config.md) — Checker configuration for all protocol types (HTTP, TCP, DNS, SMTP, etc.)
- [conventions/checker-metrics.md](conventions/checker-metrics.md) — Metrics compaction suffixes (_min, _max, _avg, _pct, etc.)
- [conventions/regions.md](conventions/regions.md) — Region naming (`$continent-$region-$city`) and wildcard matching
- [conventions/runners.md](conventions/runners.md) — Check & job runner pools: configuration, sizing, fetching architecture, node roles
- [conventions/state-entries.md](conventions/state-entries.md) — State entries table for Slack thread metadata
- [conventions/frontend-urls.md](conventions/frontend-urls.md) — Dashboard URL routing (`/dash/orgs/{orgSlug}/...`)
- [conventions/event-colors.md](conventions/event-colors.md) — Event color scheme: per-type color assignments for check and incident events
- [conventions/frontend-errors.md](conventions/frontend-errors.md) — Frontend error handling by HTTP status code
- [conventions/files.md](conventions/files.md) — File storage seam: backends (local FS, S3), signed URLs, group conventions

## Runbooks

Operational procedures for diagnosing the running system.

- [runbooks/memory-profiling.md](runbooks/memory-profiling.md) — Memory profiling & leak detection: pprof heap/alloc/goroutine/block profiles per role, base-diffing, the off-heap (cgo/SQLite) rule, the `/api/mgmt/memory` snapshot + Prometheus surfaces, baseline/soak procedure, GC levers (`GOMEMLIMIT`/`GOGC`).

## Testing

- [testing/e2e-ci.md](testing/e2e-ci.md) — E2E test infrastructure: CI environment, Playwright config, local execution
- [testing/http-test-checks.md](testing/http-test-checks.md) — Fake API test checks: 5 predefined scenarios (stable, flaky, unstable, slow, 503)

## Integrations

- [slack/manifest-dev.json](slack/manifest-dev.json) — Slack app manifest for development
- [slack/manifest-prod.json](slack/manifest-prod.json) — Slack app manifest for production

## Research

- [research/alerting-patterns.md](research/alerting-patterns.md) — Monitoring & alerting design ideas distilled from BetterStack and Hyperping research; input for future specs (May 2026)
- [research/screenshot-tools.md](research/screenshot-tools.md) — Go screenshot tools comparison (chromedp, Rod, gowitness, gochro) — Rod recommended

## Competitors

Market analysis of uptime monitoring services.

- [competitors/comparison.md](competitors/comparison.md) — Comparison matrix and pricing across 9 competitors, with current SolidPing positioning (May 2026)
- [competitors/criteria.md](competitors/criteria.md) — Evaluation framework: pricing, features, protocols, deployment, support
- [competitors/full-list.md](competitors/full-list.md) — Comprehensive directory of all monitoring services
- [competitors/indie-watch.md](competitors/indie-watch.md) — Indie/OSS/emerging entrants surfaced by the marketing-listening pipeline (Peekaping, OneUptime, OpenStatus, failover.io, Status Harbor, exit1, and ~24 more), tracked vs. background
- [competitors/positioning.md](competitors/positioning.md) — Competitive positioning & messaging: buyer-profile win/lose, counter-angles, positioning-convergence watch, messaging hooks

### Per-competitor analyses

- BetterStack — [betterstack/](competitors/betterstack/)
  - [betterstack/README.md](competitors/betterstack/README.md) — Index, at-a-glance, headline takeaways
  - [betterstack/monitoring.md](competitors/betterstack/monitoring.md) — Detection logic, recovery, regions/quorum, `validating` state
  - [betterstack/alerting.md](competitors/betterstack/alerting.md) — Severities, escalation policies, on-call, ack/snooze, channels, incident grouping
  - [betterstack/platform.md](competitors/betterstack/platform.md) — Heartbeats, Playwright, status pages, maintenance windows
  - [betterstack/api.md](competitors/betterstack/api.md) — REST surface, auth, pagination, monitor types, fields reference
  - [betterstack/integrations.md](competitors/betterstack/integrations.md) — Outgoing webhooks, Terraform provider, Telemetry/AI features
  - [betterstack/comparison.md](competitors/betterstack/comparison.md) — vs SolidPing capability matrix and lessons
  - [betterstack/sources.md](competitors/betterstack/sources.md) — Source URLs
- Hyperping — [hyperping/](competitors/hyperping/)
  - [hyperping/README.md](competitors/hyperping/README.md) — Index, at-a-glance, headline takeaways
  - [hyperping/monitoring.md](competitors/hyperping/monitoring.md) — Monitor types, HTTP config, intervals, multi-region confirmation
  - [hyperping/alerting.md](competitors/hyperping/alerting.md) — Channels, escalation, on-call, outage-vs-incident model, maintenance
  - [hyperping/platform.md](competitors/hyperping/platform.md) — Heartbeats (Healthchecks), browser checks, status pages
  - [hyperping/api.md](competitors/hyperping/api.md) — API surface, webhooks, community Terraform/SDKs, pricing
  - [hyperping/sources.md](competitors/hyperping/sources.md) — Source URLs
- [competitors/checkly.md](competitors/checkly.md) — Checkly analysis (monitoring-as-code, Playwright)
- Gatus — [gatus/](competitors/gatus/)
  - [gatus/README.md](competitors/gatus/README.md) — Index, at-a-glance, key features
  - [gatus/configuration.md](competitors/gatus/configuration.md) — Configuration syntax and technology stack
  - [gatus/deployment.md](competitors/gatus/deployment.md) — Installation, deployment, performance, security
  - [gatus/api.md](competitors/gatus/api.md) — API surface
  - [gatus/alerting.md](competitors/gatus/alerting.md) — Conditions, alert rules, channels
  - [gatus/comparison.md](competitors/gatus/comparison.md) — Strengths, weaknesses, vs SolidPing, use cases
  - [gatus/sources.md](competitors/gatus/sources.md) — Source URLs
- [competitors/healthchecks-io.md](competitors/healthchecks-io.md) — Healthchecks.io analysis (passive/heartbeat monitoring)
- [competitors/maintenant.md](competitors/maintenant.md) — Maintenant analysis (self-hosted Go, container observability, MCP, AGPL open-core)
- Pingdom — [pingdom/](competitors/pingdom/)
  - [pingdom/README.md](competitors/pingdom/README.md) — Index, at-a-glance, key features
  - [pingdom/monitoring.md](competitors/pingdom/monitoring.md) — Check types in-depth (HTTP, transaction, ping, TCP/UDP, DNS, mail) and global probe network
  - [pingdom/api.md](competitors/pingdom/api.md) — API architecture, auth, core endpoints (3.1), complete API reference
  - [pingdom/pricing.md](competitors/pingdom/pricing.md) — Synthetic and RUM plans, billing, pain points
  - [pingdom/comparison.md](competitors/pingdom/comparison.md) — vs SolidPing, technical considerations, limitations, API design patterns
  - [pingdom/examples.md](competitors/pingdom/examples.md) — Integration examples (HTTP, TCP, DNS, SMTP, results, summary, maintenance)
  - [pingdom/sources.md](competitors/pingdom/sources.md) — Source URLs
- [competitors/site24x7.md](competitors/site24x7.md) — Site24x7 analysis (Zoho/ManageEngine all-in-one, 100+ monitor types, AIOps)
- [competitors/statuscake.md](competitors/statuscake.md) — StatusCake analysis (43 probe locations)
- [competitors/uptime-kuma.md](competitors/uptime-kuma.md) — Uptime Kuma analysis (self-hosted, Vue.js)
- UptimeRobot — [uptimerobot/](competitors/uptimerobot/)
  - [uptimerobot/README.md](competitors/uptimerobot/README.md) — Index, at-a-glance, headline takeaways
  - [uptimerobot/api.md](competitors/uptimerobot/api.md) — API architecture, auth, rate limits, core v3 endpoints
  - [uptimerobot/api-reference.md](competitors/uptimerobot/api-reference.md) — Complete API reference tables (endpoints, types, status codes, headers)
  - [uptimerobot/heartbeats.md](competitors/uptimerobot/heartbeats.md) — Heartbeat monitoring in-depth (cron, Task Scheduler, Python)
  - [uptimerobot/pricing.md](competitors/uptimerobot/pricing.md) — Free / Solo / Team / Enterprise plans and pricing insights
  - [uptimerobot/comparison.md](competitors/uptimerobot/comparison.md) — vs SolidPing, technical considerations, limitations, API design patterns
  - [uptimerobot/examples.md](competitors/uptimerobot/examples.md) — Integration examples (HTTP, keyword, port, heartbeat, Slack, status page)
  - [uptimerobot/sources.md](competitors/uptimerobot/sources.md) — Source URLs
