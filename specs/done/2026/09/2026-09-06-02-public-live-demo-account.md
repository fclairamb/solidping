---
model: opus
effort: high
---

# Evaluators cannot see or touch a working SolidPing without signing up

## Problem

There is no way to experience the product before creating an account. The
marketing site links to a showcase video (`web/docs/static/showcase/`,
`wiki/features/showcase-media.md`) and the docs describe the features, but a
prospect who wants to *see* a mature account — weeks of multi-region history,
a live incident, an escalation firing, a status page, SLOs burning — or to
*feel* the core loop — create a check, watch results arrive from three
continents inside a minute — has to sign up first. That is the wrong order for
top-of-funnel: the free tier exists to keep people, the demo exists to get them
to try.

This has been deferred once already: the activation spec explicitly parked a
"demo data mode … for evaluators" as *"worth doing later, separate spec"*
([2026-05-05-03](../done/2026/05/2026-05-05-03-activation-time-to-first-signal.md:86)),
and the original auth spec seeded a `demo@solidping.com` user and a `pat_demo`
token that were later removed
([2025-12-07-auth.md:380](../done/2025/12/2025-12-07-auth.md:380)). A
half-built remnant survives: `SP_RUN_MODE=demo` maps to the
`checkerdef.Demo` sample type in
[job_startup.go:313](../../server/internal/jobs/jobtypes/job_startup.go:313)
and
[checkerdef/types.go:393](../../server/internal/checkers/checkerdef/types.go:393),
but no checker's `GetSampleConfigs` branches on it, so it is a dead enum value.

### Two options were weighed, and a third was picked

1. **A separate `demo.solidping.io` instance in a "demo mode".** Isolated, but
   it cannot show the one thing that sells the product: the shared
   multi-region fleet. A check worker talks to exactly one control plane
   (`workers` carries no `organization_uid`,
   [wiki/conventions/regions.md](../../wiki/conventions/regions.md)), so a
   second instance either duplicates us1/eu2/jp-1 and the agents, or runs
   from a single region. It also starts with zero history, adds a ninth
   manual image pin to a fleet that is already shipped by hand, and a second
   Postgres on a cluster where memory is tight.
2. **A shared `demo` login on the production server, writable, with a
   cleanup job.** Right idea, wrong safety model *if* built as a denylist:
   protecting every seeded resource and guarding every mutating endpoint is a
   large surface inside prod auth, and every future endpoint would have to
   remember it.
3. **This spec: the shared production server, a real `demo` org and user
   through the standard login flow, a positive allowlist of what a demo
   session may write, entitlement caps doing the load bounding, and a
   30-minute cleanup.** The allowlist is four routes. The load concern is
   already solved by infrastructure built for other reasons: `MaxChecks` at
   create time
   ([entitlements/usage.go:246](../../server/internal/entitlements/usage.go:246))
   and `MaxChecksPerMinute` at the worker
   ([checkworker/worker.go:1435](../../server/internal/checkworker/worker.go:1435))
   bound the whole demo org regardless of how many visitors show up.

### Two facts found while scoping that shape the design

- **Checks have no `created_by`.** `models.Check`
  ([check.go:396](../../server/internal/db/models/check.go:396)) records
  neither who created it nor a session; `maintenance_window`, `on_call_schedule`,
  `agent` and `file` do carry a `created_by` column. "Edit/delete only what you
  created" therefore needs one new nullable column.
- **The `viewer` role is not enforced as read-only anywhere.**
  `RequireOrgAccess`
  ([middleware/auth.go:217](../../server/internal/middleware/auth.go:217))
  has no role logic, no checks handler calls `HasOrgRole`, and the only
  references to `MemberRoleViewer` outside `models/auth.go` are the two places
  that *assign* it. A viewer can create and delete checks today. The demo guard
  must therefore be its own middleware and never lean on the role. Fixing the
  viewer gap itself is a separate spec.

## Proposal

### 1. Identity: a real org, a real user, the standard login

- **Org `demo`** (slug configurable), created by the startup job next to
  `default` ([job_startup.go:230](../../server/internal/jobs/jobtypes/job_startup.go:230)),
  idempotent by slug. Flagged with the org parameter `demo.enabled = true`
  (`SetOrgParameter`, [postgres.go:4490](../../server/internal/db/postgres/postgres.go:4490)).
- **User `demo@solidping.io`**, password `demo` (both configurable), created
  idempotently by email. Membership in `demo` with role **`user`** — not
  `viewer`, because check creation is the point and because the role is not a
  safety mechanism (see above). Never `SuperAdmin`, never
  `MustChangePassword` (the seeded admin sets it at
  [job_startup.go:253](../../server/internal/jobs/jobtypes/job_startup.go:253);
  a forced rotation would land on a blocked endpoint and dead-end the demo).
- **New column `users.demo boolean not null default false`**, set on this
  user. Migrations for both backends
  (`server/internal/db/postgres/migrations`, `server/internal/db/sqlite/migrations`;
  `/sync-pg-to-sqlite` exists for the second).
- **Claims carry it**: `Claims`
  ([handlers/auth/service.go:170](../../server/internal/handlers/auth/service.go:170))
  gains a `Demo bool` field (JSON `demo`, omitted when false), populated wherever claims are
  minted from a user row — password login, OAuth login, refresh, PAT
  validation, 2FA completion. A PAT belonging to the demo user is therefore a
  demo session too, which is what makes a published demo API key safe later.
- **Session cap**: org parameter `auth.session_max_duration = 3600` on the
  demo org, resolved by the existing per-org path
  ([systemconfig.go:63](../../server/internal/systemconfig/systemconfig.go:63)).
  Visitors get an hour, then log in again with one click.
- **Login is the normal `POST /auth/login`**
  ([server.go:664](../../server/internal/app/server.go:664)) with
  `{org: "demo", email, password}`. No dedicated endpoint, no session-minting
  shortcut: the credentials are public by design, and standard auth gives the
  demo user OAuth-adjacent flows, PATs and the MCP server for free.

### 2. The guard: deny every write, allow four things

A `DemoGuard` step inside `RequireAuth`
([middleware/auth.go:55](../../server/internal/middleware/auth.go:55)), right
after the claims are parsed. `RequireAuth` is the one chokepoint every
authenticated route passes through — the `orgGroup` helper
([server.go:767](../../server/internal/app/server.go:767)), the admin/owner
groups that spell the chain out by hand, and the user-level
`rootAuthProtected` routes ([server.go:688-699](../../server/internal/app/server.go:688)).
Putting it anywhere narrower reintroduces the "forgot one group" failure the
`orgGroup` comment describes.

Rule, in order:

1. `!claims.Demo` → pass.
2. Method `GET`, `HEAD`, `OPTIONS` → pass.
3. Matched route in the **allowlist** → pass:
   - `POST /auth/logout`
   - `POST /orgs/:org/checks` (create)
   - `POST /orgs/:org/checks/validate`
   - `PATCH` / `DELETE /orgs/:org/checks/:checkUid` and
     `POST /orgs/:org/checks/:checkUid/clone` — the guard lets them through;
     **ownership** is enforced in the checks service (§3).
   - The check diagnostics routes (`handlers/tracediag`) if they are
     non-GET — they are read-only probes.
4. Everything else → `403` with a new code `DEMO_READ_ONLY`
   (`handlers/base/base.go`, next to `FORBIDDEN` at
   [base.go:25](../../server/internal/handlers/base/base.go:25)), title
   *"Read-only in the demo"*.

Match on the router's route pattern, not the raw path, so `:checkUid` and
slug redirects cannot be used to slip past. This single rule covers
`PATCH /auth/me`, `POST /auth/change-password`, `POST /auth/2fa/setup`,
`POST /orgs/:org/tokens`, invitations, `POST /orgs` (creating a second org),
integrations, status pages, custom domains, org profile and org deletion
without listing any of them.

**Two paths the guard cannot see**, because they are unauthenticated:

- `POST /auth/reset-password`
  ([server.go:669](../../server/internal/app/server.go:669)) must refuse to
  complete a reset for a `users.demo` user, so nobody — including the mailbox
  owner by accident — rotates the shared password.
- `POST /auth/login` itself stays open; that is the feature. Login already
  sits under the global per-IP limiter
  ([config.go:1236](../../server/internal/config/config.go:1236)); no
  demo-specific limiter is needed unless abuse shows up.

### 3. What a demo session may create, and what it owns

Enforced in `checks.Service` for `claims.Demo` (create path at
[service.go:1257](../../server/internal/handlers/checks/service.go:1257),
mirrored on upsert, update, clone and delete):

- **New column `checks.created_by varchar(36) null`**, set from
  `claims.UserUID` on every create/clone for *all* users (it is useful audit
  data in its own right), exposed as `createdBy` on the check response.
  Seeded checks are created by the startup job with `created_by = NULL`.
- **Ownership**: `PATCH`, `DELETE` and `clone` from a demo session succeed
  only when `check.created_by == claims.UserUID`; otherwise `403
  DEMO_READ_ONLY`. Seeded checks are therefore untouchable without any
  "protected" flag, and cloning a seeded check produces an owned copy the
  visitor can then edit.
- **Types**: only side-effect-free, credential-free probes — `http`, `tcp`,
  `icmp`, `dns`, `ssl` (constants at
  [checkerdef/types.go:111-119](../../server/internal/checkers/checkerdef/types.go:111)).
  Explicitly excluded: `smtp` (send mode is a spam relay behind a public
  login), `email`, `browser` (cost), `ssh`, `kubernetes`, `docker`, every
  database type, and everything else. Reject with a `VALIDATION_ERROR` naming
  the field, so the dashboard's type picker can hide the rest.
- **Period ≥ 60 s.** The org-wide `maxChecksPerMinute` is the real ceiling;
  the floor just keeps one visitor from pinning a target every 10 s.
- **Regions**: public only. Structurally true already — private regions are
  `@`-prefixed and org-scoped
  ([regions/regions.go:42](../../server/internal/regions/regions.go:42)) and
  the demo org will never own a `custom_regions` parameter — but assert it.
- `internal` stays non-writable
  ([validate.go:126](../../server/internal/handlers/checks/validate.go:126)),
  so demo checks always count against `MaxChecks`.

### 4. Entitlements pin the org, sinks absorb the notifications

- The startup job writes an `org_entitlements` row for the demo org with
  `source` marked as system-managed so billing never reconciles it:
  `maxChecks = seeded count + 20`, `maxChecksPerMinute = 30`, `maxUsers = 1`,
  `maxSlos = seeded count`, `maxCustomDomains = 0`, `maxDeportedAgents = 0`,
  `maxSmsPerMonth = maxCallsPerMonth = maxWhatsappPerMonth = 0`,
  `displayName = "Live demo"`. Defaults live in
  [entitlements/defaults.go:145](../../server/internal/entitlements/defaults.go:145);
  the Usage page renders whatever is there.
- The seeded escalation policy targets one **sink** integration only: a
  `webhook` posting to our own `/api/v1/fake?supportedMethod=POST`
  ([fake-api spec](../done/2026/01/2026-01-02-fake-api.md)). Failing checks —
  seeded or visitor-created — then produce a visible incident, a visible
  escalation step and a visible notification log without a byte reaching a
  real person. Demo sessions cannot create integrations (§2), so this cannot
  be widened from the inside.

### 5. The living catalogue

Implement the dormant `checkerdef.Demo` sample type in `GetSampleConfigs`
([checkerdef/interface.go:88](../../server/internal/checkers/checkerdef/interface.go:88),
HTTP example at
[checkhttp/samples.go:28](../../server/internal/checkers/checkhttp/samples.go:28))
for `http`, `tcp`, `icmp`, `dns`, `ssl` and `heartbeat`, and have the startup
job load it into the demo org exactly as `loadSampleChecks` loads `Default`
into `default` ([job_startup.go:288](../../server/internal/jobs/jobtypes/job_startup.go:288)),
idempotent via an org parameter (`samples.loaded`). Rules for the catalogue:

- **Targets are SolidPing-owned only**: `solidping.io`, `/docs`, `/status0`,
  the API health endpoint, and `/api/v1/fake` with different `period` /
  `statusDown` / `delay` values so there is always something flapping, one
  thing slow, one thing hard-down. No third-party host is ever probed by the
  demo — it is the wrong place to spend other people's bandwidth.
- **Size against the `/fake` limiter**: 60 requests/minute *per client IP*
  ([server.go:546](../../server/internal/app/server.go:546)), and each worker
  region is its own client. Six fake-backed checks at 60 s is 6 rpm per
  region; keep well under the cap and say so in a comment next to the
  catalogue.
- Three or more public regions on every check, a check group, two SLOs (one
  healthy, one burning), a recurring maintenance window on one check, labels,
  one public status page at `/status0/demo` listing the lot, and the sink
  escalation policy from §4.
- **Backfill on first seed**: extract the synthetic-history generator behind
  `POST /test/generate-data`
  ([testapi/generate_data.go:127](../../server/internal/handlers/testapi/generate_data.go:127))
  into an internal package and run it once for 30 days at seed time, so the
  charts are not empty on launch day. Real results take over from there.
  Guard with an org parameter (`demo.backfilled`) so a restart never doubles
  history.

### 6. The cleanup and self-healing job

A new self-rescheduling job `demo_cleanup` on the `events_cleanup` pattern
([job_events_cleanup.go:14](../../server/internal/jobs/jobtypes/job_events_cleanup.go:14);
type in [jobdef/types.go:88](../../server/internal/jobs/jobdef/types.go:88),
factory in [registry.go:43](../../server/internal/jobs/jobtypes/registry.go:43)),
interval 30 min, TTL 1 h (both configurable). Each run:

1. Deletes every check in the demo org with `created_by = demo user` and
   `created_at < now - TTL`, **through the checks service delete path**
   ([service.go:2297](../../server/internal/handlers/checks/service.go:2297)),
   not a raw soft-delete, so check jobs, open incidents and realtime
   subscribers are handled the way a user deletion handles them.
2. Reconciles the demo identity: password hash back to the configured
   password, `must_change_password = false`, `super_admin = false`, 2FA
   disabled, memberships exactly `{demo org: user}`, entitlements row as in
   §4, `auth.session_max_duration` as in §1. Anything that slipped past the
   guard is undone within half an hour, and an operator who fat-fingers the
   demo user in the superadmin UI is corrected automatically.
3. Skips entirely when `demo.enabled` is off.

### 7. Configuration

Off by default; production SaaS turns it on.

| Key (koanf) | Env | Default |
|---|---|---|
| `demo.enabled` | `SP_DEMO_ENABLED` | `false` |
| `demo.org_slug` | `SP_DEMO_ORG_SLUG` | `demo` |
| `demo.email` | `SP_DEMO_EMAIL` | `demo@solidping.io` |
| `demo.password` | `SP_DEMO_PASSWORD` | `demo` |
| `demo.check_ttl` | `SP_DEMO_CHECK_TTL` | `1h` |
| `demo.cleanup_interval` | `SP_DEMO_CLEANUP_INTERVAL` | `30m` |

Multi-word keys need the manual env reader the rest of `config.go` uses for
underscored names (see the `SP_RUN_MODE` reader at
[config.go:1717](../../server/internal/config/config.go:1717)). When enabled,
`GET /api/v1/public-config`
([publicconfig/handler.go:104](../../server/internal/handlers/publicconfig/handler.go:104))
gains `demo: {enabled, orgSlug, email, password}` — the password *is* public
by design, and serving it explicitly beats hardcoding it in the bundle.
`GET /auth/me` gains `demo: true` for demo sessions.

Test run mode (`SP_RUN_MODE=test`) enables the demo unconditionally so the
E2E suite can exercise it.

### 8. Dashboard

- **One-click entry.** On the login page
  ([login.tsx:792-846](../../web/dash0/src/routes/orgs/$org/login.tsx:792)),
  when public config reports `demo.enabled`, a secondary button under the
  submit button — *"Try the live demo"*, `data-testid="login-demo"` — calls
  the existing `login(org, email, password)`
  ([login.tsx:389](../../web/dash0/src/routes/orgs/$org/login.tsx:389)) with
  the demo triple and goes through `routeResult` like any other login. It
  shows on every org's login page, since `/login` lands on `default` or the
  last-used org. `?demo=1` on the login URL triggers it on load, so the
  marketing site can deep-link straight in. Coordinate with
  [2026-09-06-01](2026-09-06-01-login-footer-brand-changelog-links.md), which
  touches the same page's footer.
- **Persistent banner.** In the org layout
  ([$org.tsx:1090](../../web/dash0/src/routes/orgs/$org.tsx:1090)), when
  `me.demo` is true, an `Alert` above the content on every page, built like
  [check-rate-limit-banner.tsx](../../web/dash0/src/components/shared/check-rate-limit-banner.tsx):
  *"You're in the shared live demo. Everything here is visible to other
  visitors, and anything you create is deleted after an hour. Sign up free to
  keep it."* with the sign-up CTA. Not dismissable.
- **The deletion is the conversion hook.** After a demo session creates a
  check, the success state says it is live and will disappear in an hour, and
  offers sign-up. The check detail page shows the same for owned checks.
- **`DEMO_READ_ONLY` is not a Permission Denied.** Handle it in the API client
  the way `PASSWORD_CHANGE_REQUIRED` is special-cased
  ([client.ts:363](../../web/dash0/src/api/client.ts:363)): a toast, not the
  403 page, and background writes (UI state, read receipts, last-seen markers)
  degrade silently. Audit dash0's fire-and-forget PUT/POSTs for any that would
  surface an error toast in a loop.
- **Hide what cannot be done.** Using `createdBy` and `me.demo`: edit/delete
  buttons hidden on seeded checks, the type picker limited to the allowed
  types, and the New buttons on integrations, status pages, members and
  settings replaced by a read-only note. The guard is the safety; this is
  only politeness.
- All strings in the four locales (`web/dash0/src/locales/{de,en,es,fr}`);
  `bun run test:unit` catches a missing key.

### 9. Tests

Backend:

- **Route-table proof**, modelled on `TestEveryOrgScopedAPIRouteRedirectsOnAPreviousSlug`
  (referenced at [server.go:760](../../server/internal/app/server.go:760)):
  walk every registered non-GET route with demo claims and assert `403
  DEMO_READ_ONLY` unless the route is in the allowlist. This is what makes the
  allowlist structural rather than a promise.
- Guard unit tests: non-demo claims untouched; GET passes; each allowlisted
  route passes; PAT-derived demo claims are guarded too.
- Checks service: ownership on patch/delete/clone; type, period and region
  restrictions; `created_by` set for every creator; seeded checks have
  `created_by = NULL`.
- Reset-password refuses demo users.
- Startup seed idempotent across restarts; catalogue loads once; backfill
  runs once; entitlements row present and pinned.
- Cleanup job: deletes only expired demo-owned checks, leaves seeded and
  fresh ones, reconciles a tampered password hash / superadmin flag /
  must_change_password, is a no-op when disabled.
- Public config and `/auth/me` shapes.

Dash0:

- `e2e/demo-account.spec.ts` next to
  [login.spec.ts](../../web/dash0/e2e/login.spec.ts): button visible in test
  mode, one click lands in the demo org with the banner, creating an `http`
  check succeeds and shows the expiry note, deleting a seeded check yields the
  read-only toast, the settings page shows the read-only note, `?demo=1`
  auto-logs-in.
- Unit tests for the banner and the client's `DEMO_READ_ONLY` handling.

## Decisions

- **Shared production server, not a separate instance.** The multi-region
  fleet and real history are the demo; isolation only matters if visitors can
  mutate, and the allowlist removes that.
- **Standard login with a public password, not a session-minting endpoint.**
  The only thing a dedicated endpoint bought was not publishing a password
  worth nothing; standard auth gives PATs, MCP and OAuth flows for free.
- **Writable within an allowlist, not read-only.** Creating a check is the
  aha moment. Load is bounded by entitlements that already exist; the
  allowlist is four routes; the abuse classes that remain (spam relays,
  credential-bearing types) are excluded by type.
- **Allowlist, never denylist.** A forgotten endpoint fails closed.
- **Role `user`, guard as its own middleware.** The role is not a safety
  mechanism today and the guard must not depend on it becoming one.
- **Ownership via `created_by`, no "protected" flag.** Seeded rows are
  `NULL`-owned and therefore immutable to demo sessions by construction.
- **Sinks only.** Demo notifications go to our own fake endpoint.
- **Own targets only.** The catalogue never probes a third party.
- **Shared sandbox, stated openly.** Visitors see each other's checks for up
  to an hour. The banner says everything is public; per-session isolation is
  machinery we do not need yet.
- **Not `viewer`-based, and not fixing `viewer` here.** The viewer
  enforcement gap is real and gets its own spec.

## Out of scope

- Per-visitor isolation or per-session ownership.
- Letting demo sessions create integrations, status pages, members, SLOs,
  maintenance windows or anything beyond checks.
- A separate `demo.solidping.io` instance or a "demo mode" server flag.
- Marketing-site changes (`solidping-website` repo) beyond the deep link.
- Publishing a demo PAT for the CLI / MCP walkthrough — the design allows it
  (a demo PAT inherits the guard) but the docs work is a follow-up.
- Fixing `viewer` so it is actually read-only.
- Magic-link login ([backlog](../backlog/2026-08-30-08-magic-link-login.md)).

## Open questions

- **Sign-up destination** for the banner CTA: the existing registration route
  on the same host, or the marketing site's pricing page? Default to the
  in-app registration route; the marketing link is a one-line change later.
- **Should the `Default` sample catalogue in `default` be retired** once the
  `Demo` catalogue exists, or do self-hosted installs still want their own
  first-run samples? Leave `Default` alone in this spec.
- **Billing interaction**: confirm `solidping-billing` never writes an
  `org_entitlements` row for an org it has no customer for, so the pinned demo
  row is never overwritten. If it can, add an explicit skip on the billing
  side keyed on the org slug.

## Resolved open questions

> **Sign-up destination** for the banner CTA: the existing registration route
> on the same host, or the marketing site's pricing page?

**Resolved: the in-app registration route on the same host**, as the question's
own default states. Do not link the marketing site from the banner in this
spec; swapping it later is a one-line change.

> **Should the `Default` sample catalogue in `default` be retired** once the
> `Demo` catalogue exists, or do self-hosted installs still want their own
> first-run samples?

**Resolved: leave `Default` completely alone.** Add the `Demo` catalogue
alongside it. Do not touch `loadSampleChecks`' existing `Default` behaviour,
and do not change what a self-hosted first run seeds.

> **Billing interaction**: confirm `solidping-billing` never writes an
> `org_entitlements` row for an org it has no customer for, so the pinned demo
> row is never overwritten. If it can, add an explicit skip on the billing
> side keyed on the org slug.

**Resolved: confirmed safe — the conditional does not fire, so there is NO
billing-side change to make, and this spec touches only this repository.**
Verified in `../solidping-billing` at `f583fe7`: there are exactly three
`Push` call sites — `reconciler/reconciler.go:175`, `:268` and
`handlers/webhook/service.go:75` — and every one is reachable only from a
Polar subscription resolved to a customer carrying an `org_slug`.
`Reconciler.RunOnce` iterates `polar.ListSubscriptions`, never the org table,
and `reconcileSub` logs *"customer has no org_slug in metadata, skipping"* and
returns nil when no slug resolves (`reconciler.go:133-138`). The webhook path
is likewise driven by a customer subscription. An org with no Polar customer —
which the `demo` org will never have — is therefore never pushed to.

Directive for the implementer: **do not modify `../solidping-billing`**, and do
not add a demo-slug skip there. Pin the finding instead — a comment next to the
demo `org_entitlements` seeding in the startup job recording that billing only
pushes for orgs with a Polar customer, so the pinned row is safe, and that this
is the assumption to re-check if billing ever grows an all-orgs sweep.

## Implementation Plan

Ordered so every step compiles on top of the previous one. One commit per step.

### P1 — Schema (§1, §3)

`019_v0_25_0.{up,down}.sql` for **both** dialects (018 shipped with v0.24.0;
019 is the next free number):

- `users.demo boolean not null default false`
- `checks.created_by varchar(36) null`

Models: `models.User.Demo` (+ `UserUpdate.Demo`), `models.Check.CreatedBy`.

### P2 — Claims carry `demo` (§1)

- `auth.Claims` gains `Demo bool \`json:"demo,omitempty"\``.
- `generateAccessToken` gains a `demo bool` parameter; every call site passes
  `user.Demo` (`mintOrgSession` threads it from its two callers).
- `ValidatePATToken` sets it from the owning user row.
- Belt and braces: `RequireAuth` / `RequireMCPAuth` already load the user row,
  so they OR `user.Demo` into the claims after parsing. A mint site that is
  ever added and forgotten therefore still fails closed.

### P3 — The guard (§2)

New `internal/handlers/auth/demo_guard.go`, modelled 1:1 on
`password_rotation.go`:

- `DemoWriteMessage`, `ErrorCodeDemoReadOnly = "DEMO_READ_ONLY"` in
  `handlers/base`.
- `IsDemoWriteAllowed(method, routePattern string) bool` — GET/HEAD/OPTIONS
  pass; otherwise the pattern must be in the written-out allowlist:
  `POST /api/v1/auth/logout`, `POST /api/v1/orgs/{org}/checks`,
  `POST /api/v1/orgs/{org}/checks/validate`,
  `PATCH|DELETE /api/v1/orgs/{org}/checks/{checkUid}`,
  `POST /api/v1/orgs/{org}/checks/{checkUid}/clone`, plus the non-GET
  check-diagnostics probes.
- Enforced inside `RequireAuth` and `RequireMCPAuth` right after the claims are
  parsed, matching on `httpx.RoutePattern(req)` (chi's resolved pattern, never
  the raw path).
- `POST /auth/reset-password` (unauthenticated, so invisible to the guard)
  refuses to complete for a `users.demo` user.

### P4 — Checks service rules (§3)

In `checks.Service`, a shared `demoClaims(ctx)` helper (claims come off the
request context, the package already imports `internal/middleware`):

- `created_by` set from `claims.UserUID` on **every** create/clone for **all**
  users; exposed as `createdBy` on `CheckResponse`. Startup-seeded checks keep
  `NULL`.
- Demo-session rules on create/upsert/clone: type in
  {http,tcp,icmp,dns,ssl}, period >= 60s, public regions only — each rejected
  with a `VALIDATION_ERROR` naming the field.
- Ownership on patch/delete/clone: `created_by == claims.UserUID` or
  `403 DEMO_READ_ONLY`.

### P5 — Config (§7)

`config.DemoConfig` (`demo.enabled|org_slug|email|password|check_ttl|cleanup_interval`)
plus `applyDemoEnv` in the manual-env-reader block (underscored keys never
survive koanf's `SP_*` transform). `SP_RUN_MODE=test` forces `Enabled` on.

### P6 — Seeding + entitlements (§1, §4)

`jobtypes/job_startup.go` gains `ensureDemoOrganization`, running unconditionally
after the default org (it is idempotent by slug/email, and it must survive a
database that already has orgs — the `count > 0` early return in
`ensureDefaultOrganization` is exactly the case a production demo lands in):

org by slug, `demo.enabled=true` org param, `auth.session_max_duration=3600`,
user by email (role `user`, `demo=true`, never superadmin, never
must-change-password), pinned `org_entitlements` row (with the comment recording
the billing finding from the resolved open question), a sink `webhook`
integration + escalation policy pointing at our own `/api/v1/fake`, the `Demo`
catalogue, and the 30-day backfill.

### P7 — The catalogue (§5)

`checkerdef.Demo` implemented in `GetSampleConfigs` for `http`, `tcp`, `icmp`,
`dns`, `ssl` and `heartbeat`. SolidPing-owned targets only; the fake-backed
checks sized well under the 60 rpm-per-IP `/fake` limiter, with the arithmetic
in a comment. Seeded with a check group, labels, two SLOs, a maintenance
window and one public status page.

### P8 — Backfill (§5)

Extract the `POST /test/generate-data` generator into `internal/synthdata`
(pure, no HTTP, no testapi dependency); `testapi` becomes a thin caller. The
demo seed runs it once for 30 days behind the `demo.backfilled` org parameter.

### P9 — Cleanup job (§6)

`jobdef.JobTypeDemoCleanup` + `jobtypes/job_demo_cleanup.go` on the
`events_cleanup` self-rescheduling shape, seeded from the startup job:
expired demo-owned checks deleted **through `checks.Service.DeleteCheck`**,
then the identity/entitlements/session-cap reconciliation, no-op when
`demo.enabled` is false.

### P10 — API shapes (§7)

`GET /api/v1/config` gains `demo: {enabled, orgSlug, email, password}`;
`GET /auth/me` (and every login-shaped response, via `newUserInfo`) gains
`demo: true`.

### P11 — Dashboard (§8)

`login.tsx` demo button + `?demo=1` autologin; a non-dismissable banner in the
org layout; `DEMO_READ_ONLY` special-cased in `api/client.ts` as a toast rather
than the 403 page; `createdBy`/`me.demo` used to hide what cannot be done;
strings in `de`/`en`/`es`/`fr`.

### P12 — Tests (§9)

Route-table proof (walk every non-GET route with demo claims), guard unit
tests, checks-service ownership/type/period/region tests, reset-password
refusal, seeding idempotence, cleanup-job behaviour, public-config and
`/auth/me` shapes; dash0 `e2e/demo-account.spec.ts` and unit tests for the
banner and the client.
