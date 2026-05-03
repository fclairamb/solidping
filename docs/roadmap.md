# Features Roadmap

> **Status**: Snapshot of priorities as of **May 2026**. Replaces the original Dec 2025 roadmap (Incidents + Email Alerts, Database Checks, Slack Alerts, OpenTelemetry — all shipped). Pull this file forward whenever priorities shift; don't archive it as historical reference.

## Where we are

The original roadmap is fully shipped. SolidPing now has 32 check types, 9 native notification channels, multi-region distributed workers, group-incident correlation, on-call schedules, escalation policies, credentials encryption at rest, labels, check templates, Slack Marketplace install, and an MCP server.

The product has crossed the bar where it can credibly replace **BetterStack + Opsgenie/PagerDuty** for self-hosted teams. The remaining work is about **closing the last competitive gaps** and **lowering switching cost** for users on other tools.

See `competitors/comparison.md` for the full ✅/❌ inventory against 8 competitors.

---

## Priority 1: Close the alert-volume and channel-coverage gaps

These ship together because they all touch the notification-fan-out path and amplify each other. Doing them in this order lets each one validate the design of the next.

### 1.1 Telegram, Microsoft Teams, PagerDuty notification channels
**Why P1**: These are the three channels every SaaS competitor ships and every prospective user asks for first. Specs are drafted (`../specs/ideas/2026-03-22-telegram-notifications.md`, `../specs/ideas/2026-03-22-notification-channels.md`). Discord and the chat-platform OAuth flow have already proven the pattern.

**Order**: Telegram → Discord-style webhook (already done; informs Teams) → MS Teams → PagerDuty. PagerDuty last because it's the only one that uses a different routing-key model and an Events API v2 dedup key.

**Dependencies**: None — uses the existing connection model.

### 1.2 Status-page subscriber notifications
**Why P1**: Once an outage hits the public status page, end users want to subscribe by email or RSS for the duration of the incident. UptimeRobot, Pingdom, Checkly, BetterStack all ship this. It's the single feature most asked for from public-status-page users.

**Scope**: Email + RSS to start; SMS is a Tier 3 add-on. Per-incident subscription so users don't get blanket-paged across unrelated incidents. Reuses the existing email sender and the public status page domain model.

**Dependencies**: None.

### 1.3 Screenshot capture on HTTP failure
**Why P1**: BetterStack uses this as a primary sales bullet ("see what your users saw when it broke"). Spec is ready in `../specs/ideas/2026-01-05-screenshots.md` — Rod was already chosen during the browser-checks work, and the screenshot service can be a thin Rod-based microservice deployed once per region.

**Dependencies**: S3-compatible object storage (a single bucket per deployment). MinIO works for self-hosters.

---

## Priority 2: Lower switching cost

### 2.1 Importers from BetterStack, UptimeRobot, Uptime Kuma
**Why P2**: A user with 50 monitors elsewhere will not migrate by hand. An importer that ingests their existing config (via API key for SaaS, JSON export for Uptime Kuma) is the difference between "I'd love to switch" and "we did switch." Spec stub in `../specs/ideas/2025-12-28-importers.md`.

**Order**: Uptime Kuma (JSON export, simplest) → UptimeRobot (largest user base) → BetterStack (most complex but highest-value migration).

### 2.2 Configuration as Code — Terraform provider
**Why P2**: Gatus, Checkly, BetterStack, Pingdom all have one. DevOps teams that already manage infra with Terraform won't adopt a tool that doesn't fit their workflow. Lower-effort than it sounds because the REST API is already complete and well-tested — the provider is mostly schema mapping.

**Dependencies**: Stable v1 REST API (already done).

### 2.3 Automatic application discovery
**Why P2 (and a differentiator)**: Spec in `../specs/ideas/2025-12-28-automatic-app-discovery.md`. When a user enters `metabase.example.com`, suggest `/api/health` automatically. **No competitor has this** — it's a small piece of code that produces an outsized "wow" moment in onboarding. Worth doing now because it directly shortens time-to-first-check.

---

## Priority 3: Operational maturity

### 3.1 Org-level check rate limiting
**Why P3**: Spec ready in `../specs/backlog/2026-03-30-org-check-rate-limit.md`. Today nothing prevents a single tenant from creating 1,000 30-second checks and saturating the worker pool. Proportional fair scaling is the right algorithm and the spec is concrete. This becomes critical the first time we stand up a multi-tenant SaaS.

**Dependencies**: None.

### 3.2 Initial-result semantics cleanup
**Why P3**: Spec in `../specs/backlog/2026-03-23-check-started-result.md`. The `initial` result already exists but its neutrality with respect to status, streaks, and incidents isn't consistently enforced everywhere. A small but high-value cleanup that fixes a class of edge-case bugs around re-enabled checks and config changes.

### 3.3 Subchecks
**Why P3**: Spec stub in `../specs/ideas/2026-01-01-subchecks.md`. Auto-spawn an SSL/domain-expiration sub-check from a parent HTTP check (run daily, separate alerting). Reduces manual config when a user adds a new HTTPS endpoint and forgets the cert/domain checks.

---

## Priority 4: Differentiators (when ready)

These don't have direct competitive pressure but expand the addressable market.

1. **Page speed / Core Web Vitals monitoring** — Pingdom, StatusCake. Different code path from health checks; needs Lighthouse or Rod-based metrics.
2. **Real User Monitoring (RUM)** — Pingdom, Site24x7. Front-end JS snippet + ingestion endpoint. Whole different ingestion pipeline.
3. **Mobile apps or installable PWA** — UptimeRobot, Pingdom. Alerts on the phone matter for on-call.
4. **GitHub/GitLab issue integration** — Gatus. One-step "open an issue when an incident fires."
5. **SMS / Voice escalations via Twilio** — every major SaaS. Useful for paid SaaS tier; less essential for self-hosters.
6. **Heartbeat enhancements** — `/start` endpoint, exit codes, log attachment (Healthchecks.io's design). Matters to cron-monitoring users specifically.
7. **AIOps / anomaly detection** on response-time series — Site24x7, Datadog. Big project; revisit when we have enough data per check to make it useful.
8. **NATS notifier** — `../specs/ideas/2025-12-28-nats-notifier.md`. Replace pg LISTEN/NOTIFY with NATS for the worker fan-out. Only matters when scale demands it.
9. **Drop SQLite** — `../specs/ideas/2025-12-28-drop-sqlite.md`. Open question; revisit if SQLite parity becomes a real maintenance drain.

---

## Cross-cutting considerations

- **The notification-channel pattern is the bottleneck for adding more.** Every new channel is ~250 lines + a sender + UI form + i18n. Worth investing in a tiny code-gen / template if we add more than two more.
- **Group-incident correlation changed the calculus on alert storms.** Now that one outage = one alert per channel (not N), adding more channels is no longer dangerous from an alert-fatigue standpoint.
- **Credentials encryption raised the bar for security claims.** Status-page subscriber notifications must respect the same opacity — subscribers' email addresses are PII and should be treated accordingly.
- **Importers and Terraform provider both depend on a stable REST API.** The API is stable; new endpoints should follow the same shape (camelCase, `data` envelope, `$uid` paths) — see `../CLAUDE.md`.
