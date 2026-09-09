---
model: sonnet
effort: low
---

# Organizations on the activation funnel are dead text — they should open that org's audit log

## Problem

The super-admin **Server → Activation** page (`/orgs/$org/server/activation`,
`web/dash0/src/routes/orgs/$org/server.activation.tsx`) lists every
organization with its activation milestones (signup, first check, first
result, first notifier, first page). The natural next question when a row
looks off — "signed up three weeks ago, still no check" — is *what did they
actually do?*, and the answer is that org's audit log
(`/orgs/$org/organization/audit`,
`web/dash0/src/routes/orgs/$org/organization.audit.tsx`).

Today the organization cell is plain text (`server.activation.tsx:92-95`:
name in `font-medium`, slug in muted small text), so the operator has to copy
the slug, edit the URL by hand and navigate. The sibling **Server →
Entitlements** list already solves the same problem the right way: the org
name is a `Link` to a per-org page
(`web/dash0/src/routes/orgs/$org/server.entitlements.index.tsx:126-134`,
`text-primary hover:underline`, slug kept as the muted second line).

Nothing blocks the link on the permission side — this is purely a missing
affordance:

- The funnel is served by `GET /api/v1/system/activation` behind
  `RequireSuperAdmin` (`server/internal/app/server.go:1557-1559`, `:1573`), so
  whoever sees the page is a super admin.
- Super admins cross organizations on their claims alone: the org layout
  deliberately skips its auto org-switch for them
  (`web/dash0/src/routes/orgs/$org.tsx:1013-1016`, `:1026-1027`) and the
  accessible-org redirect leaves them untouched (`:1065-1067`).
- The audit page reads `GET /orgs/:org/events` (`useAuditEvents`,
  `web/dash0/src/api/hooks.ts:2036`), registered under `orgGroup`
  (`server.go:1425-1426`) whose role check lets super admins through
  (`server/internal/middleware/auth.go:657-658`). Its `useMembers(org)` actor
  lookup (`organization.audit.tsx:211`) goes through the same gate.
- The **Organization** section's own guard passes: `isAdmin` is derived from
  `role === "superadmin"` too (`web/dash0/src/contexts/AuthContext.tsx:314-317`),
  so `organization.tsx:57` does not bounce a super admin back to the org root.
- The e2e suite already asserts "a super admin keeps the org the URL names"
  (`web/dash0/e2e/accessible-org-redirect.spec.ts:176-180`; `test@test.com`
  is a super admin in `server/test/testdata/testdata.go`).

## Proposal

Make the organization cell a link to that organization's audit log, mirroring
the entitlements list.

- In `server.activation.tsx:92-95`, wrap the name in

  ```tsx
  <Link
    to="/orgs/$org/organization/audit"
    params={{ org: row.slug }}
    className="font-medium text-primary hover:underline"
    title={t("activation.openAudit", "Open audit log")}
    data-testid={`activation-org-link-${row.slug}`}
  >
    {row.name || row.slug}
  </Link>
  ```

  and keep the slug as the muted second line, exactly as
  `server.entitlements.index.tsx:126-134` does. Add
  `data-testid={\`activation-row-${row.slug}\`}` on the `TableRow`, matching
  the `entitlements-row-…` convention.
- The `params.org` is the **row's** slug, not the page's — the whole point is
  to land in the target org's context. No search params: the audit page's
  `family` / `actor` / `target` / `ip` / cursor filters
  (`organization.audit.tsx:94-121`) all default sensibly, and the operator
  narrows from there.
- Locale key `activation.openAudit` in
  `web/dash0/src/locales/{en,fr,de,es}/server.json` next to the existing
  `activation.*` block (`en/server.json:457-467`); `locale-parity.test.ts`
  fails the build otherwise. The `t()` calls in this file carry English
  defaults inline, keep that style.
- The link is text, not a button: the design reference's link convention
  (`web/dash0/src/routes/orgs/$org/design-reference.tsx:648`, `:1174-1186`) and
  the entitlements precedent both use `Link` with `text-primary
  hover:underline` for in-table navigation. No new primitive, no
  design-reference change.
- Touch target: the link is the org name, so it is at least as tappable as
  the entitlements one; nothing else on the row is interactive, so there is no
  competing click target on mobile.

### Tests

Playwright, alongside `web/dash0/e2e/server-entitlements.spec.ts` (same
super-admin setup):

1. As `test@test.com`, open `/orgs/test/server/activation`; the row for the
   `test` org carries `activation-org-link-test` with `href` ending in
   `/orgs/test/organization/audit`.
2. Provision a second org the way `accessible-org-redirect.spec.ts:24-34`
   does — seed a zero-org user through the test-only `POST /api/v1/test/users`,
   log in as it, create an organization — so the super admin is **not** a
   member of it. Then, as `test@test.com`, reload the funnel, click that
   org's link, and assert the URL is
   `/orgs/<other>/organization/audit` **and** the audit heading
   (`events:audit.title`, `organization.audit.tsx:267`) renders — not
   "Permission Denied", not a redirect back to `/orgs/<other>`. This is the
   case that proves the cross-org path works for a super admin who is not a
   member.
3. Unit: `bun run test:unit` for the new key in all four locales.

### Out of scope

- Linking the milestone columns to the underlying object (the first check,
  the first incident): the funnel only stores timestamps
  (`server/internal/handlers/system/service.go:105-115`), not the uids, so
  that is a backend change and a separate spec.
- Making the same cell link on other server pages; the entitlements list
  already has its own per-org destination.
