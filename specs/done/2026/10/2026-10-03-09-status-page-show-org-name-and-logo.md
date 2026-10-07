---
model: sonnet
effort: medium
---

# Status page and its TV view should show the organization's name and logo

## Problem
`/s/tsa/tsa` and `/s/tsa/tsa/tv` (org `tsa`, page slug `tsa`) show only the page's own name and the page's own uploaded logo (`page.logoUrl`), falling back to the SolidPing mark.
The organization's identity is never shown, even though the org has a name and an optional logo (`Organization.LogoURL`, `server/internal/db/models/organization.go:23`, either an external http(s) URL or `/pub/assets/<file uid>`, NULL = no logo).
The public status page payload (`StatusPageResponse`, `server/internal/handlers/statuspages/service.go:~614`) carries no org name or org logo, so status0 cannot render them.

## Proposal
1. Backend: add `orgName` (string) and `orgLogoUrl` (optional, omitted when the org has no logo) to `StatusPageResponse` in `server/internal/handlers/statuspages/service.go`, filled on the PUBLIC payload from the org row already loaded by `GetOrganizationBySlug`. Admin payloads may carry them too (harmless, world-readable data). Document both in `server/internal/app/openapi/openapi.yaml` next to `logoUrl` (~line 13679).
2. Check that an org logo stored as `/pub/assets/<uid>` is served unauthenticated and works for custom-domain status pages; an external URL must pass through as is. Also check the status0 CSP `img-src 'self'` (AGENTS.md): an external http(s) org logo would be blocked on status0, so either only emit `/pub/assets/...` paths on this payload or proxy/skip external URLs (see To verify).
3. status0 brand bar, `web/status0/src/components/shared/status-page-view.tsx:~436-452`: logo precedence is page logo, then org logo, then the SolidPing mark. Show the org name next to the logo (keep `sp-logo` and `sp-page-name` custom-CSS hooks untouched). The page name stays the `<h1>` below.
4. status0 TV board, `web/status0/src/components/tv/tv-board.tsx:~399-446` (header with state icon, headline, `tv-page-name`): add the org logo (if any) and org name, sized for a wall display, without pushing the headline off a small screen. Add `data-testid`s `tv-org-name` and `tv-org-logo`.
5. Add the two fields to the status0 page type (`web/status0/src/lib/sp-page.ts`, to confirm) and to any mock/fixture used by status0 tests.
6. Changelog entry per `wiki/conventions/changelog.md`; mention in `web/docs/docs/features/status-pages.md`.

## Tests
- Go, `server/internal/handlers/statuspages/`: public payload includes `orgName`; includes `orgLogoUrl` when the org has a logo; omits it when `LogoURL` is NULL.
- Vitest in status0: brand bar renders org logo + name when set; falls back to the SolidPing mark when neither page nor org has a logo; page logo wins over org logo.
- Vitest/Playwright for TV: `tv-org-name` always shown, `tv-org-logo` absent when the org has no logo.
- E2E: extend an existing status0 spec (a status page appearance spec) to upload/set an org logo and assert it on both `/s/<org>/<slug>` and `/tv`; assert zero CSP violations (`status-page-appearance.spec.ts`).

## To verify
- Whether `/s/:org/:slug` and `/tv` use the same public endpoint and payload (`web/status0/src/routes/$org.$slug_.tv.tsx`, `web/status0/src/lib/sp-page.ts`).
- How external-URL org logos interact with status0's `img-src 'self'` CSP (`server/internal/securityheaders`).
- Whether `hideBranding`/white-label should hide the org logo (recommended: no, the org's own identity is not SolidPing branding).

## Resolved open questions

- Show the org name even when the page has its own logo and name: yes, small, next to the logo in the brand bar. The page name remains the `<h1>`.
- Page logo wins when both exist. The org logo is the fallback before the SolidPing mark.
