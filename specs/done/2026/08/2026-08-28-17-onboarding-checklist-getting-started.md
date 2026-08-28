---
model: opus
effort: high
---

# After the first check, nothing guides a new user to alerting, weekly reports, or a status page — the one nudge is a localStorage banner

## Problem

The empty-org dashboard already sells the first check well
(`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`, rendered
when `stats.total === 0` in
`web/dash0/src/components/dashboard/dashboard-page.tsx:342,431`). But the
moment the first check exists, guidance collapses to a single one-shot
`FirstResultCelebration` banner (`dashboard-page.tsx:612-670`) whose dismissal
lives in localStorage (`solidping_celebrated_first_result_${org}`) — gone on
another device, and pointing only at the check page. Report schedules, email
integrations and status pages are all discovered (or not) by wandering the
sidebar.

The server even records the funnel already
(`server/internal/activation/` — signup completed at
`auth/service.go:3038-3040`, first check created at
`checks/service.go:1460`) but shows it only to the super-admin. The user, who
is the one who should act on it, never sees it.

## Proposal

A dismissible **getting-started checklist** card on the org dashboard, with
completion **derived from real resources** (never stored per-step flags) and
dismissal stored **server-side per user per org** so "closable, re-enable in
the account" works across devices.

### Checklist card (frontend)

New `web/dash0/src/components/dashboard/onboarding-checklist.tsx`, rendered in
`dashboard-page.tsx` when the org has ≥ 1 check and the checklist is not
dismissed. It **replaces `FirstResultCelebration` entirely** — delete the
component, its localStorage key, and the now-unused `celebration.*` keys from
all four locales; two competing nudges is worse than either.

Items and their derivation (each one existing list endpoint the dashboard can
query with a long `staleTime`, and `enabled: false` once dismissed so the
dismissed state costs zero requests):

1. **Create your first check** — `stats.total >= 1`. Always checked when the
   card is visible; that is deliberate — opening on a completed step is the
   strongest "this thing works" signal onboarding can send.
2. **Get alerted** — the org has ≥ 1 enabled notifiable integration
   (`GET /orgs/:org/integrations`). Any notifiable type counts — a Slack-only
   org must not be nagged about email. Primary action: **"Send me a test
   alert"** → `POST /orgs/:org/integrations/:uid/test` (route
   `server/internal/app/server.go:1563`, handler
   `server/internal/handlers/integrations/handler.go:283-296`) against the
   default (else first) email integration. The endpoint returns 200 with a
   `success` field — surface both outcomes as a toast; a failure (typically
   SMTP unconfigured on self-hosted) must render the returned message, not
   crash and not pretend. Secondary: link to the integrations page.
3. **Weekly uptime report** — ≥ 1 enabled report schedule
   (`GET /orgs/:org/reports` list). Links to
   `/orgs/$org/organization/report-schedules`.
4. **Publish a status page** — ≥ 1 status page exists. Primary action links to
   `/orgs/$org/status-pages/new?checkUid=<first check uid>` (the
   create-from-check flow of spec 2026-08-28-16; plain
   `/status-pages/new` until that lands).
5. **Invite a teammate** — org has > 1 member or a pending invitation
   (whichever the existing members/invitations endpoints expose cheaply).
   Links to the members page.

With spec 2026-08-28-15 in place a new org starts at 3/5 — that is the point,
not a bug. All three specs are independent: the checklist derives everything
from state, so it is correct with or without the seeding.

When all items are complete, show a brief "you're all set" state and
self-dismiss by writing the same dismissal key — the card must not squat on
the dashboard of a fully configured org.

Card mechanics: dismiss "X" writes the dismissal; fully responsive (mobile
rule); all strings in all four locales
(`web/dash0/src/locales/{en,fr,de,es}/dashboard.json`) with `bun run
test:unit` green (locale-key parity); add the checklist pattern to the design
reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) per the
frontend convention.

### Per-user UI state (backend)

The dismissal needs server-side, per-user, per-org storage. The storage layer
already exists and is unused: user-scoped `state_entries`
(`models.NewUserStateEntry`, `server/internal/db/models/state.go:39-49`, with
`SetStateEntry`/`GetStateEntry`/`DeleteStateEntry` in both engines). Add a
minimal authenticated API over it:

- `GET /api/v1/me/ui-state/:key` → `{ "value": <json> }` or 404
- `PUT /api/v1/me/ui-state/:key` with a JSON body → stores it
- `DELETE /api/v1/me/ui-state/:key`

Scoped to the authenticated user (`RequireAuth`, no org in the path — the org
lives in the key). Constrain it so it cannot become a junk drawer: allowlist
the key shape (v1: `onboarding.<orgUid>` only, return `VALIDATION_ERROR`
otherwise) and cap the value size (a few KB). The checklist stores
`{ "dismissedAt": <ts> }` under `onboarding.<orgUid>`.

### Re-enable in the account

In the account area (alongside the existing `account.*` routes, e.g.
`account.notifications.tsx`), add a "Show the getting-started checklist"
control per org membership that `DELETE`s the key. If no natural account
settings page exists yet for this, add a small preferences section rather than
a whole new page.

### Tests

- Backend: handler/service tests for the ui-state endpoints — key allowlist,
  size cap, user isolation (user A cannot read user B's key), both engines.
- E2E (Playwright): fresh test org → create a check → checklist appears with
  item 1 checked; create an integration → item 2 flips without any stored
  step-flag; dismiss → survives a full reload (proves it is not
  localStorage); re-enable from the account page → card returns. Test-alert
  button: click it and assert the toast renders the endpoint's message for
  both a `success: true` and a `success: false` response.
- Unit: locale parity across en/fr/de/es for every new key.

Out of scope: showing the checklist before the first check exists (the
empty-state hero owns that moment), any change to the super-admin activation
funnel, and per-step "mark as done" overrides — derived state only.

## Implementation Plan

### 1. Backend — user-scoped state storage (`internal/db`)

`state_entries` already carries `user_uid`, but every accessor
(`GetStateEntry`/`SetStateEntry`/`DeleteStateEntry`) filters on
`organization_uid` only — with `orgUID == nil` they match *every* user's
global row, so `models.NewUserStateEntry` has never been usable. Add three
user-scoped siblings to `db.Service` and both engines:

- `GetUserStateEntry(ctx, userUID, key)`
- `SetUserStateEntry(ctx, userUID, key, value, ttl)`
- `DeleteUserStateEntry(ctx, userUID, key) (bool, error)`

All three filter `user_uid = ? AND organization_uid IS NULL`. The unique
constraint is `(organization_uid, key)`, which does not cover user rows, so
the upsert is UPDATE-then-INSERT (resurrecting a soft-deleted row by clearing
`deleted_at`) — exactly the precedent the existing `orgUID == nil` branch
sets. No migration.

### 2. Backend — `GET/PUT/DELETE /api/v1/me/ui-state/:key`

New package `internal/handlers/uistate` (handler + service), mounted on
`api.NewGroup("/me/ui-state").Use(authMiddleware.RequireAuth)`. No org in the
path: the org lives in the key.

- **Key allowlist.** v1 accepts `onboarding.<org>` only, where `<org>` is a
  slug or a UID; anything else is `VALIDATION_ERROR`. The suffix is
  **resolved to the organization's UID server-side** (uid → slug → previous
  slug) and the entry is stored under `onboarding.<orgUID>`, so a rename
  never orphans a dismissal while the dashboard keeps passing the slug it
  already has. An unresolvable org is `NOT_FOUND`.
  Deliberately no membership gate: the row is the caller's own private UI
  state, it leaks nothing, and a gate would 403 a super-admin browsing an org
  they are not a member of.
- **Size cap.** The PUT body is read through a 4 KiB `io.LimitReader`;
  anything larger is `VALIDATION_ERROR`. The body must be a JSON object.
- Responses: `200 {"value": {...}}` / `404` on GET, `200 {"value": {...}}` on
  PUT, `204` on DELETE.

### 3. Frontend — derivation module + card

- `src/lib/onboarding-checklist.ts`: pure `deriveOnboardingSteps(input)`
  returning the five steps with `done` flags and `pickTestAlertIntegration()`
  (default email → first enabled email → none). Unit-tested directly.
- `src/components/dashboard/onboarding-checklist.tsx`: the card. Rendered from
  `dashboard-page.tsx` whenever `stats.total >= 1`. Every derivation query
  (`useIntegrations`, `useReportSchedules`, `useStatusPages`, `useMembers`,
  `useInvitations`) gets an `opts` argument for `enabled` + a long
  `staleTime`, and is `enabled: false` while the dismissal is loading or
  already set. `useInvitations` is additionally gated on the admin role —
  that endpoint is admin-only and would 403 for a plain member.
- Steps: check (`stats.total >= 1`), alerting (≥1 enabled notifiable
  integration), weekly report (≥1 enabled report schedule), status page (≥1
  page → `/orgs/$org/status-pages/new?checkUid=<first check uid>`), teammate
  (>1 member or ≥1 pending invitation).
- "Send me a test alert" → `useTestIntegration`; both `success: true` and
  `success: false` render a toast built from the returned payload.
- All complete → "you're all set" state that writes the dismissal itself.
- Dismiss "X" writes `{ dismissedAt: <iso> }`; responsive layout; all copy in
  `dashboard.json` for en/fr/de/es.

### 4. Delete `FirstResultCelebration`

Component, its `solidping_celebrated_first_result_*` localStorage key, the
two comments that point at it, and the `celebration.*` block in all four
locales.

### 5. Re-enable control

A "Getting started" preferences card on `/orgs/$org/account/profile` that
`DELETE`s `onboarding.<org>` for the org in the URL and reports the outcome
via toast.

### 6. Design reference

New `OnboardingChecklistSection` in `design-reference.tsx` (+ `SECTIONS`
entry) rendering the card's presentational shell.

### 7. Tests

- `internal/db/service_test.go`: `testUserStateEntries` — set/get/delete,
  and **user isolation** (user B cannot read user A's key) — runs on both
  engines.
- `internal/handlers/uistate/service_test.go`: key allowlist + size cap.
- `test/integration/uistate_test.go`: full HTTP round trip, 404, key
  rejection, size cap, and cross-user isolation over the wire.
- `web/dash0/src/lib/onboarding-checklist.test.ts`: derivation + integration
  pick.
- `web/dash0/src/components/dashboard/onboarding-checklist.test.ts`: locale
  parity for every new key across en/fr/de/es, and proof `celebration.*` is
  gone.
- `web/dash0/e2e/onboarding-checklist.spec.ts`: throwaway owned org (which,
  per spec 2026-08-28-15, already starts with a seeded email integration and
  weekly report schedule — the test deletes the integration first so
  "item 2 flips" is a real transition), check creation, dismissal surviving a
  full reload, and re-enable from the account page; plus mocked
  `success: true` / `success: false` test-alert toasts.
