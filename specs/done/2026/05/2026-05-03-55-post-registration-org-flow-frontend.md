# Post-registration org flow (frontend)

**Depends on:** spec #54 (backend).

## Context

After confirming their email, a user with no organization currently
lands on `/no-org` with limited UX — no clear next step beyond "log
out". This spec turns it into a real entry point with two CTAs:
**create a new org** (the user becomes its admin) or **request to
join an existing org** by slug. It also adds the admin-side
"Pending requests" page and surfaces the new auto-join regex
validation errors inline on the org-settings page.

The status0 frontend is unaffected (registration lives in dash0 only).

## Honest opinion

1. **Two cards on `/no-org`, no third option.** The user already has
   the email confirmation in hand; the screen exists to remove
   ambiguity, not to offer choice paralysis. The "request to join"
   card asks for an org slug — the user has to know it, by design
   (see spec #52 for why we don't show an org list).
2. **The admin "Pending requests" page should look exactly like the
   invitations page.** Mirroring the existing UI vocabulary keeps
   review small and onboarding obvious. Don't reinvent the
   table-with-actions pattern.
3. **The "test against email" preview on the regex field is UX
   sugar, not validation.** Server validation remains the source of
   truth (`INVALID_AUTO_JOIN_REGEX`); the client preview is a
   confidence check for the admin filling in the field.
4. **Pending-request feedback should be visible after refresh.** A
   user who closes the tab and comes back to `/no-org` should still
   see "your request to {org} is pending". This is what the
   `pendingMembershipRequests` field on `MeResponse` is for — the
   no-org screen reads it on mount.

## Scope

**In**

- Rebuild `web/dash0/src/routes/no-org.tsx`:
  - Card 1 — "Start a new organization" (name + slug → existing
    `POST /api/v1/orgs`).
  - Card 2 — "Join an existing organization" (org slug + optional
    message → `POST /api/v1/auth/membership-requests`).
  - Pending/rejected requests list with cancel button.
- New admin route
  `web/dash0/src/routes/orgs/$org/organization.requests.tsx`:
  pending-requests table with approve / reject dialogs.
- `organization.settings.tsx`: surface `INVALID_AUTO_JOIN_REGEX`
  inline; richer help text with examples; optional client-side
  "test against email" preview field.
- Sidebar: add "Pending requests" link in the Organization section,
  with a numeric badge sourced from the pending count. Visible
  only when the current user is an admin.
- New typed hooks (mirroring the existing invitation hook style):
  - `useCreateMembershipRequest()`
  - `useMyMembershipRequests()`
  - `useCancelMembershipRequest()`
  - `useOrgMembershipRequests(org, {status?})`
  - `useApproveMembershipRequest(org)`
  - `useRejectMembershipRequest(org)`
- i18n strings (English + any languages currently translated for
  comparable pages).
- Playwright e2e covering the full flow.

**Out**

- Org browser / search.
- DNS verification UI (matches backend scope).
- Status0 changes.
- Login / register screen redesigns.

## Design

### 1. `/no-org` redesign

File: `web/dash0/src/routes/no-org.tsx`. Two-column responsive layout
(stacks vertically on narrow viewports). On mount, fetch
`pendingMembershipRequests` from `/auth/me` (it's already loaded by
`AuthContext`).

**Card 1 — Start a new organization**
- Fields: `name` (required), `slug` (required, validated against the
  same `^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$` regex used elsewhere).
- Submit → `POST /api/v1/orgs`. On success: set the new org as
  current org in `AuthContext`, navigate to `/orgs/$slug/`.
- The user becomes admin (backend already does this).

**Card 2 — Join an existing organization**
- Fields: `orgSlug` (required), `message` (optional textarea).
- Submit → `POST /api/v1/auth/membership-requests`. On success:
  show pending state inline ("Waiting for an admin of {org} to
  approve.") with a Cancel button.
- 409 `ALREADY_A_MEMBER` → friendly "you're already a member" with
  a link to switch to that org.
- 409 `REQUEST_PENDING` → display the existing pending request.
- 409 `REQUEST_COOLDOWN_ACTIVE` → "you can re-request after
  {decided_at + cooldown_days}".
- 404 `ORGANIZATION_NOT_FOUND` → "no organization with that slug".

**Pending list (below the cards)**
- Renders one row per item in `pendingMembershipRequests` (status
  `pending` or `rejected`).
- Pending → Cancel button (`DELETE
  /api/v1/auth/membership-requests/{uid}`).
- Rejected → "Rejected on {date}: {reason}" + "Request again"
  button (disabled with tooltip until cooldown expires).

### 2. Admin: Pending requests page

New file:
`web/dash0/src/routes/orgs/$org/organization.requests.tsx`. Mirrors
`organization.invitations.tsx` (table + create/manage dialogs).

Table columns: Requester (name + email), Message, Submitted at,
Status, Actions.

Row actions:
- **Approve** → opens dialog with a role dropdown (defaults `user`,
  options `user` / `viewer` / `admin`). Submit calls
  `POST /api/v1/orgs/{org}/membership-requests/{uid}/approve` with
  the chosen role.
- **Reject** → opens dialog with optional reason textarea. Submit
  calls
  `POST /api/v1/orgs/{org}/membership-requests/{uid}/reject`.

Both dialogs invalidate the requests query on success and toast a
confirmation.

Status filter at the top: defaults to "Pending"; admin can switch
to "Rejected" / "Approved" / "All" for history.

Visible only when `currentUserRole === 'admin'`. Otherwise the
route redirects to the org dashboard.

### 3. Org settings — auto-join regex hardening

File:
`web/dash0/src/routes/orgs/$org/organization.settings.tsx`.

- The existing `registrationEmailPattern` field stays. On save
  failure with code `INVALID_AUTO_JOIN_REGEX`, render the
  server-supplied `detail` inline beneath the input (red text,
  matches existing form-error styling).
- Help text reads (i18n key `settings.autoJoinHelp`): "Examples:
  `.+@acme\.com` or `[a-z]+@(eu|us)\.acme\.com`. The pattern must
  include `@` and cannot match free-mail providers like Gmail or
  Outlook."
- Add an optional **"Test against email"** input below the pattern
  field. Live UX-only preview: tries `new RegExp(pattern)` against
  the typed email and shows ✓ matches / ✗ doesn't match. Catches
  typos before save. Doesn't replace server validation.

### 4. Sidebar entry

File: `web/dash0/src/components/layout/AppSidebar.tsx`.

In the Organization section (above or below "Invitations"), add a
"Pending requests" link with a numeric badge showing the count of
pending requests, sourced from
`useOrgMembershipRequests(org, {status:'pending'})`. Visible only
when the current member's role is `admin`.

### 5. Hooks & types

File: `web/dash0/src/api/hooks.ts` (and the corresponding types
file). Six new hooks listed in Scope. Type:

```ts
type MembershipRequest = {
  uid: string;
  organization: { slug: string; name: string };
  user?: { uid: string; name: string; email: string }; // populated only on org-scoped GETs
  message?: string;
  status: 'pending' | 'approved' | 'rejected' | 'cancelled';
  decisionReason?: string;
  decidedAt?: string; // RFC3339
  createdAt: string;
};
```

Mutations should invalidate both `useMyMembershipRequests` and
`useOrgMembershipRequests` queries appropriately.

## Files affected

| File / dir                                                              | Change                                                                |
|-------------------------------------------------------------------------|-----------------------------------------------------------------------|
| `web/dash0/src/routes/no-org.tsx`                                       | Full redesign — two cards + pending list.                             |
| `web/dash0/src/routes/orgs/$org/organization.requests.tsx`              | New admin page (approve / reject).                                    |
| `web/dash0/src/routes/orgs/$org/organization.settings.tsx`              | Inline regex error, examples, "test against email" field.             |
| `web/dash0/src/api/hooks.ts`                                            | Six new hooks for membership requests.                                |
| `web/dash0/src/api/types.ts` (or equivalent)                            | `MembershipRequest` type.                                             |
| `web/dash0/src/contexts/AuthContext.tsx`                                | Surface `pendingMembershipRequests` from `/auth/me`.                  |
| `web/dash0/src/components/layout/AppSidebar.tsx`                        | "Pending requests" link with count badge (admin-only).                |
| `web/dash0/src/locales/*.json`                                          | New i18n keys.                                                        |
| `web/dash0/e2e/membership-requests.spec.ts` (new)                       | Playwright e2e for the full flow.                                     |

## Tests

- Playwright: register → confirm → land on `/no-org` → create org
  `acme` → land in `/orgs/acme/` as admin.
- Playwright: register → confirm → request to join `acme` → admin
  approves → user logs in and sees `acme` in their orgs.
- Playwright: admin rejects → user sees rejected status; tries to
  re-request → button disabled with cooldown tooltip.
- Playwright: settings page rejects `.*` with inline error from
  server; accepts `.+@acme\.com`.
- Playwright: "Test against email" preview correctly reports ✓ / ✗
  for a few sample emails.
- Unit (vitest if present in this repo): hooks POST/GET against the
  new endpoints; mutation invalidation works.

## Verification

1. `make dev`.
2. Register `bob@unknown.com`, confirm via the email link.
3. Land on `/no-org`. Click **Start a new organization**, create
   `acme` → land in `/orgs/acme/` as admin.
4. Sign out. Register `alice@unknown.com`, confirm.
5. On `/no-org`, type slug `acme` + a message, submit.
6. Sign in as the `acme` admin. Sidebar shows "Pending requests
   (1)". Click → see Alice's row → approve with default role `user`.
7. Sign in as Alice → `acme` appears in her orgs, role `user`.
8. Sign out. Register `mallory@spam.example`, request to join
   `acme`, sign in as admin, reject with reason "spam".
9. Sign in as Mallory → `/no-org` shows the rejected request with
   reason; "Request again" button is disabled with a cooldown
   tooltip.
10. As `acme` admin, go to settings → set
    `registrationEmailPattern` to `.*` → see inline error
    `INVALID_AUTO_JOIN_REGEX`. Set to `.+@acme\.com` → saves; type
    `bob@acme.com` in "Test against email" → shows ✓ matches; type
    `bob@gmail.com` → ✗ doesn't match.
