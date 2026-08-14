# Runbook — custom-domain TLS (in-server ACME)

Operator runbook for spec `2026-07-26-01` (CNAME-only verification + in-server
ACME). Covers the k8xp deployment prerequisite and the live acceptance
checklist for `status.webingenia.com`.

Everything in the repo (code, migrations, tests) is done; the items below are
**infrastructure and DNS actions a human has to take**, plus the manual
acceptance run that closes the spec.

---

## 1. What changed in the product

- A customer now creates **one** DNS record — a `CNAME` — to activate a custom
  domain. The `_solidping-challenge` TXT record is gone.
- `server.custom_domain_cname_mode` picks what that CNAME must point at:
  - `shared` (default) — the plain instance target (`solidping.k8xp.com`).
  - `token` — the page-specific `<token>.cname.<target>` host.
- `acme.enabled` turns on in-server TLS: the process listens on `:80` and
  `:443`, obtains Let's Encrypt certificates on demand for its own hosts and for
  verified custom domains, and stores them in the `tls_storage` table.
- The external-proxy contract (`GET /api/v1/public/custom-domains/allowed`) is
  **unchanged**. In-server ACME and an external TLS proxy are alternatives, not
  a migration: leave `acme.enabled` false and nothing about the deployment
  changes.

## 2. Deployment prerequisite (k8xp) — REQUIRED before the live test

The dev/SaaS deployment at `solidping.k8xp.com` runs behind an ingress that
terminates TLS for **known hostnames only**. Custom domains are by definition
not known ahead of time, so today a request to `https://status.webingenia.com/`
never reaches the pod with a usable TLS handshake.

Pick **one** of the following. This is an explicit ops decision — it is out of
repo scope and must be made and applied by a human.

### Option A — SNI passthrough at the ingress (preferred for in-server ACME)

Configure the ingress controller to pass TLS through, unterminated, for any
hostname it does not itself own, routing it to the SolidPing service on `:443`,
and to forward plain `:80` for HTTP-01.

- Pros: one moving part; certificates live in the database and are shared by
  every replica; nothing else to operate.
- Cons: ingress-controller-specific configuration; the controller can no longer
  inspect or route on HTTP for those hosts.
- Requires: `SP_ACME_ENABLED=true`, `SP_ACME_EMAIL=<ops address>` on the main
  deployment, and the pod able to bind `:80`/`:443` (either run with
  `NET_BIND_SERVICE`, or set `SP_ACME_LISTEN_HTTP` / `SP_ACME_LISTEN_HTTPS` to
  high ports and target those in the Service).
- **Client IP**: a passthrough is opaque, so the pod sees the proxy's address on
  every custom-domain request and per-IP rate limiting collapses to one bucket.
  Have the proxy send PROXY protocol v2 (Traefik: `proxyProtocol: {version: 2}`
  on the TCP service) and set `SP_ACME_PROXY_PROTOCOL=true` plus
  `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS=<pod/node CIDRs the proxy dials from>`.
  Headers from untrusted sources are ignored (no spoofing), bare connections
  from trusted ones are still accepted (health probes), and an empty CIDR list
  fails startup.

### Option B — dedicated LoadBalancer / NodePort

Give SolidPing its own L4 LoadBalancer (or NodePort behind one) and point the
`solidping.k8xp.com` A/AAAA records — the CNAME target customers chain to — at
that address instead of at the shared ingress.

- Pros: no ingress-controller feature dependency; the shared ingress is
  untouched.
- Cons: an extra load balancer to pay for and monitor; the instance's own
  hostname must also be served from it (or split-DNS'd).

### Option D — chain a second instance behind the first

The edge has exactly **one** catch-all slot, but two instances (prod and dev)
may both need dynamic custom domains. Point Traefik's `HostSNI(*)` router at
the **prod** instance as today, and give prod a *fallback upstream* so anything
it does not own is handed to dev, unterminated:

```
SP_ACME_FALLBACK_UPSTREAM_HTTPS=<dev address>:443
SP_ACME_FALLBACK_UPSTREAM_HTTP=<dev address>:80
SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL=true   # default
```

How it works: prod peeks the TLS ClientHello (SNI) on `:443` and the `Host`
header on `:80` **below** any TLS termination. A host prod serves — one of its
own hostnames or a verified custom domain — is terminated by prod exactly as
before. Anything else is dialed to the next hop, prefixed with a PROXY v2
header carrying the **original client**, and the peeked bytes are replayed
byte-for-byte, so dev completes its own handshake with its own certificate and
solves its own HTTP-01 / TLS-ALPN-01 challenges through the chain. Chains
deeper than two hops work the same way (one next hop each).

Requirements and consequences:

- **The downstream must trust the upstream**: set
  `SP_ACME_PROXY_PROTOCOL=true` and add the upstream's egress IPs to
  `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS` on the **dev** instance. Without it
  dev ignores the forwarded header and every custom-domain request appears to
  come from prod's IP, collapsing per-IP rate limiting to one bucket. (The
  inbound trust mechanism itself is unchanged — this is the same knob Option A
  already uses for Traefik.)
- **Both listeners or neither**: forwarding only `:443` leaves dev unable to
  solve HTTP-01. Set both unless dev is known to use TLS-ALPN-01 exclusively.
- **Failure is a closed connection**: if the next hop is unreachable, the
  client connection is dropped. It is never served locally — prod has no
  certificate for a domain it forwards, so "falling back" would only produce a
  misleading TLS error.
- **Never routable to itself**: a chain misconfigured into a cycle (prod → dev
  → prod) is refused rather than ping-ponged, both when the immediate peer is
  the configured upstream and when a connection has already crossed four hops.
  The refusal is logged once per offending peer.
- Both upstreams are validated at startup (`host:port`, and `SP_ACME_ENABLED`
  must be true), so a typo fails the pod rather than silently dropping every
  unknown-host connection.

Watch it with `solidping_tlsedge_connections_total{listener,outcome}` —
`outcome` is `local`, `forwarded`, `refused` or `dial_failed` — plus the
`tlsedge: forwarding to the fallback upstream` log line.

### Option C — keep the external TLS proxy

Leave `SP_ACME_ENABLED` unset and run a Caddy sidecar with `on_demand_tls`
gated on `/api/v1/public/custom-domains/allowed`. This is the pre-existing
setup and needs no code from this spec beyond the single-CNAME change.

### ✅ Chosen: option A, applied 2026-08-14 (k8xp `solidping-dev`)

Traefik SNI passthrough with PROXY protocol v2, on solidping `v0.15.1`.
Manifests live in the `k8xp` repo under `k8s/solidping/overlays/dev/`:

| File | What it does |
| --- | --- |
| `ingressroute-tcp.yaml` | `IngressRouteTCP`, entrypoint `websecure`, ``HostSNI(`*`)``, `tls.passthrough: true`, → `solidping:443` with `proxyProtocol.version: 2` |
| `ingressroute-http.yaml` | `IngressRoute`, entrypoint `web`, ``PathPrefix(`/`)`` at `priority: 1`, → `solidping:80` (HTTP-01 + the 308) |
| `service-patch.yaml` | adds ports 80/443 to the `solidping` Service |
| `environment-patch.yaml` | `SP_ACME_ENABLED`, `SP_ACME_EMAIL`, `SP_ACME_PROXY_PROTOCOL`, `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS=10.42.0.0/16`, `SP_CUSTOM_DOMAIN_CNAME_TARGET` |

Two things about this edge that are not obvious and cost time to establish:

- **The catch-all does not starve the other hosts.** Traefik's `ServeTCP`
  (v3.7.4, `pkg/server/router/tcp/router.go`) tries host-specific HTTPS routers
  first, then host-specific TCP routers, *then* the HTTPS catch-all, and only
  then the TCP catch-all. So ``HostSNI(`*`)`` is a fallback, not a competitor —
  verified live against 8 unrelated cluster hosts, all unaffected. The trap is
  the ordering of the last two: **any TLS router with no Host rule registers as
  an HTTPS catch-all and outranks this router**, silently starving it. Keep
  every Ingress host-scoped.
- **:80 must be an HTTP router, not a TCP one.** Traefik's non-TLS branch
  matches the TCP muxer *without* the catch-all guard, so a ``HostSNI(`*`)``
  TCP router on `web` swallows plain HTTP cluster-wide, cert-manager's own
  HTTP-01 solvers included. The HTTP router at `priority: 1` is safe because it
  merely loses every priority comparison.

TLS-ALPN-01 does **not** survive the passthrough: Traefik answers ACME-TLS/1
ALPN connections itself, above the muxers. Issuance goes through HTTP-01, which
is why both routes are required rather than just the TCP one.

### Token mode extra requirement

If `SP_CUSTOM_DOMAIN_CNAME_MODE=token` is ever enabled, the zone serving the
CNAME target must also answer for `*.cname.<target>` — and that wildcard must be
an **A/AAAA (or ALIAS) record, never a CNAME**. Go's resolver returns the
canonical name of a CNAME chain, so a wildcard CNAME would make the token hop
invisible and verification could never succeed.

## 3. Configuration reference

| Env var                          | Value for the k8xp live test         |
| -------------------------------- | ------------------------------------ |
| `SP_CUSTOM_DOMAIN_CNAME_TARGET`  | `solidping.k8xp.com`                 |
| `SP_CUSTOM_DOMAIN_CNAME_MODE`    | `shared`                             |
| `SP_ACME_ENABLED`                | `true`                               |
| `SP_ACME_EMAIL`                  | *(ops contact address)*              |
| `SP_ACME_CA_URL`                 | *(unset = LE production; use the LE staging directory for a dry run first)* |
| `SP_ACME_LISTEN_HTTP`            | `:80` (or a high port + Service port mapping) |
| `SP_ACME_LISTEN_HTTPS`           | `:443` (idem)                        |
| `SP_ACME_PROXY_PROTOCOL`         | `true` behind a TLS passthrough, otherwise unset |
| `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS` | pod/node CIDRs the proxy connects from — **required** when the above is true |
| `SP_ACME_FALLBACK_UPSTREAM_HTTPS` | `host:port` of the next hop for unknown-SNI TLS connections (Option D); unset = no forwarding |
| `SP_ACME_FALLBACK_UPSTREAM_HTTP`  | `host:port` of the next hop for plaintext `:80` (HTTP-01); unset = no forwarding |
| `SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL` | `true` (default) — send PROXY v2 with the original client to the next hop |

Do a first pass against **Let's Encrypt staging**
(`https://acme-staging-v02.api.letsencrypt.org/directory`) so a
misconfiguration cannot burn production rate limits. Browsers will warn about
the staging chain — that is expected; check the issuer name, not the padlock.
Then clear `SP_ACME_CA_URL`, wipe the staging assets
(`delete from tls_storage;` — safe: everything is re-issued on demand) and
repeat on production.

## 4. Live acceptance run — ✅ `status.webingenia.com`, 2026-08-14

Run against org `webingenia`, status page `webingenia`
(`a31cd829-ca4d-4f4c-bbe7-7e8da4bdbd7f`). The hostname is `status.` and not the
`status-page.` this runbook originally proposed. DNS record added directly via
the OVH record API (**not** `import-zones-to-ovh.py --import`, which rewrites
every record in the zone) and mirrored into `k8xp` `domain_zones/webingenia.com.txt`:

```
status.webingenia.com.  CNAME  solidping.k8xp.com.
```

A first full pass was done against Let's Encrypt **staging**, then
`SP_ACME_CA_URL` was cleared and the staging rows dropped from `tls_storage`
(`delete from tls_storage where key like '%acme-staging-v02%'` — certmagic
namespaces storage per CA, so the production assets were never at risk).

- [x] Verify turns green — `customDomainStatus: verified`.
- [x] `https://status.webingenia.com/` serves the status page with a
      publicly-trusted certificate (`ssl_verify_result=0`, LE `YE1`). First
      handshake took ~14 s while the certificate was obtained.
- [x] Cert material present in `tls_storage`; survives a pod restart with **no**
      re-issuance line in the log.
- [x] `http://status.webingenia.com/` 308-redirects to https.
- [x] `/dash0`, `/docs` and org API paths all return 404 on the custom host.
- [x] PROXY v2 carries the real client: the pod logs the external client IP,
      not the Traefik pod's `10.42.x.x`.
- [x] An unrelated hostname pointed at the edge (`nope.example.com`) is refused
      before any CA traffic — `tlsedge: refusing certificate for unknown host`.
- [x] Re-adding a cleared domain restores serving from the stored certificate
      (`customDomainCertStatus: issued`, no new issuance).
- [⚠️] Clearing the domain stops the **status page** but not the host — see below.

### ⚠️ Finding: a cleared domain still serves the dashboard

Clearing `customDomain` behaves as documented for everything the decision func
governs — `/api/v1/public/custom-domains/allowed` flips to 404 immediately, the
status page stops being served, renewal stops. But the hostname does **not**
stop responding:

- The certificate is already in `tls_storage`, and the decision func gates
  *issuance*, not serving from cache. So the TLS handshake keeps succeeding
  with a valid publicly-trusted certificate until it expires (up to 90 days).
- With no custom-domain mapping, the request falls through to the instance's
  own-host routing instead of being rejected, so the host now `302`s to
  `/dash0/` and serves the dashboard SPA — on a hostname the installation no
  longer claims. The custom-host path allowlist (which correctly 404s `/dash0`)
  only applies while the mapping exists.

Not an authentication hole — the dashboard still requires a login — but it does
mean a released or transferred domain keeps a working certificate and a
SolidPing dashboard on it, which is exactly the takeover scenario section 3 of
the feature doc says the re-verification sweep exists to close. Worth either
dropping the cached certificate when a domain is unmapped, or refusing unmapped
hostnames outright at the edge.

Useful commands while running it:

```bash
# 1. Verification state and the record the dashboard is showing.
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"webingenia","email":"...","password":"..."}' \
  'https://solidping.k8xp.com/api/v1/auth/login' | jq -r '.accessToken')
curl -s -H "Authorization: Bearer $TOKEN" \
  'https://solidping.k8xp.com/api/v1/orgs/webingenia/status-pages/a31cd829-ca4d-4f4c-bbe7-7e8da4bdbd7f' \
  | jq '{customDomain, customDomainStatus, customDomainCertStatus, customDomainRecords}'

# 2. The external-proxy contract still answers (unchanged by this spec).
curl -so /dev/null -w '%{http_code}\n' \
  'https://solidping.k8xp.com/api/v1/public/custom-domains/allowed?domain=status.webingenia.com'

# 3. Certificate actually presented on the custom host.
openssl s_client -connect status.webingenia.com:443 \
  -servername status.webingenia.com </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates

# 4. HTTP -> HTTPS redirect.
curl -sI http://status.webingenia.com/ | head -3

# 5. Custom-host allowlist (both must be 404).
curl -so /dev/null -w '%{http_code}\n' https://status.webingenia.com/dash0
curl -so /dev/null -w '%{http_code}\n' https://status.webingenia.com/api/v1/orgs/webingenia/checks

# 6. Stored TLS assets (keys only — NEVER dump values, they are private keys).
psql "$DSN" -c "select key, length(value), modified_at from tls_storage order by key;"

# 7. No re-issuance after a restart: the log must show no 'obtaining certificate'
#    line for the domain.
kubectl -n solidping-dev logs deploy/solidping | grep tlsedge
```

## 5. Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| Verify stays red | The `CNAME` does not resolve to the expected target. Check `dig +short CNAME status.webingenia.com` against the value the dashboard shows — in `token` mode the plain instance target is deliberately rejected. |
| `HTTPS failed` chip | Issuance failed. `grep tlsedge` the server log; the domain is in the log line. Common causes: `:80`/`:443` not reachable from the internet, or the LE rate limit. |
| `HTTPS pending` forever | No HTTPS request has reached the process yet — issuance is on-demand. Curl the host once. |
| `refusing certificate for unknown host` in the log | Working as intended: the hostname is not verified/servable. That is the guard protecting the CA rate limit. |
| Bind error on `:80`/`:443` at startup | The process lacks the privilege. Grant `NET_BIND_SERVICE` or move the listeners to high ports with `SP_ACME_LISTEN_HTTP`/`SP_ACME_LISTEN_HTTPS`. |
| Chained setup: the downstream's domain gets no certificate | Check `solidping_tlsedge_connections_total{outcome="dial_failed"}` and the `cannot reach the fallback upstream` log line on the upstream. If forwarding works but issuance still fails, `SP_ACME_FALLBACK_UPSTREAM_HTTP` is probably unset, so HTTP-01 never reaches the downstream. |
| Chained setup: `the fallback chain loops back here` in the log | The next hop forwards back here. Fix the topology: exactly one instance may point at another, and the last hop must have no upstream. |
| Chained setup: the downstream sees the upstream's IP as every client | The downstream is not trusting the upstream — set `SP_ACME_PROXY_PROTOCOL=true` and list the upstream's egress IPs in `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS` **on the downstream**. |

## 6. Security notes

- `tls_storage` holds ACME account keys and certificate **private keys**. It is
  read only by `internal/tlsedge` and is exposed through no API, export or debug
  surface. Never dump `value` in a support session or a bug report.
- Issuance is gated exclusively by the decision func (instance hosts ∪ verified
  custom domains). There is no configuration that widens it.
- Re-verification failure (3 consecutive) stops both serving and renewal.
