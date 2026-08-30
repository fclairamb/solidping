---
model: sonnet
effort: medium
---

# CORS: `*` + credentials is spec-invalid, and it makes `/ingest` unusable from any other origin

## Problem

Found from the business side on 2026-08-30, while checking whether
`www.solidping.io` could route its PostHog traffic through the app's
first-party proxy instead of the verbatim `eu.i.posthog.com` (which ad
blockers drop). It cannot, for three separate reasons. All three are in
`server/internal/app/server.go`.

### 1. `Access-Control-Allow-Origin: *` is sent together with `Access-Control-Allow-Credentials: true`

`corsMiddleware` (`server/internal/app/server.go:2191-2197`) hardcodes both:

```go
writer.Header().Set("Access-Control-Allow-Origin", "*")
...
writer.Header().Set("Access-Control-Allow-Credentials", "true")
```

The Fetch standard forbids this pairing: when a request's credentials mode is
`include`, a wildcard `Access-Control-Allow-Origin` fails the CORS check. So
the header pair is self-cancelling — the `credentials: true` never takes
effect for anyone, and any cross-origin caller that sends cookies or an
`Authorization` header is refused by the browser.

Observed on production, every endpoint, with `Origin: https://example.com`:

```
$ curl -sS https://solidping.io/api/v1/config -H 'Origin: https://example.com' -D - -o /dev/null
HTTP/2 200
access-control-allow-credentials: true
access-control-allow-origin: *
```

**Nothing is broken by this today** — the dashboard is same-origin, so it
never takes the CORS path. It is a latent defect that surfaces the moment a
second origin needs the API: `www`, an embeddable status-page widget on a
customer's own domain, or a separately-hosted front end.

There is also no allowlist and no config knob: the wildcard is a constant.

### 2. The PostHog proxy passes upstream CORS headers through, so they arrive duplicated

`proxyPostHog` (`server/internal/app/server.go:2635`) reverse-proxies to
PostHog Cloud. PostHog emits its *own* CORS headers, and the proxy forwards
them on top of the ones `corsMiddleware` already set. The client sees each
header twice:

```
$ curl -sS -X POST 'https://solidping.io/ingest/decide/?v=3' \
    -H 'Origin: https://www.solidping.io' -H 'Content-Type: application/json' \
    -d '{"api_key":"phc_…","distinct_id":"probe"}' -D - -o /dev/null
HTTP/2 200
access-control-allow-credentials: true
access-control-allow-credentials: true
access-control-allow-origin: *
access-control-allow-origin: https://www.solidping.io
```

A repeated `Access-Control-Allow-Origin` fails the CORS check outright —
browsers reject the response with *"contains multiple values"*. `curl` does
not care, which is why this passes every non-browser probe.

Upstream, unproxied, is well-formed (single echoed origin, no wildcard):

```
$ curl -sS -X POST 'https://eu.i.posthog.com/decide/?v=3' -H 'Origin: https://www.solidping.io' …
access-control-allow-origin: https://www.solidping.io
access-control-allow-credentials: true
```

So the duplication is ours, introduced by proxying.

### 3. The proxy has no `OPTIONS` route, so preflight gets `405` with no CORS headers at all

Only two methods are registered
(`server/internal/app/server.go:2146-2147`):

```go
mainGroup.GET(config.PostHogProxyPath+"/*path", s.proxyPostHog)
mainGroup.POST(config.PostHogProxyPath+"/*path", s.proxyPostHog)
```

`corsMiddleware` does handle `OPTIONS`, but the router rejects the method
before the handler is reached:

```
$ curl -sS -i -X OPTIONS 'https://solidping.io/ingest/decide/?v=3' \
    -H 'Origin: https://www.solidping.io' -H 'Access-Control-Request-Method: POST'
HTTP/2 405
allow: GET
allow: POST
content-length: 0
```

No `Access-Control-*` headers on a `405`, so any non-simple request (a JSON
`POST`, or one carrying a custom header) fails before it is ever sent.
`posthog-js` keeps capture calls inside the "simple request" envelope, which
is why this is latent rather than loud — but remote-config and any future
JSON call do preflight.

## Why this matters beyond correctness

The proxy exists precisely so ad blockers stop dropping analytics
(`server.go:2632-2634` says so). It works for the dashboard, which is
same-origin. It does **not** work for `www.solidping.io`, which is the site
whose audience — self-hosters, DevOps, SRE — blocks trackers at the highest
rate of any population we have. `www` therefore ships
`api_host: https://eu.i.posthog.com` verbatim today
(`solidping-website/src/clientModules/analytics.ts`), and its launch-day
numbers will be undercounted by whatever fraction of that audience runs a
blocker.

Fixing 1–3 makes `https://solidping.io/ingest` usable from `www`, which is a
one-line change on the website side.

## Proposal

1. **Replace the wildcard with an origin allowlist.** Echo the request
   `Origin` when it matches, and only then send
   `Access-Control-Allow-Credentials: true`; add `Vary: Origin`. Drive the
   list from config so a self-hoster can add their own front-end host, with
   the app's own public URL as the default. If a genuinely public,
   credential-free surface is wanted, that is `*` **without**
   `Allow-Credentials`, not both.
2. **Strip upstream CORS headers in `proxyPostHog`'s `ModifyResponse`**
   (`Access-Control-Allow-Origin`, `-Allow-Credentials`, and any other
   `Access-Control-*`) so exactly one layer owns them. Either layer can own
   them; two cannot.
3. **Register `OPTIONS` on the proxy path** alongside GET and POST, so
   preflight reaches `corsMiddleware`.
4. **Test at the header level, and count.** The bug is *duplication*, so an
   assertion that reads one value passes while the response is broken —
   assert `len(resp.Header.Values("Access-Control-Allow-Origin")) == 1`.
   Add a case for `OPTIONS /ingest/...` returning 200 with the headers
   present. `server/internal/app/posthog_proxy_test.go` is the place.

## Out of scope

Whether `www` should actually move to the first-party path is a marketing
decision tracked in `solidping-business` — this spec only removes the
blocker.
