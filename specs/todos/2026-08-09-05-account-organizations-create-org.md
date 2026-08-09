---
model: sonnet
effort: high
---

# A user who already belongs to an organization has no way to create another one

## Problem

`POST /api/v1/orgs` is open to **any authenticated user** — the route is
registered with `RequireAuth` only ([`server/internal/app/server.go:588`](../../server/internal/app/server.go)),
and the creator becomes the org **owner**
([`server/internal/handlers/auth/service.go:2660`](../../server/internal/handlers/auth/service.go)).

But dash0 exposes that capability in exactly one place: the `CreateOrgCard` on
the `/no-org` page ([`web/dash0/src/routes/no-org.tsx:105`](../../web/dash0/src/routes/no-org.tsx)).
That route is only ever reached by a **zero-org** user — after login
([`routes/orgs/$org/login.tsx:340`](../../web/dash0/src/routes/orgs/$org/login.tsx))
or after registration confirmation
([`lib/confirm-registration-handoff.ts:38`](../../web/dash0/src/lib/confirm-registration-handoff.ts)).

So a user who already belongs to one or more orgs can only create another by
hand-typing `/dash0/no-org` or by calling the API directly. The sidebar footer
dropdown lists the user's other orgs to switch to
([`components/layout/AppSidebar.tsx:345`](../../web/dash0/src/components/layout/AppSidebar.tsx))
but offers no way to make a new one — and that block is itself gated on
`organizations.length > 1`, so a single-org user sees no organization section
at all.

## Proposal

Put the entry point in the **account section** (`/orgs/$org/account/*`), not in
the sidebar dropdown. Organization membership is a property of *the user*, so
it belongs next to profile / security / sessions / tokens rather than in a
navigation menu.

### 1. New account tab: Organizations

Add a tab to the account layout
([`routes/orgs/$org/account.tsx:14`](../../web/dash0/src/routes/orgs/$org/account.tsx)),
after `Tokens`:

```ts
{ label: t("nav:organizations"), path: "/orgs/$org/account/organizations" },
```

Route files, following the `checks.tsx` / `checks.index.tsx` / `checks.new.tsx`
triple already used in this directory:

- `account.organizations.tsx` — layout, `<Outlet />` only.
- `account.organizations.index.tsx` — the list.
- `account.organizations.new.tsx` — the create form.

**List page** renders `organizations` from `useAuth()`
(`OrganizationSummary`: `slug`, `name?`, `logoUrl?`, `role` —
[`contexts/AuthContext.tsx:36`](../../web/dash0/src/contexts/AuthContext.tsx)):
one row per org with logo (fall back to the `Building` icon, as the sidebar
does), name, slug, the user's role, a "current" marker on the active org, and a
switch action for the others (`switchOrg`, same as
[`AppSidebar.tsx:154`](../../web/dash0/src/components/layout/AppSidebar.tsx)).
A primary `New organization` button links to `.../organizations/new`.

Use the primitives from
[`routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
— check it before writing any of this UI, and extend it if a needed pattern is
missing.

### 2. Extract the create form into a shared component

Move `CreateOrgCard` out of `no-org.tsx` into
`components/shared/create-org-card.tsx` (taking `slugify` with it) and render
it from both `/no-org` and `/orgs/$org/account/organizations/new`. Do not
duplicate the form — the session handling below is the load-bearing part and
must exist in exactly one place.

The component needs one new prop for the cancel/back target: the account route
returns to `/orgs/$org/account/organizations`, while `/no-org` has no org to go
back to and keeps its existing sign-out affordance. `/no-org` must keep working
exactly as it does today, including the membership-request card beside it.

### 3. Session adoption is mandatory

`POST /api/v1/orgs` returns **201 with a fresh session scoped to the new org**
(`accessToken` / `refreshToken` / `expiresIn` / `tokenType`) — see
[`service.go:2708`](../../server/internal/handlers/auth/service.go) and the
comment on `useCreateOrg` ([`api/hooks.ts:2258`](../../web/dash0/src/api/hooks.ts)).
The caller **must** `setSession(...)` before navigating, then best-effort
`refreshUser()` so the sidebar switcher and the new Organizations tab see the
org, then navigate to `/orgs/$newSlug`.

This is the trap the account-section flow makes easier to hit than `/no-org`
did: the user arrives holding a *valid* token for their current org, so
skipping adoption fails as a 403 on the new org rather than as an obvious
"logged out". Cover it with a test.

### 4. Errors

Surface the API's error shapes in the form, not as a generic failure:
`422 VALIDATION_ERROR` → "Slug must be 3–20 characters, lowercase alphanumeric
with hyphens" (regex `^[a-z0-9][a-z0-9-]{1,18}[a-z0-9]$`), `409 CONFLICT` →
"that slug is already taken". Both already come back with usable messages from
[`handlers/auth/handler.go:641`](../../server/internal/handlers/auth/handler.go).

### 5. i18n

The form's strings currently live under the `auth` namespace as `noOrg.*`.
Moving the component means either renaming them to something neutral
(`createOrg.*`) and updating `/no-org`, or having the shared component read
neutral keys added alongside. Add `nav:organizations` for the tab label.
Update every locale file, not just English.

### 6. Tests

- Playwright E2E: a user who **already has an org** opens
  `/orgs/$org/account/organizations`, creates a second org, and lands on the
  new org's dashboard with a working session — i.e. an org-scoped call
  succeeds, proving the new token was adopted. Add near the existing
  [`e2e/org-profile.spec.ts`](../../web/dash0/e2e/org-profile.spec.ts).
- Regression: `/no-org` still creates an org for a zero-org user after the
  component extraction.
- The list shows the current org marked as current and lists sibling orgs.

## Non-goals

- **No new item in the sidebar footer dropdown.** The switcher at
  [`AppSidebar.tsx:345`](../../web/dash0/src/components/layout/AppSidebar.tsx)
  stays exactly as it is; discovery happens through the account section.
- No change to `POST /api/v1/orgs` or to any server-side permission.
- No `/orgs/new` top-level route — that segment collides with the `$org` param
  and would permanently shadow an org whose slug is literally `new`, which the
  slug regex allows.

## Open questions

- **Entitlements / SaaS.** `CreateOrg` consults no entitlement, so a visible
  self-serve button means unlimited free orgs per user under
  `SP_DEPLOYMENT_MODE=saas`. Out of scope to fix here, but confirm this is
  acceptable before shipping the button rather than discovering it later.
- **Tab overflow.** `TabNav` is a plain `flex gap-4 border-b`
  ([`components/shared/tab-nav.tsx:13`](../../web/dash0/src/components/shared/tab-nav.tsx))
  with no horizontal scroll. A seventh account tab risks overflowing on
  narrow screens, and the repo requires every page to be usable on mobile.
  Either add `overflow-x-auto` to `TabNav` (affects every tabbed page — verify
  the others) or shorten the label.

## Resolved open questions

> **Entitlements / SaaS.** *(`CreateOrg` consults no entitlement, so a visible
> self-serve button means unlimited free orgs per user under
> `SP_DEPLOYMENT_MODE=saas`. Out of scope to fix here, but confirm this is
> acceptable before shipping the button.)*

**Decision: confirmed acceptable — ship the button, add no entitlement check.**

Two reasons, both of which mean this spec must not grow an entitlements story:

- `SP_DEPLOYMENT_MODE=saas` is only ever run by **Webingenia** (solidping.io).
  It is not a mode a self-hoster is expected to turn on, so unlimited self-serve
  orgs is an internal operational concern on one deployment, not a product hole.
- The capability already exists and is already reachable: `POST /api/v1/orgs`
  takes any authenticated user, and `/no-org` already renders this exact form.
  The button surfaces an existing capability rather than granting a new one.

If org-creation limits are ever wanted, they belong in their own spec covering
the API, `/no-org` and this page together — **do not** add a `maxOrgs`-style
entitlement, a SaaS-mode conditional, or any other gate as part of this work.

> **Tab overflow.** *(`TabNav` is a plain `flex gap-4 border-b` with no
> horizontal scroll … Either add `overflow-x-auto` to `TabNav` (affects every
> tabbed page — verify the others) or shorten the label.)*

**Decision: fix `TabNav`, keep the full "Organizations" label.**

- Add `overflow-x-auto` to the `TabNav` container
  ([`components/shared/tab-nav.tsx:13`](../../web/dash0/src/components/shared/tab-nav.tsx)),
  with `whitespace-nowrap` (and `shrink-0`) on the items so they scroll instead
  of wrapping or squashing.
- This is a shared component: **walk every tabbed page** that uses it and
  confirm nothing regressed — no stray scrollbar on desktop where the tabs
  already fit, and the bottom border still spans the row.
- Check it at a narrow viewport as part of the E2E work, since the repo requires
  every page to be usable on mobile.

Shortening the label to "Orgs" was rejected: it only defers the same overflow to
the next tab that gets added.
