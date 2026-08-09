---
model: sonnet
effort: medium
---

# The account Organizations list offers no shortcut to an org's settings for admins/owners

## Problem

The account-level Organizations page
(`/dash0/orgs/$org/account/organizations`, source:
[web/dash0/src/routes/orgs/$org/account.organizations.index.tsx](../../web/dash0/src/routes/orgs/$org/account.organizations.index.tsx))
lists every organization the user belongs to, with its logo, name, slug and
role, plus a "Switch" button on non-current rows. But when the user is an
**admin or owner** of an org, there is no way to jump from that list to the
org's settings page (`/orgs/$org/organization/settings`) — they have to
switch to the org, then find Organization → Settings in the sidebar.

Each row already knows the user's role in that org:
`OrganizationSummary.role`
([AuthContext.tsx:36-43](../../web/dash0/src/contexts/AuthContext.tsx)), and
the admin-capable role strings are `owner`, `admin`, and `superadmin` — the
same set AuthContext uses to derive `isAdmin`
([AuthContext.tsx:226-229](../../web/dash0/src/contexts/AuthContext.tsx)).

## Proposal

On each organization row where `o.role` is `admin`, `owner`, or
`superadmin`, render a **"Settings" button** alongside the existing actions:

- **Current org row**: a simple `Button variant="outline" size="sm" asChild`
  wrapping a `<Link to="/orgs/$org/organization/settings" params={{ org: o.slug }}>`.
- **Other org rows**: navigating straight to another org's settings would run
  against a session still resolved to the current org (the
  `/orgs/$org/organization` layout gates on the *session's* `user.isAdmin`,
  see [organization.tsx:41](../../web/dash0/src/routes/orgs/$org/organization.tsx)).
  Mirror the existing `handleSwitch` pattern
  ([account.organizations.index.tsx:31-50](../../web/dash0/src/routes/orgs/$org/account.organizations.index.tsx)):
  call `switchOrg(slug)` first, then `navigate` to
  `/orgs/$org/organization/settings` under the new slug instead of back to
  the organizations list.

Details:

- Use a `Settings` (gear) lucide icon on the button; keep the label visible
  (`hidden sm:inline` collapsing like the "New organization" button) so the
  row stays usable on mobile.
- Add `data-testid={`organization-settings-${o.slug}`}` for tests.
- New i18n keys in the `account` namespace (`organizations.settings`) for
  **all shipped locales** — check `web/dash0/src/locales/` (or wherever the
  `account` namespace JSON lives) and add every language, not just English.
- Reuse existing primitives per the design reference
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) — no new
  components needed.
- Role gate is per-row (`o.role`), *not* the session-level `user.isAdmin`,
  since the user's role can differ across orgs.

### Tests

Extend the existing Playwright coverage for the account Organizations tab
(`web/dash0/e2e/` — the spec added alongside commit `623a416c`):

- Admin/owner sees the Settings button on their org row, and clicking it on
  the current org lands on `/orgs/$org/organization/settings`.
- Clicking Settings on a *non-current* org switches the session and lands on
  that org's settings page.
- A row where the user's role is not admin/owner/superadmin (e.g. a plain
  member/user role) shows no Settings button — negative control.
