# Features Roadmap

> **Status**: Snapshot of priorities as of **2026-08-21**. Replaces the July 2026
> snapshot, whose Priority 1 has now fully shipped. Pull this file forward
> whenever priorities shift; don't archive it as historical reference.

## Where we are

The July snapshot's adoption blockers are done. August shipped the three
remaining notification channels — **Telegram** (bot + webhook routes), **MS
Teams** (webhook card *and* bot), **PagerDuty** (Events API v2 routing keys,
replacing the retired Opsgenie) — plus **auto-published incidents**: the
incident-publication overlay (spec `2026-08-19-08`) gives outages an automatic
path to the status page, adds manual incident publications, and draws the
public/internal boundary the May "outage-vs-incident split" spec asked for.
The **importers** landed too: Uptime Kuma, Better Stack, and Gatus
(`server/internal/handlers/checks/importers/`, shared clamp-don't-reject
normalization, golden tests, `migrate-from-*` guides on the docs site).

Most of July's P2 shipped as well: **SLO/SLA reporting** (calendar-month error
budgets, burn-rate readout, monthly history, emailed uptime digests,
maintenance excluded at ingest), **failure screenshots** end to end (browser
checks, bounded capture LRU, deported-agent upload-request frames), **HTTP
failure response capture**, **generic attachments** with incident screenshots,
org logo upload, and a wide UX-polish pass (command palette, list-page
consistency, empty states, status0 fixes).

In flight: the **Discord bot upgrade**
(`../specs/todos/2026-08-21-06-discord-bot-integration.md`) — Slack-parity
install flow, destination picker, thread tracking, ack buttons, inbound
comments.

The remaining gaps still sort into the same three questions:

1. **Can an evaluating team switch?** — one importer left (UptimeRobot).
2. **Does the product feel finished?** — status-page branding & privacy,
   failure diagnostics beyond screenshots.
3. **Will someone pay?** — white-label, burn-rate alerting, enterprise audit
   trail.

Specs are filed for all five (see below); they are the current work queue.

---

## Priority 1: Adoption & monetization

### 1.1 Finish the Discord bot (in flight)
Spec `../specs/todos/2026-08-21-06-discord-bot-integration.md`, implementation
started. Discord is the last channel where SolidPing ships something
noticeably shallower than the competition's.

### 1.2 Status-page trust & branding — spec filed
`../specs/todos/2026-08-21-07-status-page-branding-privacy-and-subscriptions.md`.
Logo/favicon upload, white-label as a paid entitlement, password-protected
pages, webhook + Slack subscriptions. The same three holes July identified —
but the org-logo upload pattern, S3 file storage, and entitlements plumbing
all exist now, so this is assembly, not invention.

### 1.3 UptimeRobot importer — spec filed
`../specs/todos/2026-08-21-11-uptimerobot-importer.md`. Completes the planned
Kuma → UptimeRobot → BetterStack trio (the other two shipped); largest SaaS
user base, cleanest API export.

---

## Priority 2: Product depth

### 2.1 SLO burn-rate alerting — spec filed
`../specs/todos/2026-08-21-08-slo-burn-rate-alerting.md`. The `burnRate` field
was deliberately laid as this feature's foundation; multiwindow fast/slow burn
alerts flow through the existing incident/escalation pipeline (a burn opens an
incident, so ack, severity routing, and escalation all reuse).

Still open from the SLA work, not blocked by it: rolling windows, per-region
objectives, public SLA sections on status pages, CSV/PDF report attachments.

### 2.2 Traceroute/MTR failure diagnostics — spec filed
`../specs/todos/2026-08-21-10-traceroute-failure-diagnostics.md`. The
screenshots work built the whole attachment pipeline (capture LRU, agent
upload frames, incident display); an MTR-style trace on transition-to-down is
the natural second payload, and BetterStack's other diagnostics bullet.

---

## Priority 3: Enterprise maturity & cleanups

### 3.1 Audit-log coverage for auth & config events — spec filed
`../specs/todos/2026-08-21-09-audit-log-auth-and-config-events.md`. The
`events` table is still check/incident-centric; ISO/SOC2 buyers ask for
login/SSO, membership, token, and config events. The entitlements audit trail
is the in-repo pattern. SIEM/syslog export is a deliberate follow-up, not in
the spec.

### 3.2 On-call maturity
Business-hours restriction windows, layered/concurrent rotations, per-user
quiet hours / holiday mode. Backlog spec:
`../specs/backlog/2026/05/2026-05-08-05-on-call-multi-rotation-and-override-as-event.md`;
design notes in `research/alerting-patterns.md` §5.

### 3.3 Terraform provider import fixes
The provider lives out of tree (`terraform-provider-solidping`); the API audit
(`terraform-provider-api-audit.md`) found 2 of 5 resources are slug-only and
break `terraform import org/uid`. Fix the API gaps here, then verify the
provider's published state.

### 3.4 Subchecks
Auto-spawn SSL/domain-expiration sub-checks from a parent HTTP check. Spec
stub in `../specs/ideas/2026-01-01-subchecks.md`.

### 3.5 Initial-result semantics cleanup
Carried over (`../specs/backlog/2026-03-23-check-started-result.md`) — the
`initial` result's neutrality w.r.t. status, streaks, and incidents isn't
consistently enforced. Small, fixes a class of edge-case bugs around
re-enabled checks.

### 3.6 Decide the outage-vs-incident split's fate
The publication overlay shipped the split's user-facing goals (auto-publish,
manual incidents, public/internal boundary) without the schema-level split.
`../specs/backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md` is now
partially superseded — rescope it to whatever schema work is still worth doing
(retention churn, postmortems) or archive it, before piling more features onto
the `incidents` table.

---

## Explicit non-priorities (for now)

- **RUM, page speed / Core Web Vitals, AIOps anomaly detection** — different
  ingestion pipelines, different competitors; none of them blocks a sale the
  way P1/P2 items do.
- **Native mobile apps** — the installable PWA + Web Push already cover phone
  alerts; revisit only if push reliability on iOS becomes a complaint.
- **Heartbeat enhancements** (`/start` endpoint, exit codes, log attachment) —
  partially shipped (bearer auth, JSON body, caller metadata); the rest
  matters to cron-power-users specifically, not to evaluations.
- **NATS notifier / dropping SQLite** — revisit when scale or maintenance
  drain demands it (`../specs/ideas/2025-12-28-nats-notifier.md`,
  `../specs/ideas/2025-12-28-drop-sqlite.md`).

---

## Cross-cutting considerations

- **The notification-channel matrix is essentially complete** (14+ native
  channels). The ~250-line-per-channel pattern held up through the August
  additions; the codegen/template idea from July was never needed and is
  dropped. New investment goes into channel *depth* (bots, threads, ack
  actions — the Discord spec is the template), not breadth.
- **Subscriber endpoints are PII.** Webhook/Slack subscription URLs and
  uptime-report recipients get the same encrypt-at-rest opacity as subscriber
  emails and check credentials.
- **Importers, SLA reports, and the Terraform provider all depend on a stable
  REST API.** The API is stable; new endpoints follow the same shape
  (camelCase, `data` envelope, `$uid` paths) — see `../CLAUDE.md`.
