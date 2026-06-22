# Admin "Jobs" page: live observability for background jobs + check schedule

## Context

SolidPing runs two completely different "job" subsystems, and today an operator
has **no in-app way to see either one**:

1. **Background jobs** — the generic async task queue.
   - Model: `server/internal/db/models/job.go` (`Job`, `JobStatus`).
   - Types (`server/internal/jobs/jobdef/types.go`): `sleep`, `email`, `webhook`,
     `startup`, `aggregation`, `state_cleanup`, `notification`, `snooze_sweep`,
     `escalation_step`, `network_discovery`, `network_discovery_plan`,
     `freebox_lan_discovery`.
   - States: `pending → running → success | retried | failed`. Retries chain via
     `previous_job_uid` (max 2 retries, exponential backoff: 1m / 5m / 15m —
     `server/internal/jobs/jobsvc/service.go`).
   - Table `jobs`: `uid, organization_uid (NULL for system jobs), type, config,
     output, status, retry_count, scheduled_at, previous_job_uid, created_at,
     updated_at, deleted_at`.
   - REST: org-scoped only — `GET/POST /api/v1/orgs/:org/jobs`,
     `GET/DELETE /api/v1/orgs/:org/jobs/:uid`
     (`server/internal/handlers/jobs/handler.go`), gated `RequireAuth` +
     `RequireOrgAccess` (member, **not** admin). No system-wide view, so the
     many `organization_uid = NULL` jobs (aggregation, state_cleanup,
     snooze_sweep) are invisible in the UI.

2. **Check schedule** — the distributed lease table that dispatches check
   executions to workers.
   - Model: `server/internal/db/models/check_job.go` (`CheckJob`). One row per
     check per region. **No status column** — state is derived from lease
     fields: `lease_worker_uid`, `lease_expires_at`, `lease_starts`,
     `scheduled_at`, `period`.
   - Derived states: **idle/unleased** (no lease), **in-flight/leased**
     (`lease_expires_at > now`), **stalled** (`lease_expires_at < now`),
     **crash-looping** (`lease_starts > 9`). Claimed via
     `SELECT … FOR UPDATE SKIP LOCKED` (`checkjobsvc/service.go`).
   - Table `check_jobs`: `uid, organization_uid, check_uid, region, type, config,
     config_private (encrypted secrets), config_private_keys, encrypted, period,
     scheduled_at, lease_worker_uid, lease_expires_at, lease_starts, updated_at`.
   - REST: **none for reading** — only worker endpoints (`/api/v1/workers/*`)
     touch it. There is no admin-facing list/detail at all.

Today the only visibility into either subsystem is Prometheus
(`server/internal/prommetrics/metrics.go`) — and even that only covers check
execution; **background-job queue depth/health is not exported anywhere**. For a
self-hosted product, an in-app view is the practical way to answer "is the queue
healthy / is the server busy?" without standing up Grafana.

There is already a strong precedent for everything this page needs: the
admin-only **Discovery** page is gated in the sidebar by `user?.isAdmin`
(`web/dash0/src/components/layout/AppSidebar.tsx`), and `useDiscoveryScan`
(`web/dash0/src/api/hooks.ts:3317`) does **adaptive polling** — 3s while a scan
is active, off when idle.

## Goal

Add an **admin-only "Jobs" page** in the left nav (admin section, beside
Discovery) that gives operators a live, at-a-glance view of both subsystems and
lets them drill into any single job. **Read-only in v1** (no retry/cancel/
reschedule — see Out of scope). The page **adapts its refresh rate** to server
activity so it visibly reflects how busy the instance is.

**Scope: org view by default, with a super-admin toggle.** A normal org admin
(`isAdmin`) sees their org's jobs and check schedule. A super-admin
(`isSuperAdmin`) gets a "This org / All orgs (system)" toggle that switches the
data to instance-wide endpoints, surfacing every org plus the
`organization_uid = NULL` system jobs.

## Behaviour

### 1. Navigation + access gating
- New sidebar entry in the existing `{user?.isAdmin && (…)}` admin
  `SidebarGroup` (`AppSidebar.tsx`), below Discovery. Lucide icon (pick an
  unused one — suggest `Workflow` or `Activity`), `titleKey: "jobs"`, links to
  `/orgs/$org/jobs`, `isActive` when `pathname.startsWith('/orgs/$org/jobs')`.
- Route layout `jobs.tsx` mirrors the `organization.tsx` admin guard: if
  `!user?.isAdmin` after auth loads, `navigate({ to: "/orgs/$org", replace })`.
  (403, never a redirect loop — per `wiki/conventions/frontend-errors.md`.)

### 2. List page `/orgs/$org/jobs` (`jobs.index.tsx`)
A header + an **activity overview** + **two tabs**.

- **Activity overview strip** (always visible, top of page) — small stat tiles
  fed by the stats endpoint (§5):
  - Background jobs: **pending**, **running**, **failed (24h)**.
  - Check schedule: **scheduled (total)**, **due now** (`scheduled_at <= now`,
    unleased), **in-flight** (leased, unexpired), **crash-looping**
    (`lease_starts > 9`).
  - This strip is the "overall activity of the server" surface and the input to
    adaptive refresh (§6).
- **Tabs** (use the shipped Tabs primitive from the design reference):
  - **Background jobs** — `Table` of recent jobs: type (badge), status
    (status badge), org (only in all-orgs mode), scheduled (relative), retries,
    updated (relative). Filters: status (`pending/running/success/retried/
    failed`) and type. Default filter excludes nothing but sorts active first
    (pending/running) then most-recent `updated_at`. Paginated (reuse the
    existing list-page pagination pattern; the `jobs` table accumulates `success`
    rows, so **never** unbounded-fetch — default page size 50). Row → background
    job detail.
  - **Check schedule** — `Table` of `check_jobs`: check name (links to the
    check), region, derived state badge (idle / in-flight / stalled /
    crash-looping), period, next run (`scheduled_at`, relative), lease worker +
    expiry (when leased), attempts (`lease_starts`). Bounded by check count, but
    still paginate. Row → check-job detail.
- **Super-admin toggle**: rendered only when `isSuperAdmin`. A segmented
  "This org / All orgs (system)" control. In all-orgs mode the queries hit the
  `/system/*` endpoints (§5), an **Org** column appears in both tables, and
  system jobs (`organization_uid = NULL`) show an "system" org label.
- Status → badge variant mapping (reuse `StatusBadge`/`Badge`): `success` →
  success, `running` → default/blue, `pending` → secondary, `retried` →
  warning, `failed` → destructive. Check-schedule: idle → secondary, in-flight →
  default, stalled → warning, crash-looping → destructive.

### 3. Background-job detail `/orgs/$org/jobs/$jobUid` (`jobs.$jobUid.tsx`)
Header (type + status badge + back arrow per the header convention), then:
- Meta: org, type, status, scheduled, created, updated, retry count.
- **Config** (`config` JSONB) and **Output** (`output` JSONB) rendered read-only
  (monospace / pretty-printed JSON block).
- **Retry chain**: follow `previous_job_uid` backward and forward (jobs whose
  `previous_job_uid` is this one) into a small ordered list, each linking to its
  own detail — so an operator can trace a failed→retried→failed chain.
- In all-orgs (super-admin) mode the detail uses the `/system/jobs/:uid`
  endpoint; otherwise the org endpoint.

### 4. Check-job detail `/orgs/$org/jobs/check/$checkJobUid` (`jobs.check.$checkJobUid.tsx`)
(`check` is a static segment; TanStack resolves it ahead of `$jobUid`.)
- Header + back arrow.
- Meta: linked check (name → `/orgs/$org/checks/$checkUid`), region, type,
  period, next run, derived state, lease worker, lease expiry, attempts
  (`lease_starts`), last updated.
- **Config** (`config`) shown read-only. **Secrets are never exposed**: the
  endpoint must omit `config_private` / `config_private_keys` entirely and only
  indicate *which* keys are encrypted (the key names), never values.

### 5. Backend endpoints (new)
All read-only. JSON wrapped in `{ "data": … }`, camelCase, `$uid` paths.

- **Stats** (feeds the overview strip + adaptive refresh):
  - `GET /api/v1/orgs/:org/jobs/stats` (org admin)
  - `GET /api/v1/system/jobs/stats` (super-admin)
  - Returns counts: background `{pending, running, failed24h, …}` and check
    schedule `{total, dueNow, inFlight, stalled, crashLooping}`. One cheap
    `GROUP BY status` for jobs + a few counting queries for check_jobs.
- **Check schedule list/detail** (new service + handler,
  `server/internal/handlers/...`):
  - `GET /api/v1/orgs/:org/check-jobs` (org admin) — paginated, with derived
    state computed server-side; **redacts** `config_private*`.
  - `GET /api/v1/orgs/:org/check-jobs/:uid` (org admin) — detail, redacted.
  - `GET /api/v1/system/check-jobs` + `/:uid` (super-admin) — all orgs.
- **Background jobs system view**:
  - `GET /api/v1/system/jobs` + `GET /api/v1/system/jobs/:uid` (super-admin) —
    across all orgs incl. `organization_uid = NULL`. The existing org-scoped
    `GET /orgs/:org/jobs` is reused for org mode.
- **Authorization**: the page is admin-only, so its org endpoints must require
  **org admin**, not just membership. There is no `RequireOrgAdmin` middleware
  yet — add one (or an inline `organization_members.role == "admin"` check after
  `RequireOrgAccess`) and apply it to the new org endpoints. System endpoints use
  the existing `RequireSuperAdmin` (`middleware/auth.go`). The existing
  member-level `GET /orgs/:org/jobs` stays as-is for backward compatibility.

### 6. Adaptive refresh
Mirror `useDiscoveryScan`'s function-form `refetchInterval`
(`hooks.ts:3326`). New hooks (`useJobsStats`, `useBackgroundJobs`,
`useCheckSchedule`) compute activity from the latest stats:
`active = pending + running + inFlight + dueNow > 0`.
- **Active** → poll fast (**2.5s**) so the page visibly tracks a busy server.
- **Idle** → slow poll (**15s**) rather than off (unlike Discovery), so new
  activity still surfaces without manual reload.
- `refetchIntervalInBackground: false` so a hidden tab stops hammering the API.

### 7. Design reference
Per the mandatory convention, anything new (e.g. a read-only JSON viewer block
or a compact stat tile, if not already shipped) is added to
`web/dash0/src/routes/orgs/$org/design-reference.tsx` with its import line.
Reuse `Table`, `Badge`, `StatusBadge`, `Card`, `Tabs`, `Skeleton`, `Button`
that already exist there.

### 8. i18n
Add a `jobs` translation namespace (page title, tab labels, column headers,
state labels, toggle labels, empty states) to **all** locales
(`web/dash0/src/locales/{en,fr,es,de}/`), plus the `jobs` nav key.

## Out of scope
- **Write actions** (retry failed, cancel pending, reschedule, force-run) — a
  follow-up spec. `DELETE /orgs/:org/jobs/:uid` already exists but is not wired
  into this read-only page.
- Prometheus/metrics changes — filed separately as
  [2026-06-15-06-export-background-job-metrics-prometheus.md](2026-06-15-06-export-background-job-metrics-prometheus.md)
  (alerting-grade complement: this page is for humans, those metrics are for
  Grafana/alerting).
- Any change to worker claim/lease logic or job execution.
- Exposing or decrypting `config_private` secrets — names only, never values.
- `web/dash` (legacy dashboard) — untouched.

## Testing
- **Backend** (table-driven + testcontainers, per `server/CLAUDE.md`):
  - New check-jobs list/detail + stats endpoints: derived-state computation,
    pagination, `{data}` wrapping.
  - **Authorization matrix**: viewer/user → 403 on admin endpoints; org admin →
    own org only; non-super-admin → 403 on `/system/*`; super-admin → all orgs +
    `org=NULL` jobs.
  - **Secrets redaction**: a check job with `config_private` set returns no
    secret values (only encrypted key names) from both list and detail.
- **dash0 Playwright** (`web/dash0/e2e/`):
  - Admin sees the Jobs nav entry; non-admin does not and `/orgs/$org/jobs`
    redirects to the org home.
  - List renders seeded background jobs (varied statuses) and check-schedule
    rows with correct state badges; tab switch works; status/type filters work.
  - Super-admin sees the "All orgs (system)" toggle and the Org column; a normal
    org admin does not.
  - Detail pages: background-job config/output/retry-chain; check-job links to
    its check; no secret values rendered.
- **Manual**: `make dev-test`, `/dash0/orgs/test/jobs`, desktop + mobile
  (tables scroll, tabs usable, touch targets), light + dark. Trigger activity
  (create checks / send a test notification) and confirm the refresh visibly
  speeds up while jobs are pending/running and the overview counts move.

## Implementation Plan
> This reads like "add a page" but is **backend-first**: `check_jobs` has no read
> API today and there is no org-admin gate. Build and test steps 1–3 before any
> UI; do not let this be scoped as a pure frontend task.

1. **Backend — check-jobs read API**: service method(s) to list/get `check_jobs`
   with derived state + redaction; handler under `server/internal/handlers/`;
   register org + `/system` routes in `server/internal/app/server.go`.
2. **Backend — stats + system jobs**: `…/jobs/stats` (org + system) and
   `/system/jobs` (+`/:uid`); `RequireOrgAdmin` middleware (or inline role
   check) and super-admin gating.
3. **Backend tests**: endpoints, auth matrix, redaction.
4. **Frontend hooks** (`api/hooks.ts`): `useJobsStats`, `useBackgroundJobs`,
   `useCheckSchedule`, `useBackgroundJob`, `useCheckJob`, all with adaptive
   `refetchInterval` and an `allOrgs` flag selecting org vs `/system` URLs.
5. **Frontend routes**: `jobs.tsx` (admin guard), `jobs.index.tsx` (overview +
   tabs + super-admin toggle + filters + pagination), `jobs.$jobUid.tsx`,
   `jobs.check.$checkJobUid.tsx`.
6. **Sidebar**: add the admin nav entry; design-reference additions if any.
7. **i18n**: `jobs` namespace across all locales + nav key.
8. **E2E** per Testing; then `bun run lint`, `make test-dash`, `make test`,
   manual mobile + dark-mode pass.

## Implementation Plan (detailed — by implementing agent)

Backend uses **bunrouter** + **Bun ORM**; handler signature is
`func(http.ResponseWriter, bunrouter.Request) error`. Org is resolved by
`RequireOrgAccess` into context (`middleware.GetOrganizationFromContext`) — handlers
must use the resolved `org.UID` (the `:org` URL param is the **slug**, not the UID).

### Step 1 — Backend: check-jobs read service + handler
- New package `server/internal/handlers/checkjobs/` with `service.go` + `handler.go`.
- `Service.ListCheckJobs(ctx, orgUID, opts)` and `GetCheckJob(ctx, uid)` (org-scoped
  when orgUID non-empty; all-orgs when empty for system mode). Query `check_jobs`,
  order by `scheduled_at`, paginate (limit/offset). Eager-load the `Check` for name.
- `DerivedState(cj, now)` helper: idle / in-flight / stalled / crash-looping.
- **Redaction**: build a `CheckJobView` DTO that NEVER includes `config_private`/
  `config_private_keys` values — only `encryptedKeys []string` (the key names parsed
  from `config_private_keys` JSON) + `encrypted bool`. JSON wrapped `{data: ...}`.
- Handler reads org from context, super-admin gated handlers omit org filter.

### Step 2 — Backend: stats + system jobs + org-admin gate
- Stats DTO: background `{pending, running, failed24h}` + check schedule
  `{total, dueNow, inFlight, stalled, crashLooping}`. One `GROUP BY status` for jobs
  (last 24h for failed) + counting queries for check_jobs.
- New `server/internal/handlers/jobsadmin/` (stats + system jobs view) OR extend the
  checkjobs service with stats. Keep jobs system-view reusing `jobsvc.ListJobs` with
  empty orgUID (add an all-orgs path) + a stats method.
- **`RequireOrgAdmin` middleware** added to `middleware/auth.go`: after RequireOrgAccess,
  load member via `GetMemberByUserAndOrg(user.UID, org.UID)`; allow if
  `member.Role == admin` OR `user.SuperAdmin`. 403 otherwise.
- Register routes in `server.go`:
  - org: `GET /orgs/:org/jobs/stats`, `GET /orgs/:org/check-jobs`,
    `GET /orgs/:org/check-jobs/:uid` (RequireAuth + RequireOrgAccess + RequireOrgAdmin).
  - system: `GET /system/jobs`, `GET /system/jobs/:uid`, `GET /system/jobs/stats`,
    `GET /system/check-jobs`, `GET /system/check-jobs/:uid` (RequireAuth + RequireSuperAdmin).

### Step 3 — Backend tests
- `service_test.go` + `handler_test.go` (testcontainers, table-driven, `t.Parallel()`,
  `testify/require`). Cover derived-state, pagination, `{data}` wrap, redaction
  (config_private set → no secret values), auth matrix (viewer/user → 403 on admin
  endpoints; non-super-admin → 403 on /system; super-admin → all orgs + NULL jobs).

### Step 4 — Frontend hooks (`web/dash0/src/api/hooks.ts`)
- `useJobsStats(org, {allOrgs})`, `useBackgroundJobs(org, {allOrgs, status, type, ...})`,
  `useCheckSchedule(org, {allOrgs, ...})`, `useBackgroundJob(org, uid, {allOrgs})`,
  `useCheckJob(org, uid, {allOrgs})`. URL selects `/system/*` vs `/orgs/:org/*` by
  `allOrgs`. Adaptive `refetchInterval`: 2500ms when active
  (`pending+running+inFlight+dueNow>0`), else 15000ms; `refetchIntervalInBackground:false`.

### Step 5 — Frontend routes (TanStack file-based)
- `jobs.tsx` (admin guard + Outlet), `jobs.index.tsx` (overview strip + Tabs +
  super-admin toggle + filters + pagination), `jobs.$jobUid.tsx` (bg-job detail),
  `jobs.check.$checkJobUid.tsx` (check-job detail). Run codegen for routeTree.

### Step 6 — Sidebar + design-reference
- Add admin nav entry (Workflow icon) below Discovery in `AppSidebar.tsx`.
- Add any new primitive (read-only JSON block / stat tile) to `design-reference.tsx`
  if not already shipped; otherwise reuse Card/Badge/Table/StatusBadge.

### Step 7 — i18n
- `jobs.json` in en/fr/es/de + `jobs` key in each `nav.json`; register in `i18n.ts`.

### Step 8 — E2E + QA
- Playwright specs in `web/dash0/e2e/`; then build-backend, build-client, lint-back, test.
