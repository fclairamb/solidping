---
model: opus
effort: high
---

# Superadmin entitlements editor — raise an org's limits without a billing deploy

## Problem

The only writers of `org_entitlements` today are the billing service (signed
`PUT /api/v1/orgs/:org/entitlements`, verified by `ServiceSignature` —
group registered around `server/internal/app/server.go:1508–1509`) and raw
SQL. When we decide an org deserves a bump — like the `public` demo org
during the 2026-08-26 rate-limit incident — there is no human-usable path:
no UI, no API an operator can call with their own credentials.

The building blocks already exist:

- Superadmin authz: `users.super_admin`
  (`internal/db/models/auth.go:63`), `RequireSuperAdmin` middleware
  (`internal/middleware/auth.go:577–590`), several existing superadmin
  route groups (`server.go:960, 1457, 1473, 2005`), and a dash0 superadmin
  surface (`web/dash0/src/routes/orgs/$org/server.tsx`, gated on
  `user?.isSuperAdmin`).
- Provenance: `org_entitlements` rows carry a source
  (default / billing-service / admin — see `PlanWeight`,
  `internal/entitlements/service.go:355–372`, which already treats
  admin-sourced rows as paid-tier for scheduling).
- Audit: every entitlement write already lands in
  `org_entitlement_audits`.

The real design risk is not code volume — it is **precedence versus
billing**: an admin edit that billing's next reconcile silently overwrites
is worse than no editor, because it lies to the operator.

## Proposal

### Precedence rule (the heart of the spec)

An **admin-sourced row wins until explicitly released.** While an org's
entitlements row has source=admin:

- Billing pushes are **accepted with 200** (billing must not error-loop)
  but do **not** overwrite the stored limits; the push is recorded in
  `org_entitlement_audits` as suppressed-by-admin-override, and the
  response body says so.
- "Release to billing" is an explicit admin action: it removes the admin
  override, after which the next billing push (or the deployment-mode
  defaults) applies again.

### Backend

1. Superadmin endpoints (inside an existing `RequireSuperAdmin` group):
   - `GET` a paged, searchable list of orgs with their *resolved*
     entitlements, source, and last-synced/audit timestamps.
   - `GET` one org's entitlements detail plus its audit trail
     (`org_entitlement_audits`, newest first, `limit` param).
   - `PUT` one org's entitlements with source=admin. Same payload shape as
     the billing path; `null` keeps its "unlimited" meaning. Reuse the
     existing entitlements service write path — one write path, two authz
     front doors (service signature vs superadmin session).
   - `DELETE` (release): drops the admin override as described above.
2. Every write and every suppressed billing push audited.

### Frontend (dash0)

3. A superadmin-only page colocated with the existing superadmin surface
   (the `server.tsx` area — implementer picks tab vs sibling route):
   org search → org detail with current resolved limits, their provenance
   ("Free defaults" / "Billing: Pro" / "Admin override since …"), editable
   numeric fields with explicit unlimited toggles (nil semantics must be
   visible, not an empty string), display name/emoji, and the audit trail.
4. Saving requires a confirmation step that restates the org slug and the
   diff — this is revenue policy, not a settings toggle. Releasing to
   billing likewise confirms, stating what will happen on the next sync.
5. Non-superadmins must never see the page (follow the existing
   `server.tsx` gating; 403 renders Permission Denied, no redirect).

### Tests

- Authz: non-superadmin gets 403 on every endpoint; superadmin from any
  org can target any org.
- Precedence: billing push onto an admin row → 200, limits unchanged,
  audit row written; release → next billing push applies; defaults apply
  when no billing row exists after release.
- Resolution: admin-sourced rows still count as `PlanWeightPaid`.
- Frontend unit tests for the unlimited/nil rendering, plus locales.

## Resolved open questions

Approved by the maintainer on 2026-08-26 with the spec itself.

**Q: Should the list view flag orgs currently over their
`MaxChecksPerMinute`?**

**Decision: yes.** Spec 2026-08-26-03 (implemented earlier in the same
batch) exposes demand vs cap; the superadmin org list shows an amber
over-limit indicator on orgs where demand exceeds the resolved cap, so
support can find struggling orgs proactively.

**Q: Field-level vs whole-row override?**

**Decision: whole-row only.** The editor pre-fills from the resolved
values and saves a complete admin-sourced row. Partial per-field
overrides are explicitly out of scope — a follow-up spec if they ever
matter.

## Implementation Plan

### 1. Precedence rule in the entitlements service (the heart)

- `server/internal/entitlements/precedence.go` (new):
  - `WriteOutcome{Applied bool, SuppressedBy models.EntitlementSource}`.
  - `Service.Apply(ctx, orgUID, input, actor, reason) (WriteOutcome, error)` —
    the single write path both front doors call. When the incoming source is
    `billing-service` and the **stored** row's source is `admin`, the push is
    **not** applied: an audit row with source
    `billing-service:suppressed` (`AuditSourceBillingSuppressed`) is written and
    the outcome reports `Applied=false, SuppressedBy=admin`. Everything else
    delegates to the existing `Set`.
  - `Service.Release(ctx, orgUID, actor, reason) (bool, error)` — drops the
    admin override by **deleting** the `org_entitlements` row (audited), so
    resolution falls back to deployment-mode defaults until the next billing
    push writes a fresh row.
- `db.Service` gains `CreateOrgEntitlementAudit` (audit-only insert, used by
  the suppression path) and `DeleteOrgEntitlements` (delete + audit in one tx),
  implemented for both sqlite and postgres.
- The existing `PUT/PATCH /api/v1/orgs/:org/entitlements` handler switches from
  `Set` to `Apply` and reports `applied` / `suppressedBy` / `message` in its
  200 body. `Set` stays as the raw primitive (used by tests and by `Apply`).

### 2. Superadmin endpoints

New `server/internal/handlers/entitlements/admin.go`, mounted in a
`RequireAuth + RequireSuperAdmin` group in `server/internal/app/server.go`
(deliberately under `/api/v1/system/...`, NOT under `/orgs/:org/...`, so no
`RequireOrgAccess` narrows a superadmin to their own org):

- `GET /api/v1/system/entitlements?q=&limit=&offset=` — one row per org with
  resolved limits, source, display identity, last-synced, and the
  checks-per-minute demand/cap pair from spec 2026-08-26-03
  (`Service.ChecksPerMinuteStatus`) so the list can flag over-limit orgs.
  Wrapped as `{ "data": [...], "total": n }`.
- `GET /api/v1/system/entitlements/:org?limit=` — resolved entitlements, the
  raw stored row (so the UI can tell "stored" from "default-filled"), and the
  audit trail newest-first.
- `PUT /api/v1/system/entitlements/:org` — whole-row write forced to
  `source=admin` (per the resolved question: no per-field overrides), through
  `Apply`.
- `DELETE /api/v1/system/entitlements/:org` — release to billing.

### 3. OpenAPI

Document the four endpoints and the new response fields in
`server/internal/app/openapi/openapi.yaml`.

### 4. Frontend (dash0)

- `web/dash0/src/lib/entitlements-admin.ts` (+ vitest): pure helpers for the
  nil/unlimited rendering (`formatLimit`, `parseLimitInput`,
  `limitsDiff`, `provenanceKey`) — this is what the unit tests pin.
- `web/dash0/src/api/hooks.ts`: `useAdminEntitlementsList`,
  `useAdminEntitlementsDetail`, `useSetAdminEntitlements`,
  `useReleaseAdminEntitlements`.
- Routes, colocated with the existing superadmin surface:
  - `server.entitlements.index.tsx` — search + table, amber over-limit
    indicator reusing `isOverCheckRateLimit` / `formatCheckRateDemand`.
  - `server.entitlements.$targetOrg.tsx` — provenance, editable numeric fields
    each with an explicit **Unlimited** switch, display name/emoji, audit
    trail, plus two `AlertDialog` confirmations (save restates the slug and the
    diff; release restates what the next billing sync will do).
  - New `Entitlements` tab in `server.tsx` (whose existing gate already keeps
    non-superadmins out).
- Locale keys in all four of `en/fr/de/es` `server.json`.

### 5. Tests

- Backend: authz (non-superadmin 403 on all four endpoints; superadmin from
  org A edits org B), precedence (billing push onto an admin row → 200,
  limits unchanged, suppression audit written; release → next billing push
  applies; defaults after release), resolution (`PlanWeight` still paid for an
  admin row).
- Frontend: vitest over the pure helpers (unlimited/nil rendering, diffing)
  plus locale parity across the four locales.
- E2E: a Playwright spec for the superadmin page.

## Decisions taken during implementation

Both were raised by the completeness audit on 2026-08-27 and fixed before the
spec closed.

### 1. An `admin` row resolves whole-row: nil means unlimited

`Service.Resolve` starts from `DefaultsFor(mode)` and only overwrites a field
when the stored pointer is non-nil. On SaaS **every default is non-nil**, so
the editor's "Unlimited" switch was a no-op: the stored `null` was refilled
with the SaaS default on the way out, the form re-seeded from the resolved
values, and the toggle flipped itself off with the org still capped.

The resolved open question already made admin rows **whole-row only** (the
editor pre-fills from resolved values and saves a complete row), so nil in such
a row is a deliberate statement, not an omission. `merge` now branches on the
row's source: `admin` takes the payload wholesale, everything else keeps the
existing null-fill.

`whiteLabel` is exempt in both modes. It is a boolean, so its nil cannot mean
"unbounded"; per `EntitlementLimits`' own contract it means "use the deployment
default", and reading it as `false` would make every admin override silently
revoke white-label on a deployment that grants it.

Billing and org-admin resolution is deliberately untouched — a billing push
that omits a field must still inherit the default, or a partial SKU would
uncap an org.

### 2. The org-scoped door mints `org-admin`, not `admin`

`PUT /api/v1/orgs/:org/entitlements` accepts any org admin whenever
`entitlements.admin_writes_enabled` is unset, because it **defaults to true**,
and on SaaS that parameter is only written when `SP_ENTITLEMENTS_ADMIN_WRITES`
is set explicitly. Before this spec such a row was harmless: billing's next
reconcile corrected it. With the precedence rule it would have become a
permanent lockout — any org admin could grant themselves limits *and* stop
billing from ever correcting them.

So `models.EntitlementSourceOrgAdmin` now exists, suppression does not trigger
on it, and it carries the same paid plan weight as before so self-hosted
scheduling does not regress. The source is chosen by `Handler.sourceFor` and
never by the request body: it is an authorization outcome, so a body asking to
be recorded as `admin` (or as the billing service) is ignored. A superadmin
calling the org-scoped route still mints `admin`.

**Migration:** see decision 4 — leaving those rows alone turned out to be
unsafe, and they are relabelled instead.

### 3. Not changed: the `server.tsx` layout gate still redirects

The audit flagged the shared Server-area layout redirecting non-superadmins as
a possible violation of "403 → Permission Denied, never redirect". Adjudicated
as out of scope: the repo convention is about HTTP 403 *responses*, this spec's
own clause says to follow the existing `server.tsx` gating, and changing the
shared layout would silently alter all fourteen `/server` tabs. Both new pages
do render `PermissionDenied` in place on an API 403, with a comment recording
the split.

### 4. Legacy `admin` rows are relabelled by a migration, not left alone

Caught by the second audit round: my own fix for decision 1 created a
regression on existing databases.

Decision 2 noted that pre-existing `admin` rows would start suppressing billing
and judged that acceptable. That judgement was made against an incomplete
description of the cost, because decision 1 gave `admin` a *second* new power:
whole-row resolution. Those rows are routinely **partial** — the API has never
required a complete payload on `PUT`, `mergePartial` only runs on `PATCH`, and
`toModel` copies `input.Limits` verbatim, nils included. `TestCLICoverage_Entitlements`
is itself an instance of the shape, writing three caps and leaving six nil.

So on the first resolve after deploy — long before anyone opens the editor —
every omitted cap on every legacy row would flip from the deployment default to
**unlimited**: on SaaS `maxChecksPerMinute` 10 → ∞, `maxUsers` 5 → ∞,
`maxSlos` 2 → ∞, and the three messaging caps 0 → ∞ (unbounded spend on the
instance's own Twilio/Meta credentials); on self-hosted, where that door is the
normal one, the 30-seat guard disappears. A release note is not a control for
something that lands automatically at startup.

Migration `016_v0_19_0` (both dialects; v0.18.3 is released so this cycle opens
a new consolidated file) relabels `payload.source` from `admin` to `org-admin`.
Lossless by construction — those rows *were* org-scoped writes — and it
restores both the old null-fill resolution and the old non-suppressing
precedence in one statement. Each relabelled org gets one audit row explaining
it; a superadmin who wants a genuine override re-saves it in the editor.

Two details worth recording:

- **The live source lives in exactly one place**, `org_entitlements.payload`'s
  `source` key. (`org_entitlement_audits.source` is a different thing: the
  historical log of what each past write claimed. Nothing reads it for
  behaviour — it renders as a badge — and it is deliberately *not* rewritten,
  because falsifying an audit trail to match today's vocabulary is worse than
  the vocabulary drifting. The audit row the migration inserts bridges the two.)
- **The `down` maps `org-admin` back to `admin`**, which is the correct inverse
  rather than a lossy approximation: a downgraded binary has never heard of
  `org-admin` and would resolve such a row through the default branch, demoting
  the org to the free tier.

### 5. Not changed: a trusted service may still name its own source

`sourceFor` lets a service principal write `source: "admin"`, so the billing
service — or anything holding the legacy static bearer — can suppress its own
future pushes. Pre-existing, matches this spec's "a trusted service keeps the
old behavior", and no caller does it. Left alone, with a marker comment on
`sourceFor` and a note in `wiki/features/entitlements.md` naming the one-line
fix if it ever needs closing.
