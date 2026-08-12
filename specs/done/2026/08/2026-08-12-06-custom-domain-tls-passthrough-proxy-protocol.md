---
model: opus
effort: high
---

# Custom domains behind a Traefik SNI passthrough lose the client IP: tlsedge needs PROXY protocol

## Problem

The custom-domain feature is fully built at the application layer but has no
deployment path on k8xp, and the chosen deployment topology exposes one gap in
this repo.

**What already works.** Host-based routing rewrites any verified custom domain
to its status page (`server/internal/app/custom_domain_routing.go`), and
`server/internal/tlsedge/` terminates TLS in-server: certmagic on-demand
issuance, fail-closed per-host gating (`ErrHostNotAllowed`,
`internal/tlsedge/edge.go:56` — the only thing between an SNI scan and Let's
Encrypt's 5/hour failed-validation limit), DB-backed certificate storage so any
replica can serve or renew any domain, and cert status surfaced to the
dashboard. All of it is off by default behind `acme.enabled`.

**Why it isn't deployed.** On k8xp, Traefik (a DaemonSet on the master/eu2/eu3
edges, with klipper-lb binding host ports 80/443) owns the only public `:443`,
so tlsedge has nowhere to listen. `cname.solidping.io` currently resolves
through the zone-wide `*` CNAME to the master edge's Traefik, which has neither
a route nor a certificate for customer domains.

**Chosen architecture (decided 2026-08-12).** A Traefik catch-all TCP router —
``HostSNI(`*`)`` with `tls.passthrough: true` on the `websecure` entrypoint —
pipes every TLS connection whose SNI matches no explicit router to tlsedge.
Since Traefik v2.6 the catch-all carries a hardwired priority of `-1`, so every
existing HTTPS Ingress in the cluster keeps terminating at Traefik unchanged;
only unclaimed SNI falls through. TLS-ALPN-01 validation flows through the same
passthrough (the challenge *is* a TLS handshake with the customer domain as
SNI), so certificate issuance needs zero per-domain configuration, and
three-edge HA comes free because cert storage already lives in the DB with
certmagic's storage locking. `portFromListenAddr`
(`internal/tlsedge/edge.go:439`) already feeds remapped container ports to
certmagic for exactly this "something forwards :443 to us" case.

**The gap.** A TLS passthrough is opaque: Traefik never sees HTTP, so there is
no `X-Forwarded-For`, and `Request.RemoteAddr` for *all* custom-domain traffic
becomes the Traefik pod's IP. The rate limiter is keyed on client IP — the k8xp
edge runs `externalTrafficPolicy: Local` precisely to preserve it (see the
comment in `k8xp/k8s/traefik/helmchartconfig.yaml`) — so behind the
passthrough, per-IP limiting and abuse logging silently collapse to "every
request comes from one IP". Traefik can send PROXY protocol v2 on TCP-router
services (`proxyProtocol: {version: 2}`), but tlsedge's listeners don't speak
it.

## Proposal

Add config-gated PROXY protocol (v1/v2) support to the tlsedge listeners, using
`github.com/pires/go-proxyproto` (the library Traefik itself uses).

### Listener wrapping

Wrap **both** tlsedge listeners — `acme.listen_http` and `acme.listen_https` —
in a `proxyproto.Listener` when enabled. For the TLS listener the PROXY header
is parsed off the wire *before* the TLS handshake, so certmagic/`crypto/tls`
sit unchanged on top of the wrapped listener. `net/http` derives
`Request.RemoteAddr` from `conn.RemoteAddr()`, which go-proxyproto rewrites, so
the rate limiter, request logging, and handlers pick up the real client IP with
no middleware changes — verify this end to end with a regression test rather
than assuming it.

### Configuration

Two new keys in `ACMEConfig` (`server/internal/config/config.go:367`):

- `acme.proxy_protocol` (bool, default `false`) — master switch.
- `acme.proxy_protocol_trusted_cidrs` ([]string) — CIDRs whose PROXY headers
  are honored (for k8xp: the cluster pod/node CIDRs the Traefik pods dial
  from).

Both keys contain underscores, so they must be read manually in `applyACMEEnv`
(`config.go:1684`) as `SP_ACME_PROXY_PROTOCOL` /
`SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS` (comma-separated), per the established
koanf quirk.

### Trust policy — the security-relevant part

The PROXY header is an unauthenticated preamble; whoever can open a TCP
connection can send one. The policy must be:

- **Source IP in a trusted CIDR → `USE`**: honor the header when present,
  still accept bare connections (kubelet probes and in-cluster health checks
  don't send PROXY, so `REQUIRE` is wrong).
- **Source IP not trusted → `IGNORE`**: the connection proceeds but the header
  never overrides `RemoteAddr`. This is the spoofing guard — an attacker
  connecting directly must not be able to forge an arbitrary client IP into
  the rate limiter.
- `acme.proxy_protocol: true` with **empty** `trusted_cidrs` fails fast at
  startup (extend `validateACMEConfig`, `config.go:2178`) — a trust-everyone
  default would be an IP-spoofing hole, and fail-closed matches how the rest
  of tlsedge is built.
- Bound the PROXY preamble read with go-proxyproto's header read timeout so
  the slow-header protection the listeners already have (`readHeaderTimeout`,
  `edge.go:43`) isn't bypassed at the layer below.

### Tests

Table-driven with `testify/require` and `t.Parallel()`:

- Trusted source + PROXY v2 header → handler observes the advertised
  client IP:port (through the **TLS** listener, not just the HTTP one — extend
  the `pebble_test.go` harness or a unit-level TLS listener test).
- Trusted source, no header → connection works, real peer address observed.
- Untrusted source + header → header ignored, real peer address observed.
- Malformed header from a trusted source → that connection fails, the listener
  keeps serving subsequent connections.
- `proxy_protocol` enabled + empty CIDR list → startup validation error.

### Docs

Document both keys alongside the existing `acme.*` reference in the docs site,
including the trust-policy semantics and the "empty CIDRs fails startup" rule.

## Deployment plan (k8xp — separate repo, not part of this spec's implementation)

Recorded here so the spec captures the full picture; executed manually against
`~/code/fclairamb/k8xp` after this ships:

1. Solidping deployment env: `SP_ACME_ENABLED=true`, `SP_ACME_EMAIL`,
   `SP_ACME_LISTEN_HTTP=:8080`, `SP_ACME_LISTEN_HTTPS=:8443`,
   `SP_ACME_PROXY_PROTOCOL=true`,
   `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS=<cluster CIDRs>`; a Service exposing
   8443/8080.
2. `IngressRouteTCP` on `websecure`: ``match: HostSNI(`*`)``,
   `tls.passthrough: true`, service port 8443 with
   `proxyProtocol: {version: 2}`.
3. Optional lowest-priority catch-all HTTP `IngressRoute` (``PathPrefix(`/`)``,
   `priority: 1`) → port 8080, so plain-HTTP hits on custom domains get
   tlsedge's 308-to-HTTPS instead of Traefik's 404 (no PROXY header on this
   path — Traefik proxies it as HTTP; the `USE` policy accepts bare
   connections).
4. DNS: explicit A/AAAA records for `cname.solidping.io` to the web edges
   (stop relying on the zone-wide `*` CNAME).
5. Verify on `solidping-dev` with `SP_ACME_CA_URL` pointed at Let's Encrypt
   staging: issuance works through the passthrough **and** the cluster's
   existing Ingresses (Mattermost, Metabase, OpenObserve…) still terminate at
   Traefik, before touching prod.

Known trade-offs accepted with this architecture: unknown-SNI scanner traffic
cluster-wide now reaches tlsedge (fail-closed, negative-cached, but log
noise), a deleted/typo'd Ingress host gets a refused handshake from tlsedge
instead of Traefik's default-cert 404, and passthrough traffic doesn't appear
in Traefik's access logs — solidping's own logs are the observability surface.
