---
model: opus
effort: medium
---

# Rate-limited orgs learn it from a support ticket — surface an org-level banner

## Problem

When an org exceeds its `MaxChecksPerMinute` entitlement, executions are
silently deferred (`server/internal/checkworker/worker.go:950–973`). The only
traces are an INFO log line and a Prometheus counter
(`prommetrics.ChecksRateLimited`) — neither reaches the user. The dashboard
shows unexplained gaps in results, which reads as "the product is broken",
not "you are over your plan" (exactly how the 2026-08-26 `public`-org
incident presented).

Once spec 2026-08-26-02 lands, the deficit **rotates** across all the org's
checks, so per-check signals would light up on most checks and carry no
information. The org is the right unit: the questions are "is my org's
scheduled demand over its cap?" (predictive) and "were executions actually
skipped?" (factual).

Deliberate scope decision: **org-level banner only, no per-check flag or
badge** — decided by the maintainer on 2026-08-26.

## Proposal

### Backend

1. **Demand vs cap, computed**: expose the org's scheduled demand next to
   the resolved limit. Demand = Σ over enabled, non-deleted, *active*
   checks of `regions × (60 / period_seconds)` (each selected region runs
   the full period — spec 2026-07-20-05). Exclude passive types
   (heartbeat/email): they return before the token gate
   (`worker.go:904`) and never consume budget.
2. **Skips, counted**: increment an `org_usage_counters` row
   (`kind = 'check_rate_limited'`, daily `period_start` — schema already
   fits: `organization_uid, kind, period_start date, count`) on every
   deferral, from both gates (worker path and the agent claim path in
   `internal/handlers/agentws/handler.go:831–845`). One upsert per skip is
   bounded by the cap itself (an org can't skip faster than it schedules).
3. **API**: extend the resolved-entitlements payload
   (`internal/handlers/entitlements/handler.go`, group
   `/orgs/:org/entitlements` in `server.go`) with something like:
   `checksPerMinute: { demand, limit, skippedToday }` (camelCase, wrapped
   per REST conventions).

### Frontend (dash0)

4. Amber warning banner — same visual weight as the "empty maintenance
   window" warning convention — on **two** surfaces: the checks list and
   the org Usage page (`/orgs/$org/organization/usage`). Shown when
   `demand > limit` (predictive) or `skippedToday > 0` (factual, catches
   an org that just dropped below the cap but lost executions today).
5. Banner copy: "Your organization schedules ~{demand} check executions per
   minute but your plan allows {limit}/min — some executions are being
   skipped." with two actions: a link to the scheduling page (spec
   2026-08-26-04; until that page ships, link to the Usage page), and in
   SaaS mode the existing upgrade CTA (`upgradeUrl`).
6. All four locales (`en`, `fr`, `de`, `es`) — `bun run test:unit` gates
   locale-key completeness.

### Tests

- Backend: demand computation (multi-region, passive exclusion, disabled
  checks excluded), counter increments from both gates, payload shape.
- Frontend: banner renders on both surfaces for `demand > limit`, for
  `skippedToday > 0` with demand under cap, and not at all when neither
  holds. Unit-level is enough; a Playwright assertion on the Usage page is
  a bonus.

## Non-goals

- Per-check flags, badges, or per-check `last_rate_limited_at` — explicitly
  rejected.
- Per-skip result rows or events (write volume equals real execution volume
  and pollutes availability/duration series).
- Email/notification alerts about being over the limit — a later spec if
  wanted.
