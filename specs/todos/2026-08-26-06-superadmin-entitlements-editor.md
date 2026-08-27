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
