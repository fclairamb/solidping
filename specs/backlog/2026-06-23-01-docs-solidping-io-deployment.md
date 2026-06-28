# Finish docs.solidping.io deployment — k8xp ingress hostnames + TLS

> Infra/deployment spec, not code. The docs-site **code is done and shipped**
> (`web/docs/` embedded in the binary, served at `/docs` on every host; released
> in v0.1.0). What is missing is purely cluster routing: `docs.solidping.io` (and
> `solidping.io`) resolve to the k8xp ingress but have **no host rule**, so they
> 404. Builds on the implemented spec
> `specs/done/2026/06/2026-06-22-01-docs-site-from-code-repo.md` and the drafted
> `deploy/docs-ingress.yaml`. Needs `kubectl`/cluster access (out of band from
> this repo's CI, which only does `kubectl set image`).

## Context

The documentation site is built into the SolidPing binary and served at the
**`/docs`** path on every host (Docusaurus, `baseUrl: '/docs/'`). The binary also
redirects the configured docs host (`server.docs_host`, default
`docs.solidping.io`) root → `/docs` (`handlerWithDocsHost` in
`server/internal/app/server.go`). This shipped in **v0.1.0** and is deployed to
k8xp. It demonstrably works:

```
https://solidping.k8xp.com/docs            -> 200   (app serves the docs site)
https://solidping.k8xp.com/api/mgmt/health -> 200   (app is up)
```

The marketing site (`www.solidping.io`, the `solidping-website` repo) now links
its docs to **`https://solidping.io/docs`**.

**The gap — hostname routing.** DNS is already correct:

```
docs.solidping.io  →  (CNAME) solidping.io  →  193.70.42.217  =  eu1.k8xp.com   (k8xp ingress)
solidping.io       →                          193.70.42.217  =  eu1.k8xp.com
```

But the k8xp ingress only has a host rule for **`solidping.k8xp.com`** → the
`solidping` service. There is **no rule for `solidping.io` or
`docs.solidping.io`**, so the ingress returns its default-backend 404 for them:

```
https://solidping.io/dash0/        -> 404   (even the dashboard)
https://solidping.io/docs          -> 404
https://docs.solidping.io/         -> 404
```

So this is **not** a docs problem — the app is simply not wired to those
hostnames on the cluster. `deploy/docs-ingress.yaml` is a drafted template for
the `docs.solidping.io` rule but has not been applied.

(Aside: `solidping.k8xp.com` reports `version: "dev"` — a WireGuard laptop tunnel
intercepts that hostname locally, so probing it from the maintainer's laptop hits
a dev binary, not the v0.1.0 pod. Irrelevant to the ingress fix, but don't be
fooled by it when verifying.)

## Goal

In production (k8xp), both:

- `https://docs.solidping.io/` → (binary redirect) → `/docs/` → the docs site,
  with a valid TLS cert; and
- `https://solidping.io/docs` (and the rest of the app at `solidping.io`) →
  served — because the marketing site links to `solidping.io/docs` and the main
  app domain currently 404s too.

…by adding the missing ingress host rules + TLS, with no code change.

## Non-goals

- **Clean docs.solidping.io root URLs** (no `/docs` in the path). With the current
  single `baseUrl: '/docs/'` build, `docs.solidping.io/` lands on
  `docs.solidping.io/docs/`. A clean root would need a second `baseUrl: '/'` build
  served only on that host (the "two builds" follow-up noted in the implemented
  spec). Out of scope here.
- **Moving docs off the binary to a CDN.** The optional CDN-mirror (Slice 6 of the
  implemented spec) is a separate path; this spec finishes the embedded-serve
  deployment.
- **Re-architecting the cluster / gitops.** Just add the host rules; if a
  gitops/helm source of truth exists elsewhere, land them there instead of a
  one-off `kubectl apply`.
- **Code changes.** The serving + redirect already work (proven at
  `solidping.k8xp.com/docs`).

## Design

### 1. Inspect the working ingress (model to copy)

`solidping.k8xp.com` already routes to the app. Read its ingress to copy the
exact service name, port, ingress class, and TLS/cert setup:

```bash
kubectl -n solidping-dev --context=k8xp get ingress -o wide
kubectl -n solidping-dev --context=k8xp get ingress <name> -o yaml   # the solidping.k8xp.com one
kubectl -n solidping-dev --context=k8xp get svc                       # confirm service name + port (expect solidping:4000)
```

Everything below assumes service `solidping` port `4000` and namespace
`solidping-dev` — **confirm against the above** before applying.

### 2. Add the hostnames → the `solidping` service

Two options; prefer whichever matches how `solidping.k8xp.com` is configured:

- **(a) Extend the existing ingress** — add `solidping.io` and
  `docs.solidping.io` to its `spec.rules[].host` list (and `spec.tls[].hosts`),
  same backend. Lowest-risk, single source of truth.
- **(b) Apply `deploy/docs-ingress.yaml`** — the drafted standalone ingress for
  `docs.solidping.io`. Fill its TODOs (ingress class, cert issuer, service) from
  step 1. Add a sibling rule (or a second file) for `solidping.io`.

**Host-header preservation is required.** The binary's docs-host redirect fires
only when it sees `Host: docs.solidping.io`. Standard ingress controllers pass
the original Host through to the backend by default — keep it that way (do **not**
rewrite the upstream Host). No path rewrite is needed: route `host/* → service/*`
and let the binary handle `/` → `/docs/`.

### 3. TLS

`docs.solidping.io` and `solidping.io` each need a cert. Reuse the mechanism the
`solidping.k8xp.com` ingress uses:
- cert-manager `cluster-issuer` annotation + a `tls:` block (per host or a shared
  wildcard `*.solidping.io` secret), or
- whatever terminates TLS today (if it's terminated at an edge/LB, add the SANs
  there instead).

Verify the issued cert's SANs include both new hostnames.

### 4. Config sanity

- `server.docs_host` defaults to `docs.solidping.io`; ensure the prod deployment
  does **not** override `SP_DOCS_HOST`/`SP_SERVER_DOCS_HOST` to something else
  (else the root redirect won't fire for that host). Default is correct — just
  confirm it isn't overridden in the deployment env.

### 5. Land the config in the repo

Commit the final, applied ingress YAML(s) into `deploy/` (replace the template
placeholders with the real values) so the routing is version-controlled and
re-appliable, and note the one-time `kubectl apply` step in a short `deploy/README`.

## Verification

From a host that is **not** behind the WireGuard tunnel (so `solidping.io` /
`docs.solidping.io` resolve to the real cluster), with a normal TLS trust store
(a browser, or `curl` without `-k`):

```bash
# docs subdomain: root redirects into /docs, then serves
curl -sS -I https://docs.solidping.io/                       # 302 → /docs/
curl -sSL -o /dev/null -w "%{http_code} %{url_effective}\n" https://docs.solidping.io/   # 200, …/docs/
curl -sS -o /dev/null -w "%{http_code}\n" https://docs.solidping.io/docs/installation/docker   # 200

# main domain: app + docs path (where the marketing site links)
curl -sS -o /dev/null -w "%{http_code}\n" https://solidping.io/api/mgmt/health   # 200
curl -sS -o /dev/null -w "%{http_code}\n" https://solidping.io/docs              # 200
curl -sS -o /dev/null -w "%{http_code}\n" https://solidping.io/dash0/            # 200

# TLS valid (no -k needed); cert SANs include both hosts
echo | openssl s_client -connect docs.solidping.io:443 -servername docs.solidping.io 2>/dev/null | openssl x509 -noout -text | grep -A1 "Subject Alternative Name"
```

- The marketing site's docs links (`solidping.io/docs`, footer/navbar) resolve to
  a live page.
- Old `www.solidping.io/docs/*` URLs still bounce via the 404 redirect to
  `solidping.io/docs/*` (already shipped) and now land on a real page.

## Risk log

| Risk | Mitigation |
|---|---|
| Wrong service name/port in the new rule → 503/404 | Copy exact backend from the working `solidping.k8xp.com` ingress (step 1) |
| Ingress rewrites the upstream Host → binary's docs-host redirect never fires (docs.solidping.io/ → /dash0) | Preserve the original Host header; no `upstream-vhost`/host rewrite |
| No TLS cert for the new SANs → HTTPS fails (browser cert error) | Issue per-host or wildcard `*.solidping.io` cert; verify SANs |
| `SP_DOCS_HOST` overridden in prod env → root redirect points at the wrong host | Confirm the deployment uses the default `docs.solidping.io` |
| `solidping.io` apex already routed elsewhere (e.g. redirect to www) | Check existing DNS/ingress for the apex before adding the app rule; coordinate with the marketing-site hosting |
| Adding `solidping.io` to the app exposes the dashboard/API on the apex unexpectedly | Intended (it's the app domain), but confirm auth/CORS/`base_url` are correct for that host |
| Tunnel confusion during verification (`solidping.k8xp.com` → laptop dev binary) | Verify against `solidping.io`/`docs.solidping.io` from off-tunnel, or turn the tunnel off |
| Double `/docs/docs/` if someone path-rewrites instead of host-routing | Do not prepend `/docs` at the ingress — `baseUrl '/docs/'` already does it; just route the host through |
