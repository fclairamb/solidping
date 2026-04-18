# SolidPing Documentation

## Architecture & Design

- [architecture.md](architecture.md) — System architecture: handler-service pattern, multi-tenancy, distributed checks, data model, scalability
- [api-specification.md](api-specification.md) — Complete REST API specification: auth, orgs, users, workers, checks, results, config
- [database-model.md](database-model.md) — Database schema with all 28 tables, columns, foreign keys, and design patterns

## Conventions

Project-wide standards and naming rules.

- [conventions/database.md](conventions/database.md) — Table naming, soft deletes, audit trails
- [conventions/checker-config.md](conventions/checker-config.md) — Checker configuration for all protocol types (HTTP, TCP, DNS, SMTP, etc.)
- [conventions/checker-metrics.md](conventions/checker-metrics.md) — Metrics compaction suffixes (_min, _max, _avg, _pct, etc.)
- [conventions/regions.md](conventions/regions.md) — Region naming (`$continent-$region-$city`) and wildcard matching
- [conventions/state-entries.md](conventions/state-entries.md) — State entries table for Slack thread metadata
- [conventions/frontend-urls.md](conventions/frontend-urls.md) — Dashboard URL routing (`/dash/orgs/{orgSlug}/...`)
- [conventions/event-colors.md](conventions/event-colors.md) — Event color scheme: per-type color assignments for check and incident events
- [conventions/frontend-errors.md](conventions/frontend-errors.md) — Frontend error handling by HTTP status code

## Testing

- [testing/e2e-ci.md](testing/e2e-ci.md) — E2E test infrastructure: CI environment, Playwright config, local execution
- [testing/http-test-checks.md](testing/http-test-checks.md) — Fake API test checks: 5 predefined scenarios (stable, flaky, unstable, slow, 503)

## Integrations

- [slack/manifest-dev.json](slack/manifest-dev.json) — Slack app manifest for development
- [slack/manifest-prod.json](slack/manifest-prod.json) — Slack app manifest for production

## Research

- [research/screenshot-tools.md](research/screenshot-tools.md) — Go screenshot tools comparison (chromedp, Rod, gowitness, gochro) — Rod recommended

## Competitors

Market analysis of uptime monitoring services.

- [competitors/comparison.md](competitors/comparison.md) — Comparison matrix and pricing across 6 competitors
- [competitors/criteria.md](competitors/criteria.md) — Evaluation framework: pricing, features, protocols, deployment, support
- [competitors/full-list.md](competitors/full-list.md) — Comprehensive directory of all monitoring services
- [competitors/betterstack.md](competitors/betterstack.md) — BetterStack Uptime analysis
- [competitors/checkly.md](competitors/checkly.md) — Checkly analysis (monitoring-as-code, Playwright)
- [competitors/gatus.md](competitors/gatus.md) — Gatus analysis (config-as-code, self-hosted)
- [competitors/healthchecks-io.md](competitors/healthchecks-io.md) — Healthchecks.io analysis (passive/heartbeat monitoring)
- [competitors/pingdom.md](competitors/pingdom.md) — Pingdom analysis (RUM, page speed)
- [competitors/statuscake.md](competitors/statuscake.md) — StatusCake analysis (43 probe locations)
- [competitors/uptime-kuma.md](competitors/uptime-kuma.md) — Uptime Kuma analysis (self-hosted, Vue.js)
- [competitors/uptimerobot.md](competitors/uptimerobot.md) — UptimeRobot analysis (free tier, protocol support)
