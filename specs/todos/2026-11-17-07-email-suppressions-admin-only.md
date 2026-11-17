---
model: opus
effort: low
---

# Viewers can list the org's email suppressions (third parties' addresses)

## Problem

Found by the security scan of 2026-11-17 (finding AP2). Severity: Low. Any `viewer` of the org can trigger it, and so can a viewer's PAT.

- `GET /api/v1/orgs/:org/email-suppressions` is registered on `orgGroup` (`server/internal/app/server.go:2055-2057`). That group's write floor only gates non-GET routes (`server.go:941-947`), so every role can read the list.
- Neither `ListSuppressions` (`server/internal/handlers/emailsuppressions/handler.go:27-36`) nor `Service.List` (`service.go:50-67`) checks the role.
- Each row carries a recipient email address, the reason (bounce, complaint, unsubscribe), the check it is scoped to and the date. These are mostly third parties' addresses (people who receive the org's alert and report emails), and the list shows who opted out of what.
- This is the class already fixed for status page subscribers (finding AC5, spec `specs/done/2026/11/2026-11-02-04-status-page-subscribers-admin-only.md`, now on `orgStatusPagesAdmin`, `server.go:2190-2193`). The suppression list was missed.
- dash0 shows the list on the email integration page (`web/dash0/src/routes/orgs/$org/integrations.$integrationUid.tsx:172`, hook `web/dash0/src/api/hooks.ts:3078-3100`).

## Proposal

1. Put both suppression routes on an admin-gated group (`RequireAuth`, `RequireOrgAccess`, `RequireOrgAdmin`), like `orgStatusPagesAdmin`. Answer `403 FORBIDDEN` to viewers and users. The DELETE (re-subscribe someone) moves to admin too: it overrides a recipient's own opt-out.
2. In dash0, show the suppression card only to admins, with the usual "Permission Denied" handling on a 403 (never a redirect).
3. Note the role in `wiki/api-specification/` and in `server/internal/app/openapi/openapi.yaml` for both endpoints.

## Tests

- The app router test that builds the real middleware chain: a viewer and a user get 403 on `GET` and `DELETE /email-suppressions`. This fails before the fix. Positive control: an admin gets 200 with the rows, and 204 on delete.
- dash0 Playwright or component test: a viewer opening the email integration page sees no suppression list and no error loop.

## Open questions

- Should `user` (write) members keep the re-subscribe action? **Recommended:** no, admin only, like the subscriber list: it is third parties' personal data and overrides their own opt-out.
