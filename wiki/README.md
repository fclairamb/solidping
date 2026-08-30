# SolidPing Documentation

## Roadmap

- [roadmap.md](roadmap.md) — Feature roadmap and priorities (rewritten 2026-07-28: adoption blockers → product completeness → enterprise maturity, plus explicit non-priorities)

## Architecture & Design

- [architecture.md](architecture.md) — System architecture: handler-service pattern, multi-tenancy, distributed checks, data model, scalability
- [api-specification/README.md](api-specification/README.md) — REST API index: conventions, auth legend, and a table of contents for every domain page
  - [api-specification/management.md](api-specification/management.md) — Health, version, limits, memory, bug report, feature flags, scheduling cost, email preview
  - [api-specification/auth.md](api-specification/auth.md) — Login, registration, tokens, 2FA, passkeys, OAuth providers, OIDC, SAML
  - [api-specification/orgs.md](api-specification/orgs.md) — Organizations, settings, org tokens, invitations, members, membership requests
  - [api-specification/entitlements.md](api-specification/entitlements.md) — Per-org limits, the billing-service write API, and the audit log
  - [api-specification/checks.md](api-specification/checks.md) — Checks, validate, export/import/apply, dependencies, clone, labels, check types, groups, severities, badges
  - [api-specification/results-incidents.md](api-specification/results-incidents.md) — Results, incidents and their actions, events, and the live-update WebSocket
  - [api-specification/notifications.md](api-specification/notifications.md) — Notification routes/contacts, delivery history, web push, email suppressions, public unsubscribe
  - [api-specification/on-call.md](api-specification/on-call.md) — Escalation policies, on-call schedules, overrides, iCal feeds
  - [api-specification/status-pages.md](api-specification/status-pages.md) — Status pages, sections, resources, subscribers, status updates, public views and feeds
  - [api-specification/maintenance.md](api-specification/maintenance.md) — Maintenance windows and their check associations
  - [api-specification/integrations.md](api-specification/integrations.md) — Integrations/channels, Slack app endpoints, Freebox pairing and LAN hosts
  - [api-specification/agents.md](api-specification/agents.md) — Deported agent WebSocket, private regions, enrollment tokens, agent inventory
  - [api-specification/discovery.md](api-specification/discovery.md) — Network discovery scans and discovered checks
  - [api-specification/jobs.md](api-specification/jobs.md) — Org jobs, admin job observability, system-wide job observability
  - [api-specification/files.md](api-specification/files.md) — Generic file storage and signed public reads
  - [api-specification/heartbeat.md](api-specification/heartbeat.md) — Heartbeat / cron ping endpoints
  - [api-specification/mcp-oauth.md](api-specification/mcp-oauth.md) — MCP endpoint and the embedded OAuth 2.1 authorization server
  - [api-specification/system.md](api-specification/system.md) — Regions, system parameters, email inbox, activation, scheduling lane load
  - [api-specification/test-and-static.md](api-specification/test-and-static.md) — Test-mode endpoints, SPA/docs/static routes, metrics, CORS preflight
  - [api-specification/errors.md](api-specification/errors.md) — Error envelope and standard error codes
- [database-model/README.md](database-model/README.md) — Database schema index: all 50 tables grouped by domain, plus the entity-relationship overview
  - [database-model/core.md](database-model/core.md) — Tenancy root and configuration: organizations, parameters, app_settings, files
  - [database-model/auth.md](database-model/auth.md) — Users, credentials, identity providers, org membership, OAuth client registry
  - [database-model/checks.md](database-model/checks.md) — Checks, groups, labels, dependencies, the lease-based check_jobs scheduler, workers
  - [database-model/agents.md](database-model/agents.md) — Deported org-scoped agents and their one-shot enrollment tokens
  - [database-model/results-incidents.md](database-model/results-incidents.md) — Time-series results, incident lifecycle, notification ledger, severities, audit events
  - [database-model/notifications.md](database-model/notifications.md) — Integrations, per-check channels, escalation policies, on-call schedules, email suppressions
  - [database-model/status-pages.md](database-model/status-pages.md) — Status pages, sections, resources, subscribers, published updates
  - [database-model/maintenance.md](database-model/maintenance.md) — Maintenance windows and the checks/groups they cover
  - [database-model/discovery.md](database-model/discovery.md) — discovered_checks: scan-produced check suggestions awaiting promotion
  - [database-model/entitlements.md](database-model/entitlements.md) — Per-org plan limits and the entitlement change audit trail
  - [database-model/jobs.md](database-model/jobs.md) — Generic background job queue and the key-value state store
  - [database-model/patterns.md](database-model/patterns.md) — Schema-wide design patterns, migration layout, file locations
- [terraform-provider-api-audit.md](terraform-provider-api-audit.md) — API completeness audit for the out-of-tree Terraform provider: per-resource lifecycle/secret/import coverage and gaps

## Features

End-to-end pages for individual subsystems — read these before touching
the relevant code.

- [features/notifications-and-escalation.md](features/notifications-and-escalation.md) — How a check failure becomes a page: incident lifecycle, channel fan-out, escalation policies, on-call resolution, suppression layers (maintenance windows, cascade rollup, ack/snooze).
- [features/check-dependencies.md](features/check-dependencies.md) — Hard vs soft dependency edges, cascade rollup walk, parent-resolve re-evaluation, correlation windows, edge cases.
- [features/entitlements.md](features/entitlements.md) — Per-org limits (`maxChecks`, `maxUsers`, `maxChecksPerMinute`) and where each is enforced; defaults per deployment mode, resolution (defaults → row → live usage), sources, stale fallback, audit log. Note: there are **no** feature toggles.
- [features/email-inbox-checks.md](features/email-inbox-checks.md) — Passive checks that succeed when an email arrives. JMAP supervisor, per-check token, status resolution priority, mailbox retention, distinction from email-as-channel.
- [features/mcp.md](features/mcp.md) — Model Context Protocol surface: endpoint, scopes (`mcp` / `mcp:read`), tool inventory, prompts, sessions, protocol version negotiation, how to add a new tool.
- [features/deported-agents.md](features/deported-agents.md) — Deported agents / private locations: customer-hosted check workers, outbound WebSocket protocol, Ed25519 enrollment & reconnect, age-sealed credentials the server cannot decrypt, private-region security boundary, and a competitor comparison.
- [features/browser-monitoring.md](features/browser-monitoring.md) — Headless-Chrome (chromedp) checks: when to pick browser over http, execution model, capabilities & limits, worker requirements, security model.
- [features/showcase-media.md](features/showcase-media.md) — Regenerable product screenshots & video: the `web/dash0/showcase/` Playwright pipeline, `make showcase`, AV1 post-processing, which assets are committed, where they're surfaced, and the marketing (`solidping-website`) hand-off.
- [features/config-as-code.md](features/config-as-code.md) — Declarative checks: export → edit → `sp apply` loop, the `solidping.io/managed` scope, reconcile plan (create/update/delete/unmanaged/rename), `${env:}`/`${param:}` secret references, prune + deletion cap, admin gating.
- [features/platform-watchdog.md](features/platform-watchdog.md) — The hourly `platform_watchdog` job: how the platform reports on ITSELF. Three independent detectors (dark region with assigned work, fleet execution collapse, frozen active incidents), transition-based anti-flood, delivery through the operators' own notification routes, and the out-of-band `solidping_watchdog_*` gauges.
- [features/email-dark-mode.md](features/email-dark-mode.md) — How transactional email renders in a dark inbox: the per-client support matrix, the `light only` pin (why it stays), the designed `prefers-color-scheme` palette in `base.html`, the `?colorScheme=dark` preview, and the still-open, human-gated Gmail un-pin decision plus its device-matrix template.
- [features/results-aggregation.md](features/results-aggregation.md) — The raw → hour → day → month results rollup: per-org job, tier boundaries, transactional compaction, pure-Go aggregate math (warning-counts-as-up, degraded promotion, metric suffixes), retention config, consumers (uptimebar, badges, results-API fallback), failure-mode history.

## Conventions

Project-wide standards and naming rules.

- [conventions/database.md](conventions/database.md) — Table naming, soft deletes, audit trails
- [conventions/checker-config.md](conventions/checker-config.md) — Checker configuration for all protocol types (HTTP, TCP, DNS, SMTP, etc.)
- [conventions/checker-metrics.md](conventions/checker-metrics.md) — Metrics compaction suffixes (_min, _max, _avg, _pct, etc.)
- [conventions/regions.md](conventions/regions.md) — Region naming (`$continent-$region-$city`) and wildcard matching; org-relative private regions (`@<slug>`), the audit of every path that matches one, and migration 012
- [conventions/runners.md](conventions/runners.md) — Check & job runner pools: configuration, sizing, fetching architecture, node roles
- [conventions/state-entries.md](conventions/state-entries.md) — State entries table for Slack thread metadata
- [conventions/frontend-urls.md](conventions/frontend-urls.md) — Dashboard URL routing (`/dash/orgs/{orgSlug}/...`)
- [conventions/email-templates.md](conventions/email-templates.md) — Transactional email templates: required blocks (preheader, text), the label/value fact grid, and why the subject and plaintext parts render through text/template
- [conventions/event-colors.md](conventions/event-colors.md) — Event color scheme: per-type color assignments for check and incident events
- [conventions/frontend-errors.md](conventions/frontend-errors.md) — Frontend error handling by HTTP status code
- [conventions/files.md](conventions/files.md) — File storage seam: backends (local FS, S3), signed URLs, group conventions
- [conventions/generated-client.md](conventions/generated-client.md) — `pkg/client` regeneration cadence (once per batch/release) and ownership; how CI catches a regeneration that doesn't compile
- [conventions/changelog.md](conventions/changelog.md) — Writing CHANGELOG.md entries as user-facing prose for the generated `/docs/changelog` page; the scope-label lookup table; checking the render before a release ships

## Runbooks

Operational procedures for diagnosing the running system.

- [runbooks/memory-profiling.md](runbooks/memory-profiling.md) — Memory profiling & leak detection: pprof heap/alloc/goroutine/block profiles per role, base-diffing, the off-heap (cgo/SQLite) rule, the `/api/mgmt/memory` snapshot + Prometheus surfaces, baseline/soak procedure, GC levers (`GOMEMLIMIT`/`GOGC`).
- [runbooks/custom-domain-tls.md](runbooks/custom-domain-tls.md) — Custom-domain TLS: single-CNAME verification modes (`shared`/`token`), in-server ACME (`acme.*`) vs. an external TLS proxy, the four edge options (SNI passthrough, dedicated LB, chained instances, external proxy), config reference, acceptance checklist, troubleshooting, and the 2026-08-23 investigation of *intermittent* re-verification failure while `dig` succeeds (class: resolver/transport fault, infra-side).

## Testing

- [testing/e2e-ci.md](testing/e2e-ci.md) — E2E test infrastructure: CI environment, Playwright config, local execution
- [testing/http-test-checks.md](testing/http-test-checks.md) — Fake API test checks: 5 predefined scenarios (stable, flaky, unstable, slow, 503)

## Integrations

- [slack/README.md](slack/README.md) — Slack app manifests overview; the `/check` and `/comment` slash commands, the `comment_ingestion` (explicit-by-default) setting, inbound thread-reply → incident-comment scopes and the re-authorization requirement
- [slack/manifest-dev.json](slack/manifest-dev.json) — Slack app manifest for development
- [slack/manifest-prod.json](slack/manifest-prod.json) — Slack app manifest for production
- [discord/README.md](discord/README.md) — Discord bot operator setup: application/bot creation, the exact permissions requested and why Manage Threads is needed, the privileged `MESSAGE_CONTENT` intent and its 100-guild review threshold, the two inbound transports (HTTPS interactions + Gateway), Ed25519 verification, guild→org mapping and comment ingestion

## Research

- [research/alerting-patterns.md](research/alerting-patterns.md) — Monitoring & alerting design ideas distilled from BetterStack and Hyperping research; input for future specs (May 2026)
- [research/screenshot-tools.md](research/screenshot-tools.md) — Go screenshot tools comparison (chromedp, Rod, gowitness, gochro) — recommended Rod at the time; **note the shipped browser checker actually uses chromedp**, so treat this as a historical evaluation
- [research/market-feedback.md](research/market-feedback.md) — What users hate, want, and will pay for: public sentiment about uptime/incident tools from HN, Indie Hackers, dev.to and review aggregators (complements `competitors/`, which covers what the tools *do*)

## Competitors

Market analysis of uptime monitoring services.

- Cross-competitor comparison — [competitors/comparison/](competitors/comparison/)
  - [comparison/README.md](competitors/comparison/README.md) — Index of the comparison set
  - [comparison/overview.md](competitors/comparison/overview.md) — At-a-glance matrix across 7 uptime-first competitors, where SolidPing stands today, and the 2026-07 pricing/market-moves refresh
  - [comparison/pricing.md](competitors/comparison/pricing.md) — Free, entry, mid-tier and 100-monitor pricing brackets with a winner per bracket
  - [comparison/monitor-types.md](competitors/comparison/monitor-types.md) — Check-type matrix across 9 tools (HTTP → game servers, email inbox, custom JS)
  - [comparison/api.md](competitors/comparison/api.md) — Auth, API design, rate limits and endpoint coverage (BetterStack / UptimeRobot / Pingdom)
  - [comparison/features.md](competitors/comparison/features.md) — Monitoring capabilities, notification-channel matrix, advanced features, developer experience
  - [comparison/pros-cons.md](competitors/comparison/pros-cons.md) — Pros, cons and "best for" verdicts per vendor
  - [comparison/use-cases.md](competitors/comparison/use-cases.md) — What to learn from / avoid per vendor, and a recommendation per user type
  - [comparison/solidping-position.md](competitors/comparison/solidping-position.md) — SolidPing advantages, Tier 1/2/3 feature inventory (✅ shipped, ❌ gaps), unique strengths, SaaS pricing proposal
  - [comparison/verdict.md](competitors/comparison/verdict.md) — Summary table and winner-by-category verdict
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
- [competitors/checkmate.md](competitors/checkmate.md) — Checkmate analysis (Bluewave Labs; self-hosted Node/Mongo, hardware-metrics Capture agent, GlobalPing distribution)
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
