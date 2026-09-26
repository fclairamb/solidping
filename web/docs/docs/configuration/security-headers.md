---
sidebar_position: 5.2
title: Security Headers
---

# Security headers

SolidPing sends a `Content-Security-Policy`, an `X-Frame-Options` and, on
status pages, a `Referrer-Policy` with every page it serves. They are on by
default and need no reverse-proxy configuration. JSON API responses carry
none of them.

## What each page gets

| Surface | Policy |
|---------|--------|
| Public status pages (`/s/…` and custom domains) | Full policy, first-party fetches only (see below) |
| Dashboard (`/d/…`) | Full policy, tuned to what the dashboard loads |
| Docs (`/docs`), API explorer (`/openapi`), acknowledge / unsubscribe / subscription pages, not-found page | Framing protection only |

### Status pages

```text
default-src 'self'; script-src 'self' 'sha256-…'; style-src 'self' 'unsafe-inline';
img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none';
base-uri 'self'; form-action 'self'; frame-ancestors 'self'
Referrer-Policy: no-referrer
X-Frame-Options: SAMEORIGIN
```

The page's [custom CSS](/features/status-pages#custom-css) still applies
(`style-src 'unsafe-inline'`). What changes is where that CSS can fetch from:
images and fonts load only from the status page's own origin or from `data:`
URIs. An organization admin can therefore not use the stylesheet to beacon
visitors to a third-party server, for example with an attribute selector that
loads a different `url()` for each character typed in the subscribe form.

In practice:

- An **uploaded logo and favicon** keep working. They are served from the
  same origin (`/pub/status-page-assets/…`). This is the supported way to brand
  a page.
- A small image or a font can be inlined as a `data:` URI.
- `url("https://cdn.acme.com/logo.svg")` in custom CSS, and external images in
  incident-update Markdown, **no longer load**. The browser blocks them and
  logs a CSP violation in its console. If you self-host and need them, see
  [Widening the policy](#widening-the-policy).

`Referrer-Policy: no-referrer` stops a status page URL, which can carry a TV
mode kiosk token, from being sent to any site a visitor clicks through to.

### Dashboard

The dashboard allows its own origin for scripts, styles, fonts and requests,
plus:

- `img-src data: blob: https: http:`: organization logos can be external URLs,
  and sign-in providers return avatars on their own domains.
- the request's own `https://` / `wss://` origin, for the live update socket.
- the PostHog host, when [product analytics](/configuration/analytics) are
  enabled with an explicit `SP_POSTHOG_HOST`. For a PostHog Cloud host
  (`https://eu.i.posthog.com`, `https://us.i.posthog.com`) the regional
  assets host (`https://eu-assets.i.posthog.com`, …) is allowed too, since
  posthog-js loads its recorder and surveys from there. With the default
  first-party `/ingest` proxy, analytics need nothing extra.

### Framing

Every page is `frame-ancestors 'self'`: only SolidPing itself may put it in an
iframe (the appearance editor previews the status page this way).
`X-Frame-Options: SAMEORIGIN` says the same thing to older browsers. It is
`SAMEORIGIN` and not `DENY` because same-origin framing is allowed.

## Embedding a status page

To show a status page in an iframe on another site (an intranet, a wall
display), list that site under **Organization → Settings → Status page
embedding**, one origin per line:

```text
https://intranet.acme.com
https://*.acme.com
```

Or through the API:

```bash
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"statusPageAllowedEmbedOrigins":["https://intranet.acme.com"]}' \
  https://solidping.acme.com/api/v1/orgs/acme/settings
```

The origins are added to `frame-ancestors` on every status page of that
organization, on `/s/…` and on its custom domains. Other organizations are not
affected. The change takes effect within a minute.

- Each entry is a scheme and a host, optionally with a port or a leading `*.`
  label (`https://*.acme.com`). Paths, queries, a bare `*`, CSP keywords,
  spaces and `;` are refused.
- At most 20 entries. An empty list goes back to `'self'` only.
- While the list is not empty, `X-Frame-Options` is not sent. It cannot express
  an allowlist, and `SAMEORIGIN` would contradict the CSP in browsers that
  read both.

The value is stored as the organization parameter
`statuspage.allowed_embed_origins` (comma-separated).

The [embeddable widget](/features/status-pages#embeddable-live-widget) is a script,
not an iframe, and does not need this.

## Widening the policy

An operator can add sources to the shipped policies without touching a reverse
proxy:

| Variable | System parameter | Default |
|----------|------------------|---------|
| `SP_HEADERS_CSP_EXTRA_SOURCES` | `headers.csp_extra_sources` | - |

The value is `;`-separated groups, each a directive followed by one or more
sources:

```bash
SP_HEADERS_CSP_EXTRA_SOURCES="img-src https://cdn.acme.com; font-src https://fonts.acme.com"
```

- A source is added to that directive on every page whose policy sets it. The
  docs and the API explorer set no `img-src`, so the example above does not
  touch them (adding one would restrict them, not widen them).
- Directives you can widen: `default-src`, `script-src`, `style-src`,
  `img-src`, `font-src`, `connect-src`, `worker-src`, `media-src`,
  `frame-src`, `manifest-src` and `frame-ancestors`.
- `frame-ancestors https://intranet.acme.com` allows that site to frame every
  page, dashboard included, and drops `X-Frame-Options` everywhere. Prefer the
  per-organization list above for status pages.
- An invalid group is logged at startup and skipped. The rest still applies.
- Read at startup: restart after changing it.

Widening `img-src` on status pages gives back the channel this policy closes,
to the hosts you list. Only list hosts you control.

## Development

`make dev` proxies `/d` and `/s` to the Vite dev servers. Proxied responses
carry no policy, because Vite's dev page needs inline scripts and a hot-reload
socket on another port. The policy applies to the embedded build (`make
build`, and every release).
