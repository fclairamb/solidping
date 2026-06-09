# Status Pages as an SEO / LLM-Indexability Asset

## Problem

We want SolidPing's public status pages to generate organic discovery for the
service — both classic search (Google) and LLM indexing (ChatGPT, Claude,
Perplexity crawlers). Today they generate **zero**:

- `web/status0` is a pure client-side React SPA, embedded in the Go binary and
  served from a static `index.html` (`server/internal/app/server.go:1394`).
  The HTML crawlers receive is a generic shell: static
  `<title>SolidPing - Status Page</title>`, no description, no OpenGraph, no
  JSON-LD, no `robots.txt`, no `sitemap.xml`.
- Googlebot renders JS (eventually, on a render budget), but **most LLM
  crawlers (GPTBot, ClaudeBot, PerplexityBot) do not execute JavaScript** — to
  them every status page is an empty page.
- Status pages live only under our own domain (path-based `:org/:slug`
  addressing, no custom-domain support in the `StatusPage` model), so they
  can't generate backlinks either.
- The only crawlable surface today is the Atom feed
  (`GET /api/v1/status-pages/:org/:slug/feed.xml`) — real content
  (status-update markdown), publicly served, 5-min cache. That's a good seed,
  but feeds alone don't rank.

## Honest assessment (read this before the proposal)

**The uptime data itself will not rank.** "All systems operational" plus a
green bar is thin, volatile, near-duplicate content. Google won't send traffic
to it and may even devalue a domain hosting hundreds of such pages. Anyone
expecting status *pages* to be an organic-traffic engine will be disappointed.

What actually works in this space, in order of real-world impact:

1. **The "Powered by SolidPing" backlink flywheel.** This is the actual SEO
   engine for statuspage.io, Instatus, BetterStack and friends: customer
   status pages on *customer domains* (status.acme.com) each carry a footer
   link back to us. Hundreds of independent-domain backlinks is the single
   most valuable SEO asset a status-page product can produce. **It requires
   custom domains, which we don't have yet.** Without them, every status page
   is a page on our own domain linking to itself — SEO value ~zero.
2. **Incident posts / postmortems are the only content that ranks and gets
   LLM-cited.** A written incident report is unique, dated, factual text —
   exactly what both Google and LLM crawlers want. We already store this
   (`status_updates.body_markdown`); we just never serve it as HTML, and
   updates have no permalinks.
3. **Brand/navigational queries** ("acme status", "is acme down") are winnable
   and cheap — they just need the page title/description to be server-rendered.
4. **LLM indexability is mostly "serve real HTML".** No tricks: meaningful
   markup without JS execution, plus structured data and an `llms.txt`. The
   crawlers are dumb fetchers; meet them with static content.

And one warning: **don't index everything.** Free-tier throwaway pages with
one check and no description are thin-content liabilities. Indexing should be
opt-in or quality-gated, with `noindex` as the default for empty pages.

## Proposal

Phased, cheapest-win first. Phases 1–2 are small Go-server changes; Phase 3
is the strategic one; Phase 4 is the content play.

### Phase 1 — Server-injected `<head>` + crawl hygiene (small, do first)

No SSR framework. The Go server already serves `index.html` for status0
routes; make it inject per-page tags before responding (the data is one
existing service call away, and `index.html` is already served with a 60s
cache):

- Dynamic `<title>` ("Acme Status — All systems operational"), meta
  description (page description + current status), canonical URL.
- OpenGraph/Twitter tags — makes every status link unfurl properly in Slack,
  Teams, X, iMessage. This is arguably more valuable day-to-day than the SEO
  itself.
- JSON-LD (`WebPage` + per-resource status; `NewsArticle`-ish for incident
  updates later).
- `robots.txt` and a `sitemap.xml` listing public, enabled,
  indexing-opted-in status pages.
- `noindex` default for pages that don't meet a minimal quality bar
  (description set, ≥1 resource); explicit per-page "allow indexing" toggle.

### Phase 2 — Static HTML body for non-JS crawlers (medium)

Extend the same injection to render the actual page content server-side as
semantic HTML inside the SPA shell (sections, resource names, current status,
overall availability %, recent status updates as rendered markdown). React
replaces it on hydration; crawlers without JS get the real page. This is
"poor-man's SSR" — one Go template, no Node on the server, fits the
single-binary philosophy. Add `llms.txt` at the root describing the public
surfaces.

### Phase 3 — Custom domains + "Powered by SolidPing" (the strategic one)

- `custom_domain` on `StatusPage`, Host-header → (org, slug) resolution,
  ACME/Let's Encrypt cert provisioning (or documented CNAME-behind-Caddy for
  self-hosters).
- Footer "Powered by SolidPing" link, on by default for free tier (removable
  on paid — that's also a monetization lever every competitor uses).
- This is a sizable feature justified on its own merits (it's also the #1
  status-page table-stakes feature we lack); the backlink flywheel is the SEO
  payoff.

### Phase 4 — Content surfaces that can actually rank

- **Permalink per status update / incident** (`/incidents/<uid>-<slug>`),
  server-rendered (Phase 2 machinery), listed in the sitemap, linked from the
  Atom feed entries instead of the bare page URL.
- **Incident history page** (`/history`) — dated archive, internal links.
- Optional later: public **uptime report** pages (monthly SLA summaries) —
  only if customers ask; on their own they're thin.

### Explicitly out of scope

- Full JS SSR (Next/TanStack Start) for status0 — heavy, breaks the
  embedded-single-binary model, and Phase 2 captures ~all the crawler value.
- Programmatic-SEO landing pages ("is X down" for third-party services) —
  different product, reputational risk, and Google is actively demoting
  that pattern.

## Acceptance Criteria

- [ ] Fetching a public status page with `curl` (no JS) returns its real
      title, meta description, OG tags, JSON-LD, and human-readable status
      content
- [ ] `robots.txt` and `sitemap.xml` served; sitemap lists only public,
      enabled, indexing-opted-in pages; everything else `noindex`
- [ ] Status links unfurl with correct title/description in Slack
- [ ] Status updates have stable, server-rendered permalinks referenced from
      the Atom feed
- [ ] (Phase 3) A status page is reachable on a customer domain with a valid
      cert and carries a Powered-by backlink
- [ ] `llms.txt` describes the public status surfaces

## References

- Existing Atom feed: `server/internal/handlers/statussubscribers/handler.go:278`
- SPA serving / embed: `server/internal/app/server.go:1394`
- Public page data (already everything Phase 2 needs):
  `server/internal/handlers/statuspages/service.go:783`
- Status update content: `server/internal/db/models/status_update.go`
  (`body_markdown`), migration `029_status_updates.up.sql`
