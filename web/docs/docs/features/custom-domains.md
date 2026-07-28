---
sidebar_position: 13
title: Custom Domains
---

# Custom Domains for Status Pages

Serve a status page on a domain you own — `status.yourcompany.com` — instead of
the installation's own `.../status0/...` URL. This is the surface your customers
see, so a branded hostname matters.

## Overview

A custom domain is:

- **One per status page.** Bind a hostname to a page; the page is then served at
  the root of that host (`https://status.yourcompany.com/`).
- **DNS-verified.** You prove ownership (a `TXT` challenge) and set up routing (a
  `CNAME`). Both are required before the page is served.
- **Periodically re-verified.** SolidPing re-checks the ownership record every
  few hours and stops serving the domain if it is released or transferred.

Only **verified, enabled, public** pages are served on a custom host. Everything
else on that host — the operator dashboard, docs, the rest of the API — returns
`404`. The status page's public API (page data, Atom feed, subscribe/confirm,
badges) is available on the custom host so the page is fully functional.

## Setting it up

1. **Add the domain.** Open the status page in the dashboard, find the **Custom
   domain** section, enter your hostname (e.g. `status.yourcompany.com`), and
   save. SolidPing generates a one-time challenge token and shows you two DNS
   records.
2. **Create the DNS records** at your domain provider:

   | Type    | Name                                       | Value                          |
   | ------- | ------------------------------------------ | ------------------------------ |
   | `CNAME` | `status.yourcompany.com`                   | *(your installation's target)* |
   | `TXT`   | `_solidping-challenge.status.yourcompany.com` | `sp-domain-verify=<token>`  |

   The exact values are shown next to each record in the dashboard, with a
   copy-to-clipboard button.
3. **Click Verify.** SolidPing checks that the `TXT` record proves ownership and
   the `CNAME` points at the installation. Once both pass, the status chip flips
   to **Verified** and the page starts serving on your domain.

The `CNAME` target defaults to the host of your installation's base URL. Set it
explicitly with `SP_CUSTOM_DOMAIN_CNAME_TARGET` (see
[Configuration](#configuration)).

### Apex domains

A `CNAME` is illegal at a zone apex (`yourcompany.com` with no subdomain). Use a
subdomain (`status.yourcompany.com`), or an `ALIAS`/`ANAME` record if your DNS
provider supports one that points at the same target.

## TLS

The Go server **does not terminate TLS or issue certificates** for custom
domains — that stays with your edge, which keeps the server
single-responsibility. SolidPing exposes one small contract the edge uses to
decide whether it should obtain a certificate for an incoming hostname:

```
GET /api/v1/public/custom-domains/allowed?domain=status.yourcompany.com
  → 204  the domain is a verified, enabled, public custom domain
  → 404  otherwise
```

### Self-hosted (Caddy `on_demand_tls`)

[Caddy](https://caddyserver.com/) can obtain certificates on demand, gated by the
`allowed` endpoint, so it only issues certs for domains SolidPing actually
serves:

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

Alternatively, if you own a single parent zone, put a **wildcard certificate**
(`*.yourcompany.com`) or a manually managed certificate on your reverse proxy and
skip on-demand issuance entirely.

### SaaS / Kubernetes edge

Terminate at the edge using the same `allowed` endpoint — a Caddy sidecar with
`on_demand_tls`, or cert-manager gated on the endpoint. There is **no in-server
ACME** in this version; the `allowed` endpoint is the only contract between
SolidPing and the TLS edge.

## Verification & takeover protection

After the first successful verification, a background job re-runs the ownership
(`TXT`) check every 6 hours. If the record disappears (the domain was released or
transferred away), the check fails; after **3 consecutive failures** SolidPing
clears the verification, stops serving the page on that host, and the `allowed`
endpoint starts answering `404`. Restore the `TXT` record and click **Verify**
again to bring it back.

The custom-domain column is **globally unique** among live pages, so two
organizations can never both claim the same hostname — the first to verify wins,
and a conflicting attempt is rejected with a `409 Conflict`.

## Configuration

| Setting                              | Env var                                                        | Default                | Description                                                                 |
| ------------------------------------ | -------------------------------------------------------------- | ---------------------- | --------------------------------------------------------------------------- |
| `server.custom_domain_cname_target`  | `SP_CUSTOM_DOMAIN_CNAME_TARGET` / `SP_SERVER_CUSTOM_DOMAIN_CNAME_TARGET` | host of `base_url`     | The hostname customers point their `CNAME` at (shown in the DNS records).   |

## Entitlements

Custom domains are gated by the `maxCustomDomains` entitlement. Self-hosted
installations are unlimited by default; managed (SaaS) plans set a per-plan cap.
Attempting to add a domain beyond the cap returns a quota error with a link to
your plan's usage page.
