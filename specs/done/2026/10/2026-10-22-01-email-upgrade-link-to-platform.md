---
model: sonnet
effort: medium
---

# "See plans" in emails must link to the platform, never to billing

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/15

## Problem

The usage-cap email (`server/internal/email/templates/usage-cap-reached.html:22`, button
`alerts.usageCap.seePlans`, plain-text twin at line 35) links to `{{.UpgradeURL}}`. That value comes from
`Entitlements.UpgradeURL` (`server/internal/entitlements/usage_reserve.go:283`), called from
`server/internal/jobs/jobtypes/phone_cap_notice.go:132`. It expands the system parameter
`entitlements.upgrade_url_template` (env `SP_ENTITLEMENTS_UPGRADE_URL_TEMPLATE`, `server/internal/app/saas.go:60`),
which in SaaS points at the billing service (`https://solidping-billing.k8xp.com/app/upgrade?org=<slug>`).

Reporter's rule: billing must never be the target of an email link, everything lands on the platform first.
The email must therefore link to a platform (dash0) page, which then forwards to billing with the signed
`#bt=` token minted per viewer (`handlers/entitlements/handler.go:578`, `adminUpgradeURL`). The token must
never sit in a mailbox, so the email cannot carry it.

## Proposal

1. Add a platform URL for the upgrade entry point, built from the server's public base URL
   (find the helper that already builds `usageURL` for the same email in `phone_cap_notice.go`, reuse it):
   `<base>/d/orgs/<slug>/...` page that, once the user is logged in, reads `upgradeUrl` from
   `GET /orgs/:org/entitlements` (already signed for admins, `handler.go:193`) and redirects to it.
2. `Entitlements.UpgradeURL` (`usage_reserve.go:283`) returns that platform URL when the deployment has an
   upgrade template configured (SaaS), and `""` otherwise (self-hosted keeps no button). Update its doc comment.
3. Keep the dashboard banner (`web/dash0/src/components/shared/check-rate-limit-banner.tsx:93`) as is.
4. Document in `wiki/features/saas-mode.md:51` that the template is dashboard-only and emails use the platform page.

## Tests

- `server/internal/entitlements`: `UpgradeURL` with a billing template returns a URL on the platform host,
  not the billing host; with no template returns `""`.
- `server/internal/jobs/jobtypes` (usage cap email): rendered HTML and text contain the platform URL and
  do not contain the billing host or `#bt=`.
- dash0 Playwright (or Vitest) for the new redirect page: logged-in admin is sent to `upgradeUrl`;
  non-admin (no `upgradeUrl`) sees a clear message, no redirect.

## To verify

- Which dash0 route already exists for usage/plan (`showUsageLink` in `check-rate-limit-banner.tsx:77`);
  the redirect may fit on it instead of a new page.
- Other emails or notifications that interpolate the upgrade template (grep found only `usage-cap-reached`).

## Open questions

1. Where should the email button land: a new `/d/orgs/<org>/upgrade` redirect page (recommended: one
   entry point, token minted at click time, works for any future email), or the existing usage page with
   its own "Upgrade" button (one extra click, no new route)?

## Resolved open questions

1. **Add a new `/d/orgs/<org>/upgrade` redirect page.** The email button points to it; it mints the billing token at click time. One entry point that works for any future email, no extra click.
