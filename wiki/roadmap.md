# Features Roadmap

> **Status**: Snapshot of priorities as of **2026-07-28**. Replaces the May 2026
> snapshot, which is now almost fully shipped. Pull this file forward whenever
> priorities shift; don't archive it as historical reference.

## Where we are

Nearly everything from the May snapshot has shipped. SolidPing now has 38 public
check types (39 registered, including the internal `sleep` type), **11 native
notification channels** (including Twilio SMS + outbound voice wakeup calls with
magic-link acknowledge), multi-region distributed workers with per-region periods
and region spread, private locations — org-owned **and** platform-operated system
agents — SSH tunnels for 18 check types, status pages with **custom domains
(single CNAME + automatic Let's Encrypt TLS)**, custom CSS with live preview,
double-opt-in email subscribers and an Atom feed, maintenance windows with
recurrence, group-incident correlation, on-call schedules + multi-step escalation
policies + severity-gated channel routing, ack/snooze/manual-resolve with
magic-link ack, credentials encryption at rest, configuration as code (YAML
export/import/apply), automatic discovery (network scan, Docker, Kubernetes,
Freebox LAN) with promote-to-check, an MCP server with 39 tools, SSO (OIDC, SAML,
LDAP + 6 OAuth providers), passkeys, a full OAuth 2.1 authorization server,
realtime WebSocket updates, per-org entitlements with check-rate token buckets and
plan-weighted scheduler fairness, and a 146-path public API.

Carried over from May and still genuinely open: **Telegram / MS Teams / PagerDuty
channels**, **importers**, **screenshots**, **subchecks**.

The feature-matrix war is won — see `competitors/comparison/` for the full
✅/❌ inventory against 8 competitors (`competitors/comparison/solidping-position.md`
for the Tier 1/2/3 breakdown). The remaining gaps sort into three questions:

1. **Can an evaluating team switch?** — channels they already page with, importers.
2. **Does the product feel finished?** — incidents that reach the status page by
   themselves, status-page branding, failure diagnostics.
3. **Will someone pay?** — white-label, SLA reporting, enterprise audit trail.

---

## Priority 1: Adoption blockers

### 1.1 PagerDuty, Microsoft Teams, Telegram notification channels
**Why P1**: The only gap where every SaaS competitor ships something we don't.
PagerDuty matters most strategically — a team evaluating SolidPing mid-migration
won't drop their pager on day one, so the integration is the adoption bridge, not
a nice-to-have. Specs are drafted (`../specs/ideas/2026-03-22-telegram-notifications.md`,
`../specs/ideas/2026-03-22-notification-channels.md`); the Slack/Discord OAuth work
proved the pattern and Google Chat/Mattermost proved the webhook-card pattern
Teams needs.

**Order**: Telegram → MS Teams → PagerDuty. PagerDuty last because it's the only
one with a different model (Events API v2 routing keys + dedup keys).

**Dependencies**: None — senders plug into `server/internal/notifications/registry.go`.

### 1.2 Auto-publish incidents to status pages — the outage-vs-incident split
**Why P1**: The biggest product-coherence gap. Automatic incidents never surface
on the public status page — status updates are hand-authored, so when a check goes
down at 3am the status page says everything is fine until a human writes an
update. Every serious status-page competitor auto-displays outages.

**Scope**: Implement the split from
`../specs/backlog/2026/05/2026-05-08-04-outage-vs-incident-split.md` — operational
**outages** (probe data, paging state, ack verbs, fast retention churn) separated
from customer-facing **incidents** (edited title/timeline, status-page attachment,
public URL). This also unlocks manual incident creation (there is currently no
`POST /incidents`), postmortems, and clean public-vs-internal webhook events.
Design input distilled in `research/alerting-patterns.md`.

**Dependencies**: None, but it's a schema-level change — do it before piling more
features onto the `incidents` table.

### 1.3 Importers: Uptime Kuma → UptimeRobot → BetterStack
**Why P1** (was P2): Nothing else converts existing users, and the config-as-code
apply endpoint gives importers a natural target format. Uptime Kuma is at ~89k★
on its 2.x line, and its users' top complaints — no REST API, no multi-location,
no multi-user/SSO, no PostgreSQL — are exactly SolidPing's differentiators. A user
with 50 monitors elsewhere will not migrate by hand.

**Order**: Uptime Kuma (JSON export, simplest, largest self-hosted overlap) →
UptimeRobot (largest SaaS user base, good API) → BetterStack (most complex,
highest-value migration). Spec stub in `../specs/ideas/2025-12-28-importers.md`.

**Dependencies**: None — emit config-as-code YAML and reuse `checks/apply`.

---

## Priority 2: Product completeness

### 2.1 Status-page trust & branding
**Why P2**: Three concrete holes that show up on every buyer's comparison
checklist (Instatus, Hyperping, BetterStack all ship them):
- **Logo/favicon upload** — theming is CSS-variables only today; the SolidPing
  logo and "powered by solidping.io" footer are hardcoded in
  `web/status0/src/components/shared/status-page-view.tsx`. White-label (remove
  the badge) is a natural paid-tier entitlement for the SaaS.
- **Password-protected pages** — `visibility: private` just 404s the public
  endpoint, so there is no way to share a status page with customers privately.
- **Subscription channels beyond email + Atom** — webhook and Slack subscribe are
  the most-requested next steps.

**Dependencies**: File storage for logo upload already exists (local FS / S3).

### 2.2 Failure diagnostics — screenshots, then traceroute/MTR
**Why P2**: BetterStack's primary sales bullet ("see what your users saw when it
broke"). Unusually cheap for its demo impact: the spec is ready
(`../specs/ideas/2026-01-05-screenshots.md`), Rod was chosen during the
browser-checks work, and the stated blocker — S3-compatible object storage — has
shipped. Traceroute/MTR capture on network-level failures is the natural
follow-up (also a BetterStack differentiator).

**Dependencies**: None remaining.

### 2.3 SLA reporting — SLO targets, error budgets, scheduled uptime reports
**Shipped** (spec `2026-08-20-01`). Per-check and per-group objectives with a
calendar-month error budget in the objective's own timezone, a burn rate and a
projected-exhaustion readout, monthly history off the permanent month rollups,
and weekly/monthly emailed uptime digests. Planned maintenance is excluded from
the objective's denominator via ingest-time `results.maintenance` tagging — the
part that had to ship first, because rollup buckets cannot be sliced after the
fact. API: [`api-specification/slos.md`](api-specification/slos.md).

**Still open**: burn-rate *alerting* through the escalation pipeline
(multiwindow fast/slow burn — the `burnRate` field is deliberately its
foundation), rolling windows, per-region objectives, public SLA sections on
status pages, and CSV/PDF report attachments.

---

## Priority 3: Enterprise maturity & cleanups

### 3.1 Audit-log coverage for auth & config events
The `events` table is check/incident-centric. ISO/SOC2-minded buyers will ask for
login/SSO events, member and role changes, integration changes, and token
lifecycle events. The entitlements audit trail already exists separately and can
serve as the pattern.

### 3.2 On-call maturity
Business-hours restriction windows, layered/concurrent rotations, per-user quiet
hours / holiday mode. Backlog spec:
`../specs/backlog/2026/05/2026-05-08-05-on-call-multi-rotation-and-override-as-event.md`;
design notes in `research/alerting-patterns.md` §5.

### 3.3 Terraform provider import fixes
The provider lives out of tree (`terraform-provider-solidping`); the API audit
(`terraform-provider-api-audit.md`) found 2 of 5 resources are slug-only and
break `terraform import org/uid`. Fix the API gaps here, then verify the
provider's published state.

### 3.4 Subchecks
Auto-spawn SSL/domain-expiration sub-checks from a parent HTTP check. Spec stub
in `../specs/ideas/2026-01-01-subchecks.md`.

### 3.5 Initial-result semantics cleanup
Carried over from May (`../specs/backlog/2026-03-23-check-started-result.md`) —
the `initial` result's neutrality w.r.t. status, streaks, and incidents isn't
consistently enforced. Small, fixes a class of edge-case bugs around re-enabled
checks.

---

## Explicit non-priorities (for now)

- **RUM, page speed / Core Web Vitals, AIOps anomaly detection** — different
  ingestion pipelines, different competitors; none of them blocks a sale the way
  P1/P2 items do.
- **Native mobile apps** — the installable PWA + Web Push already cover phone
  alerts; revisit only if push reliability on iOS becomes a complaint.
- **Heartbeat enhancements** (`/start` endpoint, exit codes, log attachment) —
  partially shipped (bearer auth, JSON body, caller metadata); the rest matters
  to cron-power-users specifically, not to evaluations.
- **NATS notifier / dropping SQLite** — revisit when scale or maintenance drain
  demands it (`../specs/ideas/2025-12-28-nats-notifier.md`,
  `../specs/ideas/2025-12-28-drop-sqlite.md`).

---

## Cross-cutting considerations

- **The notification-channel pattern is the bottleneck for adding more.** Every
  new channel is ~250 lines + a sender + UI form + i18n. P1.1 adds three — invest
  in a tiny code-gen/template as part of that work.
- **Group-incident correlation changed the calculus on alert storms.** One outage
  = one alert per channel (not N), so adding channels is no longer dangerous from
  an alert-fatigue standpoint.
- **Credentials encryption raised the bar for security claims.** Subscriber email
  addresses are PII; new subscription channels (webhook, Slack) and uptime-report
  recipients must be treated with the same opacity.
- **Importers, SLA reports, and the Terraform provider all depend on a stable
  REST API.** The API is stable; new endpoints follow the same shape (camelCase,
  `data` envelope, `$uid` paths) — see `../CLAUDE.md`.
