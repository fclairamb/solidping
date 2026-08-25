---
sidebar_position: 13
title: Custom Domains
---

# Custom Domains for Status Pages

Serve a status page on a domain you own — `status.yourcompany.com` — instead of
the installation's own `.../status0/...` URL. This is the surface your customers
see, so a branded hostname matters.

**One CNAME record, then automatic HTTPS.** That is the whole setup.

## Overview

A custom domain is:

- **One per status page.** Bind a hostname to a page; the page is then served at
  the root of that host (`https://status.yourcompany.com/`).
- **Verified with a single `CNAME`.** No second record, no TXT challenge.
- **Automatically secured** when in-server TLS is enabled: SolidPing obtains a
  Let's Encrypt certificate on the first HTTPS request and renews it for you.
  If you prefer, TLS can still be terminated by your own reverse proxy — see
  [Alternative: an external TLS proxy](#alternative-an-external-tls-proxy).
- **Periodically re-verified.** SolidPing re-checks the `CNAME` every few hours.
  A transient DNS fault is tolerated for days without interrupting your page,
  and the domain recovers on its own; only a domain that stays unreachable for
  days stops being served.

Only **verified, enabled, public** pages are served on a custom host. Everything
else on that host — the operator dashboard, docs, the rest of the API — returns
`404`. The status page's public API (page data, Atom feed, subscribe/confirm,
badges) is available on the custom host so the page is fully functional.

## Setting it up

1. **Add the domain.** Open the status page in the dashboard, find the **Custom
   domain** section, enter your hostname (e.g. `status.yourcompany.com`), and
   save. SolidPing shows you the one DNS record to create.
2. **Create the `CNAME`** at your domain provider:

   | Type    | Name                     | Value                          |
   | ------- | ------------------------ | ------------------------------ |
   | `CNAME` | `status.yourcompany.com` | *(your installation's target)* |

   The exact value is shown next to the record in the dashboard, with a
   copy-to-clipboard button.
3. **Click Verify.** SolidPing resolves the `CNAME` and checks that it points at
   the expected target. Once it does, the status chip flips to **Verified** and
   the page starts serving on your domain.
4. **Visit `https://status.yourcompany.com/`.** With in-server TLS enabled, the
   certificate is obtained during that first request, so the very first visit
   may take a few seconds. The dashboard then shows an **HTTPS active** chip.

The `CNAME` target defaults to the host of your installation's base URL. Set it
explicitly with `SP_CUSTOM_DOMAIN_CNAME_TARGET` (see
[Configuration](#configuration)).

### Apex domains

A `CNAME` is illegal at a zone apex (`yourcompany.com` with no subdomain). Use a
subdomain (`status.yourcompany.com`), or an `ALIAS`/`ANAME` record if your DNS
provider supports one that points at the same target.

## Verification modes

`server.custom_domain_cname_mode` picks what the `CNAME` must point at. Both
modes ask the customer for exactly one record.

### `shared` (default)

The customer points their host at the plain installation target:

```
status.yourcompany.com.  CNAME  solidping.example.com.
```

This is the simplest possible UX and matches what every hosted status-page
product asks for.

:::caution Dangling-CNAME trade-off
Because every customer points at the *same* target, the target alone does not
prove who owns the domain. If a customer deletes their status page but leaves
the `CNAME` in place, another organization can claim that hostname — the global
uniqueness constraint means whoever claims it first wins, and the previous owner
gets a `409 Conflict` if they come back.

This is acceptable for most installations. If it is not acceptable for yours,
use `token` mode.
:::

### `token`

The `CNAME` target is derived from a per-page secret:

```
status.yourcompany.com.  CNAME  spq7f3k2m6x4t7b.cname.solidping.example.com.
```

Still one record, but now the target itself proves ownership: a dangling
`CNAME` can only ever be re-claimed by a page that holds that exact token. A
domain verified in `token` mode does **not** verify against the plain shared
target — there is deliberately no dual-accept, since accepting both would
nullify the protection.

:::warning Wildcard DNS requirement (token mode)
Token mode requires the installation's own zone to answer for
`*.cname.<your-target>`, and that wildcard **must be an `A`/`AAAA` (or `ALIAS`)
record — never a `CNAME`.**

Go's resolver returns the *canonical* (final) name of a CNAME chain. If
`*.cname.<target>` were itself a `CNAME`, the customer's token hop would be
invisible in the answer and verification could never match.
:::

Switching modes does not rewrite existing rows: a page picks up the new mode's
record the next time its domain is verified, and the dashboard shows the record
for the currently configured mode.

## HTTPS

### In-server (recommended)

Set `SP_ACME_ENABLED=true` and `SP_ACME_EMAIL=...` and the server terminates TLS
itself:

- It listens on `:80` (ACME HTTP-01 challenge, and a `308` redirect to https for
  everything else) and `:443` (TLS).
- Certificates are obtained **on demand**, during the first TLS handshake for a
  hostname, and renewed automatically.
- A hostname is only ever sent to the CA if it is one of the installation's own
  hosts *or* a verified, enabled, public custom domain. Unknown and unverified
  hostnames are refused before any CA traffic, which blocks certificate
  squatting and protects Let's Encrypt's failed-validation rate limit.
- Certificates, private keys and the ACME account live in the database
  (`tls_storage`), so every replica shares them and a restart never re-issues.
- Your installation's own hostname gets a certificate too, so a self-hoster does
  not need any reverse proxy at all.

The dashboard shows the per-domain certificate state next to the domain:
**HTTPS pending** (nothing issued yet), **HTTPS active**, or **HTTPS failed**
(the reason is in the server log, keyed by domain).

Requirements:

- The process must be able to bind `:80` and `:443` — run it with the
  capability, as root, or remap the ports with `SP_ACME_LISTEN_HTTP` /
  `SP_ACME_LISTEN_HTTPS` and forward to them.
- Custom-domain traffic must reach the process with TLS **not** already
  terminated by something else. Behind a Kubernetes ingress that terminates TLS
  for known hostnames only, use SNI passthrough or a dedicated
  LoadBalancer/NodePort for the CNAME target.

### Behind a TLS passthrough: PROXY protocol

A TLS passthrough is opaque on purpose: the proxy in front (a Traefik
``HostSNI(`*`)`` TCP router, an HAProxy `mode tcp` frontend, an NLB…) never sees
HTTP, so there is no `X-Forwarded-For` and every request would appear to come
from the proxy's own address. Per-IP rate limiting and abuse logging silently
collapse to "everything comes from one IP".

Set `SP_ACME_PROXY_PROTOCOL=true` and the two ACME listeners read a PROXY
protocol preamble (v1 or v2) before the payload — on the TLS listener it is
consumed *before* the handshake, so nothing else in the chain changes and
handlers see the real client. Configure the proxy to send it (in Traefik, a TCP
service with `proxyProtocol: {version: 2}`).

`SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS` is the security-relevant half. The
preamble is unauthenticated — anyone able to open a TCP connection can prepend
one — so it is only honored from the sources you list (CIDR ranges or bare IPs,
comma-separated; for Kubernetes, the pod/node CIDRs the proxy dials from):

- **Trusted source**: the header is used when present, and a connection *without*
  one is still accepted. Health probes (kubelet, in-cluster checks) do not send
  a preamble and must keep working.
- **Any other source**: the connection is served normally, but its header is
  ignored — the real peer address is what the rate limiter and the logs see. A
  client connecting directly can never forge an arbitrary IP.
- **Empty list**: startup fails. Trusting everyone by default would be an
  IP-spoofing hole, so this fails closed rather than guessing. Unparseable
  entries fail startup too instead of being silently dropped.

Both listeners are wrapped, not just the TLS one: the plain listener carries the
HTTP-01 challenge and the redirect to https, and its client IP feeds the same
rate limiter.

### Alternative: an external TLS proxy

In-server ACME is opt-in and entirely independent of the external-proxy path,
which remains supported and unchanged. SolidPing exposes one small contract your
edge can use to decide whether it should obtain a certificate for an incoming
hostname:

```
GET /api/v1/public/custom-domains/allowed?domain=status.yourcompany.com
  → 204  the domain is a verified, enabled, public custom domain
  → 404  otherwise
```

#### Caddy `on_demand_tls`

[Caddy](https://caddyserver.com/) can obtain certificates on demand, gated by
that endpoint, so it only issues certs for domains SolidPing actually serves:

```caddyfile
{
  on_demand_tls {
    ask http://solidping:4000/api/v1/public/custom-domains/allowed
  }
}

# Your own hosts, terminated normally.
solidping.example.com {
  reverse_proxy solidping:4000
}

# Any other host: obtain a cert on demand (gated by `ask`) and proxy through.
https:// {
  tls {
    on_demand
  }
  reverse_proxy solidping:4000
}
```

Leave `SP_ACME_ENABLED` unset (`false`) in this setup so the server does not
also try to bind `:80`/`:443`.

#### Wildcard or manual certificates

If you own a single parent zone, put a **wildcard certificate**
(`*.yourcompany.com`) or a manually managed certificate on your reverse proxy
and skip on-demand issuance entirely.

#### cert-manager / Kubernetes

Terminate at the edge using the same `allowed` endpoint — a Caddy sidecar with
`on_demand_tls`, or cert-manager gated on the endpoint.

## Verification & takeover protection

After the first successful verification, a background job re-runs the `CNAME`
check every 6 hours and moves the domain through four states:

| State | Served? | What it means |
| --- | --- | --- |
| **Pending** | no | Configured, never verified. Only an explicit **Verify** promotes it. |
| **Active** | yes | Verified and healthy. |
| **Grace** | **yes** | Re-checks have failed 3 times in a row (~18 hours). The page keeps serving; the dashboard shows a *DNS re-check failing* warning with the last diagnostic. |
| **Demoted** | no | Re-checks failed 12 times in a row (~3 days). Serving stops, certificate renewal stops, and the `allowed` endpoint answers `404`. |

Grace exists because the two things one counter used to conflate are not the
same: a DNS blip is common and temporary, a released or transferred domain is
rare and permanent. Reacting to the first as if it were the second took status
pages offline for hours of resolver trouble.

**Recovery is automatic.** Once the `CNAME` resolves correctly again, a demoted
domain is re-promoted after **3 consecutive successful checks** — provided
SolidPing is still holding an unexpired certificate for it. You can always skip
the wait by clicking **Verify**. A domain that has *never* verified is never
promoted automatically: the first verification is always an explicit action.

**A hard demotion notifies you.** It writes a
`statuspage.custom_domain.demoted` event to the organization's activity feed
and emails every owner and admin, so a dark status page is not discovered by
your customers during an outage. Entering *grace* is deliberately silent — the
page is still serving.

**A domain that is no longer served fails legibly, not at the TLS layer.** If
SolidPing still holds a valid certificate for the hostname, the handshake
completes and the visitor gets a plain "status page unavailable" page (`503`)
naming the host. It never returns a browser security interstitial, and it never
serves the operator dashboard on a hostname you no longer control. A hostname
SolidPing holds no certificate for is still refused outright at the handshake —
that refusal is what protects Let's Encrypt's failed-validation rate limit from
a hostile SNI scan.

Clearing or changing a custom domain deletes its certificate and private key
from `tls_storage`, so no live key is left for a hostname someone else may now
control.

The serving decision is memoized for 30 seconds, so a domain you remove can
still be served for up to that long. Only *positive* answers are cached: a
domain you have just verified starts serving on the very next request.

The custom-domain column is **globally unique** among live pages, so two
organizations can never both hold the same hostname at once; a conflicting
attempt is rejected with `409 Conflict`.

## Configuration

| Setting                              | Env var                                                                  | Default            | Description                                                                        |
| ------------------------------------ | ------------------------------------------------------------------------ | ------------------ | ---------------------------------------------------------------------------------- |
| `server.custom_domain_cname_target`  | `SP_CUSTOM_DOMAIN_CNAME_TARGET` / `SP_SERVER_CUSTOM_DOMAIN_CNAME_TARGET` | host of `base_url` | The hostname customers point their `CNAME` at (shown in the DNS record).           |
| `server.custom_domain_cname_mode`    | `SP_CUSTOM_DOMAIN_CNAME_MODE` / `SP_SERVER_CUSTOM_DOMAIN_CNAME_MODE`     | `shared`           | `shared` or `token` — see [Verification modes](#verification-modes).               |
| `acme.enabled`                       | `SP_ACME_ENABLED`                                                        | `false`            | Master switch for in-server TLS. Off = no extra listeners and no CA traffic.        |
| `acme.email`                         | `SP_ACME_EMAIL`                                                          | *(none)*           | ACME account contact address. **Required** when `acme.enabled` is true.            |
| `acme.ca_url`                        | `SP_ACME_CA_URL`                                                         | Let's Encrypt prod | ACME directory URL. Point it at the LE staging directory while testing.            |
| `acme.listen_http`                   | `SP_ACME_LISTEN_HTTP`                                                    | `:80`              | HTTP-01 challenge listener; redirects everything else to https with a `308`.        |
| `acme.listen_https`                  | `SP_ACME_LISTEN_HTTPS`                                                   | `:443`             | TLS listener. Requests flow into the normal routing, so custom hosts behave alike.  |
| `acme.proxy_protocol`                | `SP_ACME_PROXY_PROTOCOL`                                                 | `false`            | Read a PROXY protocol (v1/v2) preamble on **both** ACME listeners — see [Behind a TLS passthrough](#behind-a-tls-passthrough-proxy-protocol). |
| `acme.proxy_protocol_trusted_cidrs`  | `SP_ACME_PROXY_PROTOCOL_TRUSTED_CIDRS`                                   | *(none)*           | Comma-separated CIDR ranges or IPs whose PROXY header is honored. **Required** when `acme.proxy_protocol` is true — an empty list fails startup. |
| `acme.fallback_upstream_https`       | `SP_ACME_FALLBACK_UPSTREAM_HTTPS`                                        | *(none)*           | `host:port` of a second instance to hand unknown-SNI TLS connections to, unterminated. Empty = refuse them here, as before. |
| `acme.fallback_upstream_http`        | `SP_ACME_FALLBACK_UPSTREAM_HTTP`                                         | *(none)*           | Same next hop for plaintext `:80`, so the downstream can solve its own HTTP-01 challenges. |
| `acme.fallback_upstream_proxy_protocol` | `SP_ACME_FALLBACK_UPSTREAM_PROXY_PROTOCOL`                            | `true`             | Prefix forwarded connections with a PROXY v2 header carrying the original client. |

### Chaining a second instance

Some edges (a Traefik `HostSNI(*)` catch-all, for instance) can only forward
unknown hostnames to ONE backend. Setting `acme.fallback_upstream_https` and
`acme.fallback_upstream_http` lets that backend pass on what it does not serve:
the hostname is read from the TLS ClientHello (or the `Host` header) *below*
any TLS termination, and a connection for a domain this instance does not own
is spliced to the next hop with every byte replayed, so that instance completes
its own handshake with its own certificate. The next hop should trust this one
for PROXY protocol (`acme.proxy_protocol` +
`acme.proxy_protocol_trusted_cidrs`) so it still sees the real client. An
unreachable next hop closes the connection — a forwarded domain is never served
here.

## Entitlements

Custom domains are gated by the `maxCustomDomains` entitlement. Self-hosted
installations are unlimited by default; managed (SaaS) plans set a per-plan cap.
Attempting to add a domain beyond the cap returns a quota error with a link to
your plan's usage page.
