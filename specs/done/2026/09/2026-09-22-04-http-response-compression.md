---
model: sonnet
effort: medium
---

# Every response leaves the server uncompressed: a 3.2 MB status page that gzips to 200 KB

## Problem

Nothing in the stack compresses HTTP responses. The Go server has no gzip
handler (`grep -ri gzip server/internal` finds nothing outside tests), and the
reference Kubernetes overlay adds no compression middleware at the ingress.
Measured on the dev deployment, on a 200-resource public status page with a
7-day history and both availability and response time enabled:

| Response | Bytes on the wire | Same bytes, `gzip -6` |
|---|---|---|
| `GET /api/v1/status-pages/{org}/{slug}` | 3,168,037 | 201,906 |
| status0 bundle `/s/assets/index-*.js` | 1,036,007 | not measured, JS typically 3–4× |

`curl --compressed` returns exactly the same byte count as `curl` without it,
which is the proof: the server ignores `Accept-Encoding`. The browser confirms
it (`performance.getEntriesByType('resource')`: `encodedBodySize ===
decodedBodySize` on every asset and API call).

For the TV wallboard this is the difference between the page arriving in 7 s
on a fast link and 12 s on an office link: 4.2 MB of transfer that should be
about 500 KB. Specs `2026-09-22-05` to `2026-09-22-09` shrink the payload and
the server time; this one is independent of them and is worth shipping first,
because it applies to every JSON, HTML, CSS, JS, SVG and Atom body the server
emits, on every deployment, with no operator action.

## Decision: compress in the Go server, at the outermost handler

Compression lives in the binary, not in a reverse proxy. SolidPing is
self-hosted; the docs promise that the single binary behind any (or no) proxy is
a complete deployment. A proxy-side setting fixes one cluster; a wrapper in
[`Server.Handler()`](server/internal/app/server.go:3797) fixes every install.
A proxy that also compresses does no harm: proxies pass a response that already
carries `Content-Encoding` through untouched.

Library: `github.com/klauspost/compress/gzhttp`. It is already in `go.mod` as
an indirect dependency (`klauspost/compress v1.20.0`); promote it to direct. It
is the maintained, faster successor of the `nytimes/gziphandler` wrapper, and
it handles the three things a naive `gzip.NewWriter` wrapper gets wrong:

- **Content-type allowlist and minimum size.** Only bodies of a listed type
  and above `MinSize` are encoded; everything else passes through byte-for-byte.
- **`Content-Length` and `Vary`.** It drops `Content-Length` when it encodes,
  and appends `Accept-Encoding` to `Vary` (creating it when absent).
- **`http.Flusher` / `http.Hijacker` passthrough**, so streaming and upgraded
  connections keep working when the type is not on the allowlist.

## Design

### Placement

Wrap the root `http.Handler` returned by `Server.Handler()`. That is outside
the `httpx` router and its `httpx.Middleware` chain (`corsMiddleware`, Sentry,
logging, metrics, timeout, recoverer, rate limits, audit — see
[`server.go:599`](server/internal/app/server.go:599)), so:

- every route is covered, including the embedded SPA bundles served by
  [`embedded_assets.go`](server/internal/app/embedded_assets.go) (which set an
  explicit `Content-Length`; gzhttp removes it when it encodes), the docs site,
  `/openapi`, `/llms.txt`, `feed.xml` and the public API;
- the logging and metrics middlewares still observe the *uncompressed* body
  size, which is what they measure today — no metric changes meaning;
- the `httpx.Middleware` signature (`func(HandlerFunc) HandlerFunc`) needs no
  adapter.

### Options

```go
gzhttp.NewWrapper(
    gzhttp.MinSize(1024),
    gzhttp.CompressionLevel(gzip.BestSpeed), // klauspost level 1 ≈ stdlib 5 in ratio, far cheaper
    gzhttp.ContentTypes([]string{
        "application/json", "application/problem+json", "application/manifest+json",
        "text/html", "text/css", "text/plain", "text/markdown", "text/csv",
        "text/javascript", "application/javascript",
        "image/svg+xml",
        "application/xml", "text/xml", "application/atom+xml", "application/rss+xml",
        "application/yaml", "application/x-yaml", // the served OpenAPI document
    }),
)
```

Deliberately **not** on the list, so they pass through untouched:

- `text/event-stream` — the MCP endpoint's listening stream
  ([`mcp/handler.go:196`](server/internal/mcp/handler.go:196)); buffering it
  would break the protocol.
- images, fonts (`woff2` is already compressed), `application/octet-stream`,
  archives and every other binary type.
- anything on a `101 Switching Protocols` response: the realtime WebSocket
  endpoint (spec `2026-07-02-02`) upgrades through the wrapper's `Hijacker`
  passthrough and never reaches the encoder.

The level is `BestSpeed`. On this payload klauspost level 1 lands within a few
percent of level 6's ratio at roughly a quarter of the CPU, and a 3 MB body
encodes in single-digit milliseconds. The bundles are re-encoded on every
request; pre-compressing the embedded assets at build time is a possible
follow-up, not part of this spec.

### Kill switch

`server.compression` (bool, default `true`; env `SP_SERVER_COMPRESSION`).
There is no correctness reason to turn it off — the only reason is an operator
who wants the CPU spent at the proxy instead — but a wrapper that touches every
byte the server sends deserves a one-line way out. Document it with the other
`server.*` keys.

### `Vary` and the pinning tests

`statuspagecache` pins `Vary: X-Forwarded-Proto` on public status-page
responses and explains, at length, why nothing else belongs there. That
reasoning is about **body variation the handler controls**. `Accept-Encoding`
is a transport concern added by the wrapper below the handler; it is also the
one header CDNs and Varnish key on correctly, so it does not weaken the
"shared caches must be able to store this" goal.

- The handler-level tests (`TestPublicResponsesPinTheVaryHeader` in
  [`cache_control_test.go`](server/internal/handlers/statuspages/cache_control_test.go),
  `feed_cache_test.go`, `incidents_cache_test.go`, `statuspagecache_test.go`)
  build handlers directly and never see the wrapper. They stay as they are.
- App-level tests that go through `Server.Handler()` (`custom_domain_routing_test.go`,
  `status0_meta_test.go`, `openapi_spec_test.go`) send no `Accept-Encoding`, and
  gzhttp leaves such requests byte-identical to today, so they stay green too.
  Do not weaken an exact `Vary` assertion to "contains" anywhere; add a separate
  test for the encoded case instead (below).
- Update the package doc of [`statuspagecache`](server/internal/statuspagecache/statuspagecache.go)
  ("Exactly one request header changes such a body …") with one paragraph: the
  transport layer appends `Accept-Encoding`, the handler's own `Vary` stays as
  pinned.

## Plan

1. `go get github.com/klauspost/compress@v1.20.0` as a direct dependency.
2. Add `Server.Compression` to `internal/config` with the env mapping and the
   default, following the existing `server.*` keys.
3. In `Server.Handler()`, wrap the root handler when the flag is on. Keep the
   option list in one named function (`compressionWrapper()`), with the
   allowlist as a package-level slice so the test can assert against it.
4. Tests, in `internal/app`:
   - `GET /api/v1/status-pages/{org}/{slug}` with `Accept-Encoding: gzip` →
     `Content-Encoding: gzip`, no `Content-Length`, `Vary` contains both
     `X-Forwarded-Proto` and `Accept-Encoding`, and the decoded body equals the
     body of the same request without `Accept-Encoding`.
   - the same request without `Accept-Encoding` → no `Content-Encoding`,
     `Vary` exactly as pinned today.
   - a body under 1 KiB (`/api/mgmt/health`) with `Accept-Encoding: gzip` →
     not encoded.
   - the embedded status0 `index-*.js` with `Accept-Encoding: gzip` → encoded.
   - the MCP endpoint with `Accept: text/event-stream` → not encoded, first
     event flushed (reuse whatever the MCP tests already do to read one event).
   - the realtime WebSocket route still upgrades through the wrapped handler.
   - `server.compression=false` → nothing is ever encoded.
5. Docs: the `server.compression` row in `web/docs/docs/configuration/`; one
   sentence in the installation pages that mention a reverse proxy
   (`kubernetes.md`, `windows.md`, `configuration/index.md`): SolidPing
   compresses its own responses, so no proxy-side compression is needed and
   enabling it anyway is harmless.
6. Changelog entry per `wiki/conventions/changelog.md`.

## Acceptance

- `curl -s --compressed -o /dev/null -w '%{size_download}'` on the page endpoint
  of a large page reports under 10 % of the uncompressed size.
- `make test` and `make test-postgres` green without any exact-`Vary`
  assertion having been loosened.
- The MCP and realtime e2e coverage that exists today still passes.

## Out of scope

- Pre-compressed embedded assets (`.js.gz` / `.br` at build time).
- Brotli. gzip is what every client sends; brotli's ratio gain on JSON is not
  worth a second encoder path today.
- Any change to the reference Kubernetes overlays. They need none.
