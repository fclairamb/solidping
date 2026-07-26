---
model: opus
effort: xhigh
---

# Custom domains require two DNS records and an external TLS proxy

## Problem

The shipped custom-domain flow (spec `2026-07-22-01`) asks the customer for
**two** DNS records — a routing CNAME plus a `_solidping-challenge` TXT
ownership token ([verifier.go:119](server/internal/domainverify/verifier.go:119)) —
and leaves TLS entirely outside the Go server: the only certificate story is
"run Caddy `on_demand_tls` against
`GET /api/v1/public/custom-domains/allowed`"
([server.go:1187](server/internal/app/server.go:1187)). That is a real adoption
tax on both audiences:

- **Customers** must create and keep two records where every competitor
  (Instatus, Better Stack, Statuspage) needs one CNAME.
- **Self-hosters** must deploy and configure a reverse proxy before a custom
  domain can serve HTTPS at all — the binary has no TLS listener anywhere.

The TXT challenge adds little real ownership proof over the CNAME (both require
control of the same DNS zone). Its one genuine job is preventing
**dangling-CNAME takeover across orgs** (customer deletes their page but leaves
the CNAME up; another org claims the domain). That protection can be preserved
with a *single* record by making the CNAME target itself carry the per-page
token.

## Proposal

### Scope

1. **CNAME-only verification** — drop the TXT challenge entirely.
2. **In-server ACME** (Let's Encrypt via [certmagic](https://github.com/caddyserver/certmagic)),
   on-demand per custom domain, certificates stored in the DB, opt-in by
   config. The external-proxy path (`allowed` endpoint) remains supported and
   unchanged.
3. **Full test coverage**, including a live end-to-end acceptance run on
   `status-page.webingenia.com` against the k8xp SaaS deployment.

Out of scope (unchanged from v1): apex domains (CNAME is illegal at the apex —
docs-only ALIAS/ANAME guidance), multiple domains per page,
canonical→custom redirects.

### CNAME verification modes

Config `server.custom_domain_cname_mode`: `"shared"` (default) | `"token"`.

- **shared** — the customer CNAMEs to the plain instance target
  (`status.webingenia.com CNAME solidping.k8xp.com`), i.e. today's
  `Config.CustomDomainCNAMETarget()`
  ([config.go:357](server/internal/config/config.go:357)). Verification =
  CNAME resolves to that target. This is the requested UX. **Trade-off,
  documented in the spec and the docs page**: a dangling CNAME left pointing at
  the shared target can be claimed by any org (first-claim-wins via the global
  unique index is the only arbiter). Acceptable for now; mode exists so SaaS
  can tighten later.
- **token** — the CNAME target is per-page:
  `<token>.cname.<cnameTarget>` (reusing the existing
  `custom_domain_token` column, [status_page.go](server/internal/db/models/status_page.go)).
  Verification = the domain's CNAME resolves to the page's own token host.
  Same one-record UX, restores the ownership binding the TXT record provided.
  Requires a wildcard DNS entry `*.cname.<cnameTarget>`.

**DNS wildcard caveat (token mode)**: the wildcard must be an **A/AAAA (or
ALIAS) record**, not a CNAME — Go's `net.Resolver.LookupCNAME` returns the
*canonical* (final) name of the chain, so if `*.cname.…` were itself a CNAME
the token hop would be invisible. Document this; verification compares the
first/canonical CNAME of the customer domain against the expected token host.

Verification passes in the configured mode only (no dual-accept — accepting
the shared target in token mode would nullify the protection).

### What gets removed / changed

- `domainverify`: delete `CheckTXT`, `ChallengeLabel`, `TXTValuePrefix`
  ([verifier.go:23-27](server/internal/domainverify/verifier.go:23));
  `Verify` becomes CNAME-only; `Records` returns a single CNAME record whose
  value depends on the mode. Keep `Normalize` untouched.
- Token generation ([custom_domain.go:47](server/internal/handlers/statuspages/custom_domain.go:47))
  stays — the token now names the CNAME target in token mode (shorten to ~12
  base32/hex chars so the full target fits DNS label limits; a DNS label caps
  at 63 chars). Migration note: existing tokens are 43-char base64url —
  regenerate on next domain set, don't retro-rewrite rows.
- Re-verify job ([job_custom_domain_verify.go:101](server/internal/jobs/jobtypes/job_custom_domain_verify.go:101)):
  re-check **CNAME** (mode-aware) instead of TXT. Keep the 6 h cadence, the
  3-consecutive-failures unverify, and the never-promote rule.
- API response: `customDomainRecords` shrinks to one entry (shape unchanged —
  array of `{type,name,value}`); no OpenAPI structural change beyond the docs
  text ([openapi.yaml](server/internal/app/openapi/openapi.yaml) is
  hand-maintained).
- Dashboard ([status-page-form.tsx](web/dash0/src/components/shared/status-page-form.tsx)):
  the DNS-records block renders whatever the API returns, so it drops to one
  row naturally; update helper copy ("create this CNAME record") in all four
  locales (`web/dash0/src/locales/{en,fr,de,es}/`).
- Docs (`web/docs/docs/features/custom-domains.md`): rewrite around "one
  CNAME + automatic HTTPS"; demote the Caddy `on_demand_tls` section to an
  "alternative: external TLS proxy" appendix. Document the token-mode wildcard
  requirement and the apex limitation.

### In-server ACME (certmagic)

New package `server/internal/tlsedge` (name at implementer's discretion):

- **Library**: `github.com/caddyserver/certmagic` — chosen over
  `x/crypto/acme/autocert` for on-demand issuance with a decision callback,
  pluggable storage, cluster-safe locking, renewal management, and
  rate-limit-aware retries.
- **On-demand gating**: `certmagic.OnDemandConfig.DecisionFunc` allows a
  hostname iff it is one of the instance's own hosts (base-url host, docs
  host, CNAME target — the `reservedHosts` set,
  [custom_domain.go:67](server/internal/handlers/statuspages/custom_domain.go:67),
  so self-hosters get TLS for the main host too) **or**
  `CustomDomainServable(ctx, host)` is true
  ([custom_domain.go:285](server/internal/handlers/statuspages/custom_domain.go:285)).
  This is also the guard against attackers burning Let's Encrypt's
  failed-validation limits (5/hour/hostname): unverified domains never trigger
  issuance.
- **Storage**: implement `certmagic.Storage` (Store/Load/Delete/Exists/List/
  Stat + Lock/Unlock) over a new `tls_storage` key-value table
  (`key TEXT PK, value BYTEA/BLOB, modified_at`), in **both** engines. Locks
  via a `tls_storage_locks` row with expiry (certmagic tolerates coarse
  locking; single-writer per key is enough). Migration goes into the next
  consolidated release migration (`009_v0_8_0.up/down.sql`, postgres + sqlite —
  latest existing is `008_v0_7_0`).
- **Listeners**: when enabled, the server additionally listens on
  `acme.listen_http` (default `:80` — serves `/.well-known/acme-challenge/`
  and 308-redirects everything else to https) and `acme.listen_https`
  (default `:443` — `tls.Config` from certmagic, requests then flow into the
  existing top-level handler chain, so custom-host routing
  ([custom_domain_routing.go](server/internal/app/custom_domain_routing.go))
  applies unchanged: status page + allowlisted API only, 404 for `/dash0`
  etc.). Both HTTP-01 and TLS-ALPN-01 solvers enabled.
- **Cleanup**: when a domain is cleared or unverified, do nothing active —
  the decision func stops new issuance/renewal and serving; stored assets
  age out. (Optional nicety: delete cert assets on domain clear.)
- **Failure surfacing**: issuance errors must be visible — log with the
  domain, and expose the last error per domain via the authed status-page
  response (`customDomainCertStatus: "none" | "issued" | "error"` derived from
  certmagic cache/storage) so the dashboard can show why HTTPS isn't live yet.

### Config

New `acme` block (all koanf multi-word keys need the manual env reader —
extend `applyServerEnv` / `manualReaderEnvVars()`
([envvars.go:91](server/internal/config/envvars.go:91)); see the
`SP_*` koanf quirk):

| Key | Env | Default | Meaning |
|---|---|---|---|
| `acme.enabled` | `SP_ACME_ENABLED` | `false` | Master switch; off = zero behavior change |
| `acme.email` | `SP_ACME_EMAIL` | `""` | ACME account contact (required when enabled) |
| `acme.ca_url` | `SP_ACME_CA_URL` | LE production | Directory URL — overridable to LE staging / Pebble for tests |
| `acme.listen_http` | `SP_ACME_LISTEN_HTTP` | `:80` | Challenge + redirect listener |
| `acme.listen_https` | `SP_ACME_LISTEN_HTTPS` | `:443` | TLS listener |
| `server.custom_domain_cname_mode` | `SP_CUSTOM_DOMAIN_CNAME_MODE` | `shared` | `shared` \| `token` (see above) |

### Deployment prerequisite (k8xp — infra, out of repo scope but blocking the live test)

The ingress currently terminates TLS for known hostnames only; custom domains
are unknown ahead of time, so custom-domain traffic must reach the pod with
TLS intact on 443 (and plain 80 for HTTP-01). Options: SNI-passthrough at the
ingress, or a dedicated LoadBalancer/NodePort that `solidping.k8xp.com`
(the CNAME target) resolves to. This must be in place before the
`status-page.webingenia.com` acceptance run; record the chosen approach in
`wiki/`.

### Testing (mandatory — the feature is not done until all four layers pass)

1. **Unit**: `domainverify` CNAME-only matrix (shared + token modes, chain
   canonical-name behavior, wrong target, NXDOMAIN); records builder;
   token-host construction/length; decision-func table (own hosts, servable,
   unverified, unknown, disabled page).
2. **DB / integration (testcontainers)**: `tls_storage` Storage
   implementation in both engines — store/load/delete/list/stat round-trips,
   lock contention (two goroutines, one lock), lock expiry.
3. **ACME end-to-end (integration test)**: spin
   [Pebble](https://github.com/letsencrypt/pebble) (LE's test CA) via
   testcontainers, point `acme.ca_url` at it, and drive a real
   issuance for a test hostname resolving to the test server (Pebble's
   `-dnsserver` / challtestsrv, or hostname remapping). Assert: cert issued
   on first TLS handshake, persisted in `tls_storage`, served on the second
   handshake without re-issuance, and **denied** (handshake fails, no CA
   traffic) for an unverified domain — the negative control.
4. **Dash0 E2E (Playwright, `web/dash0/e2e/`)**: single-CNAME record row with
   copy button, verify button flow, cert-status chip states.
5. **Live acceptance on `status-page.webingenia.com`** — checklist executed
   against the k8xp deployment after the infra prerequisite lands (DNS for
   `webingenia.com` is controlled by the operator; the record to pre-create is
   `status-page.webingenia.com CNAME solidping.k8xp.com`):
   - [ ] Claim `status-page.webingenia.com` on a status page in dash0; verify
         turns green via the Verify button.
   - [ ] `https://status-page.webingenia.com/` serves the status page with a
         valid publicly-trusted certificate (first request may take a few
         seconds — on-demand issuance).
   - [ ] Cert material present in `tls_storage` (survives a pod restart —
         restart and confirm no re-issuance in logs).
   - [ ] `http://status-page.webingenia.com/` 308-redirects to https.
   - [ ] `/dash0` and non-allowlisted API paths return 404 on the custom host.
   - [ ] Clearing the domain in dash0 → host stops serving within the cache
         TTL; re-adding restores it.

### Security checklist

- Decision func is the only path to issuance; unverified/unknown hosts never
  reach the CA (protects LE rate limits and blocks cert squatting).
- Token mode documented as the takeover-safe option; shared mode's dangling-
  CNAME risk stated plainly in docs.
- Re-verify keeps unverifying after 3 failures → serving *and* renewal stop.
- `tls_storage` holds private keys: never expose via any API; exclude from
  any debug/export surfaces.
- The `allowed` endpoint stays untouched for external-proxy deployments.

### Open questions

- Which CNAME mode should the production SaaS (`solidping.io`) run — flip to
  `token` at launch, or start `shared` and migrate?
- Should `customDomainCertStatus` trigger a dashboard warning banner when
  issuance has been failing > 1 h?
- ZeroSSL/other CAs as fallback (certmagic supports CA fallback natively) —
  worth enabling from day one or keep LE-only?

## Implementation Plan

1. **Dependency** — add `github.com/caddyserver/certmagic` (pulls `mholt/acmez/v3`,
   `libdns`, `zerossl`, `miekg/dns`, `zap`).
2. **`domainverify` → CNAME-only, mode-aware** — delete `CheckTXT`,
   `ChallengeLabel`, `TXTValuePrefix`, `ChallengeName` and the `LookupTXT` hook.
   Add `Mode` (`shared` | `token`), `ParseMode`, `TokenHostLabel` (`cname`),
   `ExpectedTarget(mode, token, cnameTarget)` and `TokenHost`. `Records` returns
   one CNAME row; `Verify(ctx, domain, token, cnameTarget, mode)` compares the
   canonical CNAME against the mode's expected target only (no dual-accept).
   Keep `Normalize` and `CheckCNAME` untouched in behavior.
3. **Token shape** — `generateCustomDomainToken` shortens to a
   letter-leading, lowercase-base32, 15-char token so `<token>.cname.<target>`
   fits the 63-char DNS label cap. Existing 43-char tokens are left alone and
   regenerate on the next domain set.
4. **Config** — new `ACMEConfig` block (`acme.enabled`, `acme.email`,
   `acme.ca_url`, `acme.listen_http`, `acme.listen_https`) plus
   `server.custom_domain_cname_mode`. Multi-word keys get `applyACMEEnv` /
   `applyServerEnv` manual readers and entries in `manualReaderEnvVars()`.
   `Config.CustomDomainCNAMEMode()` resolves the mode; `Validate()` rejects
   `acme.enabled` without an email and an unknown mode.
5. **Migration `009_v0_8_0`** (postgres + sqlite) — `tls_storage`
   (`key` PK, `value` bytea/blob, `modified_at`) and `tls_storage_locks`
   (`key` PK, `owner`, `expires_at`), with a `down` that reverses LIFO.
6. **DB layer** — `models.TLSStorageEntry` / `models.TLSStorageLock` /
   `models.TLSStorageKeyInfo`; nine `TLSStorage*` methods on `db.Service`
   implemented identically for both engines (store/load/delete-with-prefix/
   exists/list/stat + acquire/refresh/release lock). Round-trip, prefix-delete,
   lock-contention and lock-expiry tests on sqlite and embedded Postgres.
7. **`internal/tlsedge`** — `Storage` (certmagic.Storage over the DB, including
   the lock lease refresher), `DecisionFunc` (instance hosts ∪
   `CustomDomainServable`), `Edge` owning the certmagic `Cache`/`Config`/
   `ACMEIssuer`, the `:80` challenge+308-redirect listener and the `:443` TLS
   listener that feeds the existing handler chain unchanged, plus
   `CertStatus(domain) → none|issued|error` backed by storage lookups and an
   `OnEvent` failure map.
8. **Server wiring** — `Server.Start` builds and starts the edge when
   `acme.enabled` and the node runs the API; shutdown drains both listeners.
   `statuspages.Service` gains a `CertStatusProvider` hook so
   `customDomainCertStatus` appears on the authed status-page response.
9. **Re-verify job** — CNAME + mode aware; 6 h cadence, 3-failure unverify and
   the never-promote rule unchanged.
10. **Frontend (dash0)** — helper copy switches to a single CNAME record, a
    cert-status chip renders next to the verified chip, and the four locale
    files are updated. `dns-record-row` and the card layout are unchanged
    otherwise (the API already drives the row count).
11. **Docs + OpenAPI + wiki** — rewrite `custom-domains.md` around
    "one CNAME + automatic HTTPS" with the external-proxy path demoted to an
    appendix; add the token-mode wildcard caveat and the shared-mode
    dangling-CNAME trade-off. OpenAPI gets `customDomainCertStatus` and updated
    descriptions. `wiki/runbooks/custom-domain-tls.md` carries the k8xp
    deployment prerequisite and the verbatim live-acceptance checklist.
12. **Tests** — `domainverify` matrix, decision-func table, token-host
    construction, storage round-trips on both engines, an ACME end-to-end
    against a Pebble container (skipped when Docker is unavailable), and the
    dash0 Playwright spec updated for one CNAME row + the cert chip.
