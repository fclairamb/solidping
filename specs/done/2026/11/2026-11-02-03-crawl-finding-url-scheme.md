---
model: opus
effort: low
pr: batch
automerge: false
---

# Crawl check: sitemap finding URLs are stored and linked with any scheme

## Problem

Found by the security scan of 2026-11-02 (finding AC4). Severity: low. Any member who can create or edit a `crawl` check can trigger it, and so can the crawled site.

- Links found on crawled pages go through `resolveRef`, which drops non-http(s) schemes (`server/internal/checkers/checkcrawl/parse.go:185`).
- Sitemap URLs skip that filter: the `Sitemap:` lines of `robots.txt` (`robots.go:42`), a sitemap index's `<loc>` children (`crawl.go:276`), and the URL from robots (`crawl.go:211`).
- A failed fetch of such a URL is recorded verbatim as a `sitemap_error` finding (`crawl.go:256-258`).
- dash0 renders a finding's URL as `<a href={finding.url} target="_blank">` with no scheme check (`web/dash0/src/components/checks/crawl-card.tsx:126`).
- So a site the attacker controls can plant a `javascript:` (or `data:`) link that every member sees on the check page.

The dashboard CSP (`script-src 'self'`, no `'unsafe-inline'`/`'unsafe-hashes'`, `server/internal/securityheaders/securityheaders.go:127-139`) blocks `javascript:` navigation in current browsers, hence low. It is still a stored script link in the dashboard, one CSP regression away from XSS. The `make dev` proxy has no CSP at all.

## Proposal

1. In `checkcrawl`, accept a sitemap URL only if it parses as absolute http(s), before queueing (`crawl.go:211`, `:276`) and before it can become a finding. Drop anything else silently, like `resolveRef` does for page links.
2. In `crawl-card.tsx`, render `href` only when `finding.url` matches `^https?://`, otherwise plain text, the same rule as `vendor-status-card.tsx:38`.

## Tests

- `server/internal/checkers/checkcrawl`: a `robots.txt` with `Sitemap: javascript:…`, and a sitemap index with a `<loc>` of `data:…`, produce no finding and no fetch. Positive control: an `https://` sitemap from robots is fetched, and its 404 is reported as a `sitemap_error` finding.
- dash0 Playwright, or a component test if one exists for the crawl card: a finding whose URL is `javascript:…` renders without an `href`. An `https://` finding renders a link.

## To verify

- Whether the check's own `sitemap` config value (`crawl.go:124`, `:219`) is already validated as http(s) on save. If not, apply the same rule there.
