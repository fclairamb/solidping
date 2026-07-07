# SaaS "Free" plan shows self-hosted limits — align deployment mode, defaults, and plan labeling

## Problem

On `https://solidping.k8xp.com/dash0/orgs/$org/organization/usage` the
org shows **Current plan: Free** with:

- Checks: 52 / **Unlimited**
- Checks per minute: 41.1 / **Unlimited**
- SSO users: 2 / **30**

That is not the SaaS Free plan — it is the **self-hosted default profile**
(`maxSsoUsers=30`, everything else unlimited,
`server/internal/entitlements/defaults.go`) being rendered with a SaaS-style
"Free" label. The intended SaaS Free plan (authoritative definition in
`solidping-billing/server/internal/plans/plans.go`) is `maxChecks=10` +
`maxChecksPerMinute=6`.

Three causes stack up:

1. **Deployment config (primary).** Neither the k8xp dev overlay
   (`k8xp/k8s/solidping/overlays/dev/environment-patch.yaml`,
   solidping.k8xp.com) nor the prod overlay (`overlays/prod/…`, solidping.io)
   sets `SP_DEPLOYMENT_MODE`. The server falls back to `self-hosted`
   (`server/internal/config/config.go` — `c.Deployment.Mode =
   DeploymentModeSelfHosted`), so `DefaultsFor` hands out self-hosted limits.
   **Prod has the same problem, not just dev.** No billing service is
   deployed on k8xp either, so nothing ever writes real Free-plan
   entitlements into `org_entitlements`.

2. **Frontend fallback label.** The usage page renders
   `data.displayName ?? t("usage.freePlan")`
   (`web/dash0/src/routes/orgs/$org/organization.usage.tsx`), so *any* org on
   in-code defaults — including on a genuinely self-hosted server — claims to
   be on a "Free" plan. On self-hosted that label is misleading in itself.

3. **In-code SaaS defaults are looser than the billing Free plan.**
   `DefaultsFor(config.DeploymentModeSaaS)` only caps
   `maxChecksPerMinute=6` and leaves `maxChecks` unlimited. A fresh SaaS org
   that the billing service hasn't reconciled yet still shows
   "Checks: Unlimited" and can create unbounded checks.

### Caution — flipping the switch throttles $org

$org currently runs 52 checks at ~41 checks/minute. Setting
`SP_DEPLOYMENT_MODE=saas` makes `ReserveCheckExecution`
(`server/internal/entitlements/service.go`) enforce the 6/min token bucket
immediately, and the Free-plan `maxChecks=10` puts the org 5× over its check
quota. The mode flip must ship together with an admin/billing entitlement
override for $org.

## Proposal

### A. Align in-code SaaS defaults with the billing Free plan

In `server/internal/entitlements/defaults.go`, make
`DefaultsFor(DeploymentModeSaaS)` return the same limits as the billing
service's Free plan:

- `maxChecks = 10`
- `maxChecksPerMinute = 6`
- `maxSsoUsers = nil` (unlimited, matching billing's Free plan)

Keep the two definitions consistent going forward — add a comment in both
repos pointing at each other (the OSS default is the "billing never
reconciled us" fallback and must match billing's `free` SKU).

### B. Backend-driven plan label instead of a frontend fallback

Give the defaults a display identity so the frontend stops guessing:

- SaaS defaults: `displayName = "Free"`, `displayEmoji = "🆓"` (matching
  billing's Free plan identity).
- Self-hosted defaults: `displayName = "Self-hosted"` (recommendation; emoji
  optional, e.g. "🏠").

`Service.merge` currently never falls back to `defaults.DisplayName` /
`DisplayEmoji` — extend it so a row without a display identity (or no row at
all) inherits the default one, same null-fill semantics as the limits.

Frontend: remove the hardcoded `?? t("usage.freePlan")` fallback in
`organization.usage.tsx` (render the backend-provided name; if absent, show
nothing or an em dash). Remove the now-dead `usage.freePlan` locale key from
all locale files.

### C. Ops rollout (k8xp repo — separate PR, sequenced)

1. Seed an **admin entitlement override for $org** first
   (`PUT /api/v1/orgs/$org/entitlements` with `source: "admin"` and
   limits fitting actual usage, e.g. Team-tier numbers or better), so the
   mode flip doesn't throttle it.
2. Add `SP_DEPLOYMENT_MODE=saas` to **both** the dev and prod overlays'
   `environment-patch.yaml`. Defaults are captured at boot
   (`NewService(defaults, …)`), so the required pod restart comes with the
   apply anyway.
3. Verify on the live usage page: $org shows its override; a scratch
   org shows Free-plan limits (10 checks, 6/min) with the 🆓 Free label.

## Out of scope

- Deploying the billing service on k8xp / wiring the full upgrade loop
  (`SP_ENTITLEMENTS_SERVICE_TOKEN`, upgrade URL template) — separate effort.
- Changing enforcement semantics (token bucket, `maxChecks` at creation) —
  already implemented, only the defaults change.
- Migrating existing self-hosted installs' entitlements — defaults are
  resolved live, no data migration needed.

## Acceptance criteria

- In SaaS mode, a fresh org (no `org_entitlements` row) resolves
  `maxChecks=10`, `maxChecksPerMinute=6`, `maxSsoUsers=nil`, displayName
  "Free" 🆓 — covered by unit tests on `DefaultsFor` and `Service.merge`.
- In self-hosted mode, the usage page no longer claims "Free"; it shows the
  self-hosted label with `maxSsoUsers=30` and everything else unlimited.
- A stored row with limits but no display identity inherits the default
  displayName/emoji; a billing-written row keeps its own.
- `usage.freePlan` locale key is gone from code and locales.
- `make test`, `make lint`, `make test-dash` green; existing entitlements
  handler/quota tests updated for the new SaaS defaults.
- k8xp (separate PR): both overlays set `SP_DEPLOYMENT_MODE=saas`;
  $org has an admin override **before** the flip; live verification of
  $org + a scratch org per C.3.

## Implementation plan

- [x] A: update `DefaultsFor` SaaS limits (+ cross-repo comment in
      solidping-billing `plans.go`); adjust unit tests.
- [x] B: add default display identity per mode; extend `Service.merge`
      null-fill to displayName/emoji; update entitlements handler tests.
- [x] B: dash0 — drop the `freePlan` fallback in `organization.usage.tsx`,
      remove the locale key, update any e2e/snapshot referencing it.
- [ ] C: k8xp PR — `SP_DEPLOYMENT_MODE=saas` in dev + prod overlays; seed
      $org admin override first; live verification.

## Status (2026-07-07)

Parts A and B are implemented, merged, and independently audited clean on
`batch/2026-07-06-2` (branch `fix/saas-free-plan-entitlements-defaults-mixup`,
merge commit `3dba6151`). The in-repo code fix is complete: SaaS defaults now
match the billing Free plan (`maxChecks=10`, `maxChecksPerMinute=6`,
`maxSsoUsers=nil`), both deployment modes carry a backend-driven display
identity ("Free" 🆓 / "Self-hosted" 🏠) with proper null-fill in
`Service.merge`, and the dash0 usage page and locale files no longer have the
hardcoded "Free" fallback.

**Part C remains outstanding and requires a human** — it edits the separate
`k8xp` repo and makes a live `PUT /api/v1/orgs/$org/entitlements` call against
a real running organization, plus flips `SP_DEPLOYMENT_MODE=saas` on real
dev/prod deployments. This was deliberately left undone by the automated
implementation process (out of scope for an autonomous subagent to touch
production infrastructure). This spec stays in `specs/todos/` — not archived
to `specs/done/` — until Part C ships, since the original live-production
symptom (org showing "Free" with self-hosted limits) isn't actually fixed
until then.
