---
model: opus
effort: medium
---

# No CSP or frame-ancestors anywhere; raw user CSS on public status pages has exfil channels

## Problem

- `rg "Content-Security-Policy"` over the repo returns zero: no CSP, no
  `frame-ancestors`, no `X-Frame-Options` on any served page — status pages,
  dash0 and status0 are all frameable and run with no header backstop.
- The status-page custom CSS (operator-authored stylesheet injected as a
  React text child into a `<style>` on the *public* page —
  `web/status0/src/components/shared/status-page-view.tsx:417-422`) is raw:
  React escaping prevents tag breakout, but the CSS itself allows external
  `url()` loads, CSS attribute/value selectors can exfiltrate DOM values via
  `url()` beacons, and overlays can clickjack. The threat model is "org
  admin poisons their org's public visitors" — bounded, but CSP would
  remove the exfil channels almost for free. Same applies to `sp-page`
  custom-domain pages, where the document origin IS the application origin.

## Proposal

1. New response-header middleware applied to HTML-producing surfaces
   (status0, `sp-page` custom domains, dash0, docs):
   - `X-Frame-Options: DENY` (legacy agents) **and**
     `Content-Security-Policy: frame-ancestors 'self'` baseline.
   - Per-org embedding allowlist: a system/org parameter
     `statuspage.allowed_embed_origins` (comma-separated scheme+host list)
     turns the status-page routes' policy into
     `frame-ancestors 'self' https://acme.example;` — orgs that embed their
     status page in their intranet get an explicit knob instead of disabling
     the header globally.
2. Status pages get a real CSP with the custom-CSS feature kept intact:
   `default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'
   data:; font-src 'self' data:; connect-src 'self'; frame-ancestors …`.
   `'unsafe-inline'` on style-src keeps the custom stylesheet working, while
   `img-src`/`font-src`/`connect-src` being self-only kills the `url()`
   exfil channels and external beacons (CSS-loaded external images/fonts are
   governed by img-src/font-src even when the style rule is inline).
3. dash0 and docs keep functioning: dash0 loads fonts/analytics — inventory
   every external origin the shipped frontend actually uses (PostHog, error
   reporting, fonts, maps) and include them in the shipped policy; keep the
   policy in one Go-side constant per surface so it is testable, and document
   how an operator relaxes it (config parameter, e.g.
   `headers.csp_extra_sources`) rather than editing headers in nginx.
4. The status0 SPA strips the kiosk token via `history.replaceState` already
   (`statuspagekiosk.go:51-55`); add `Referrer-Policy: no-referrer` on
   status0/custom-domain documents while in there.
5. Docs page (security headers) + changelog. Rollout note: any CSP on dash0
   can break extensions users install server-side? No — server-side only;
   the risk is inline scripts in the embedded docs search — verify and
   adjust the shipped constant accordingly.

## Tests

- Middleware test: status0 page → CSP present with self-only img/font and
  the org's embed origins rendered into frame-ancestors; dash0 and docs get
  the baseline; API JSON routes unchanged.
- Embed allowlist: org with `statuspage.allowed_embed_origins` set sees its
  origins in frame-ancestors; other orgs do not; default has
  `frame-ancestors 'self'`.
- A test page with custom CSS containing `background:url(https://evil.example/…)`:
  the CSP delivered with the page forbids the load (assert header value;
  browser-level enforcement covered by a Playwright assertion that the CSP
  header is present on the status page).
- No existing route loses functionality: run the dash0 Playwright suite and
  the status0 suite with the middleware enabled (it ships default-on).