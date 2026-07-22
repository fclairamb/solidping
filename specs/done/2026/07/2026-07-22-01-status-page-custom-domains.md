---
model: opus
effort: high
---

# Status pages cannot be served on a customer-owned domain

## Problem

Status pages are the only customer-facing surface of the product, and every
competitor (Better Stack, Instatus, Statuspage, UptimeRobot) lets an org serve
theirs at `status.theircompany.com`. SolidPing has no support at all: the model
has no domain field ([status_page.go:53](server/internal/db/models/status_page.go:53))
and public pages are only reachable path-based on the installation's own hosts —
`/status0/{org}` or `/status0/{org}/{slug}`
([server.go:1291](server/internal/app/server.go:1291),
[$org.$slug.tsx](web/status0/src/routes/$org.$slug.tsx)). No company points its
customers at `solidping.io/status0/...`, so the otherwise complete status-page
feature (sections, subscribers, Atom feed, badges, incident updates) is
effectively unusable for its main audience. It is also the classic paid-tier
anchor in this market.

The only host-based routing today is `handlerWithDocsHost`
([server.go:1506](server/internal/app/server.go:1506)) — installed as the
top-level handler at [server.go:2002](server/internal/app/server.go:2002) — which
302-redirects the docs host into `/docs`. Custom domains generalize that into a
DB-backed host → status-page **rewrite** (not redirect: the browser must stay on
the custom host).

## Proposal

### Scope (v1)

One custom domain per status page, DNS-verified (CNAME + TXT), served at the
root of the custom host, periodically re-verified, entitlement-gated
(`maxCustomDomains`). TLS termination stays outside the Go server (see TLS
section). Multiple domains per page, apex/ALIAS guidance beyond docs, and
automatic canonical→custom redirects are out of scope.

### Data model & migration

Add columns to `status_pages` (no new table — one domain per page):

```sql
ALTER TABLE status_pages ADD COLUMN custom_domain varchar(253);
ALTER TABLE status_pages ADD COLUMN custom_domain_token varchar(64);
ALTER TABLE status_pages ADD COLUMN custom_domain_verified_at timestamptz;
ALTER TABLE status_pages ADD COLUMN custom_domain_checked_at timestamptz;
ALTER TABLE status_pages ADD COLUMN custom_domain_failures smallint NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX status_pages_custom_domain_idx
  ON status_pages (custom_domain)
  WHERE custom_domain IS NOT NULL AND deleted_at IS NULL;
```

- The **global partial unique index is the security anchor**: it prevents one
  org from claiming another org's live domain, mirroring the existing partial
  indexes from `001_v0_1_0.up.sql:694-695`. A unique-violation on write maps to
  `409 CONFLICT`.
- Goes into the next consolidated release migration (currently
  `008_v0_7_0.up.sql` / `.down.sql`) in **both**
  `server/internal/db/postgres/migrations/` and
  `server/internal/db/sqlite/migrations/` (latest existing: `007_v0_6_0`). If
  spec `2026-07-22-02` lands in the same release, share the file.
- Model: extend `models.StatusPage`
  ([status_page.go:53](server/internal/db/models/status_page.go:53)) and
  `StatusPageUpdate` (`:92`).
- DB interface: add `GetStatusPageByCustomDomain(ctx, domain)` next to
  `GetStatusPageBySlug` in [service.go:555](server/internal/db/service.go:555),
  implemented in both engines. Returns only rows where
  `deleted_at IS NULL`.

### Domain normalization & validation (on write)

- Lowercase, strip trailing dot, convert IDN to ASCII/punycode via
  `golang.org/x/net/idna` (store the ASCII form only).
- Must be a valid hostname with ≥ 2 labels; reject IPs, wildcards, ports.
- Reject domains equal to (or a subdomain of) the instance's own hosts: the
  host of `server.base_url` ([config.go:588](server/internal/config/config.go:588)),
  `server.docs_host` (`:602`), and the CNAME target (below) — prevents
  self-shadowing.

### API

Per REST conventions (camelCase, `$uid` paths, PATCH):

- **Set/clear**: `customDomain *string` on the existing
  `UpdateStatusPageRequest` / `CreateStatusPageRequest`
  ([service.go:207-233](server/internal/handlers/statuspages/service.go:207)).
  Setting or changing the domain generates a fresh token
  (crypto/rand 32 bytes → base64url, same as `generateToken` in
  [statussubscribers/service.go:89](server/internal/handlers/statussubscribers/service.go:89)),
  clears `custom_domain_verified_at`, resets `custom_domain_failures`.
  `null` clears everything. Errors: `VALIDATION_ERROR` (bad hostname),
  `CONFLICT` (taken), `FORBIDDEN`/quota error (entitlement).
- **Read**: `StatusPageResponse`
  ([service.go:119](server/internal/handlers/statuspages/service.go:119)) gains
  `customDomain`, `customDomainStatus` (`"unverified" | "verified"`), and
  `customDomainRecords`: the two DNS records the user must create —
  `{type: "CNAME", name: "<domain>", value: "<cnameTarget>"}` and
  `{type: "TXT", name: "_solidping-challenge.<domain>", value: "sp-domain-verify=<token>"}`.
  These fields appear only on the **authenticated** org endpoints, never on the
  public view (`ViewStatusPage`,
  [service.go:885](server/internal/handlers/statuspages/service.go:885)).
- **Verify now**: `POST /api/v1/orgs/:org/status-pages/:statusPageUid/custom-domain/verify`
  — runs the DNS checks synchronously, stamps `custom_domain_verified_at` /
  `custom_domain_checked_at`, returns the updated response. Rate-limit
  (e.g. 10/min/org).
- **Edge TLS hook** (public, unauthenticated):
  `GET /api/v1/public/custom-domains/allowed?domain=` → `204` if the domain is
  verified+enabled+public, else `404`. This is the "ask" endpoint for Caddy
  `on_demand_tls` / cert-manager gating in the SaaS deployment. Constant-time
  behavior, no body — no existence leak beyond the boolean the TLS edge needs.
- OpenAPI: `server/internal/app/openapi/openapi.yaml` is **hand-maintained**
  (embedded at [server.go:139](server/internal/app/server.go:139)) — edit it
  manually for the new fields/endpoints.

### Verification mechanics

New small package `server/internal/domainverify`:

- `Verifier` with `LookupTXT` / `LookupCNAME` func fields defaulting to a plain
  `net.Resolver` (same idiom as
  [checkdns/checker.go:238-341](server/internal/checkers/checkdns/checker.go:238) —
  keep the funcs injectable for tests).
- Pass = TXT `_solidping-challenge.<domain>` contains
  `sp-domain-verify=<token>` **and** CNAME of `<domain>` resolves to the
  configured CNAME target (compare case-insensitively, trailing-dot-stripped).
  TXT alone is ownership; CNAME alone is routing; require both.

### Host resolution & serving

Extend the top-level handler chain
([server.go:2002](server/internal/app/server.go:2002)): after the docs-host
check, a custom-domain resolver:

1. Extract host via the `docsHostMatches` idiom (`net.SplitHostPort`,
   case-insensitive — [server.go:1533](server/internal/app/server.go:1533)).
   Skip if it equals base-url host / docs host / CNAME target.
2. Look up host → page through a TTL cache (30–60 s) using the generic
   `utils/cache.Cache[T]`
   ([cache.go:17](server/internal/utils/cache/cache.go:17)). **Cache negative
   results too** (sentinel value — required anyway since entries hold
   `weak.Pointer` and may vanish before TTL) so random-host scans don't hammer
   the DB. Serve only pages that are verified && `Enabled` &&
   `Visibility == "public"` (same gate as
   [service.go:898](server/internal/handlers/statuspages/service.go:898)).
3. On a hit, allowlist-route by path prefix:
   - `/` and any non-asset SPA path → serve the status0 index fallback
     (`serveStatus0Static`, [server.go:1828](server/internal/app/server.go:1828))
     with meta injection resolved **from the host** (see below).
   - `/status0/*` → static assets as normal (the SPA is built with the
     `/status0` base, so its asset URLs keep working on the custom host).
   - Public API passthrough, exactly the endpoints status0 needs:
     `/api/v1/status-pages/*` (view + `feed.xml`,
     [server.go:1164-1167](server/internal/app/server.go:1164)),
     `/api/v1/orgs/:org/status-pages/:uid/subscribers` (`:1173`),
     `/api/v1/public/status-subscribers/*` (`:1174-1176`), and badges
     (`:770-773`).
   - **Everything else — `/dash0`, `/docs`, auth, the rest of `/api` — returns
     404 on custom hosts.**
4. No hit → fall through to the normal router (unknown hosts behave exactly as
   today).

### status0 SPA bootstrap on custom hosts

The SPA resolves org/slug from the path
([status0_meta.go:53-74](server/internal/app/status0_meta.go:53)); on a custom
host the path is `/`. Mechanism:

- Server side: when the request arrived via a resolved custom host, build the
  meta from the host-resolved page instead of `statusPagePathParts`
  (`status0MetaForPath`,
  [status0_meta.go:169](server/internal/app/status0_meta.go:169)), and inject an
  extra `<meta name="sp-page" content="{org}/{slug}">` alongside the OG tags
  (`injectStatus0Meta`, `:146`). OG URLs must use the custom-host URL.
- SPA side: the root route
  ([web/status0/src/routes/index.tsx](web/status0/src/routes/index.tsx)) reads
  that meta tag on mount; when present, render the same view as
  `$org.$slug.tsx` (via `usePublicStatusPage(org, slug)`,
  [hooks.ts:91](web/status0/src/api/hooks.ts:91)) **without navigating**, so the
  address bar stays `status.acme.com/`. Subscribe/feed links must be relative
  (they resolve against the custom host and are allowlisted above).

### Periodic re-verification (takeover protection)

Self-rescheduling job, exactly the `snooze_sweep` pattern:

- `JobTypeCustomDomainVerify` + `job_custom_domain_verify.go`, registered in
  [registry.go:12-30](server/internal/jobs/jobtypes/registry.go:12), seeded once
  at startup via an `ensureCustomDomainVerifyJob` in
  [job_startup.go](server/internal/jobs/jobtypes/job_startup.go) (dedupe is
  built into `CreateJob`), self-rescheduling every 6 h like `rescheduleSelf`
  ([job_snooze_sweep.go:100-113](server/internal/jobs/jobtypes/job_snooze_sweep.go:100)).
- For every page with a non-null domain: re-run the TXT check. Success →
  stamp `checked_at`, reset `failures`. Failure → increment `failures`; at
  **3 consecutive failures clear `custom_domain_verified_at`** so the host
  stops being served (and the TLS "ask" endpoint starts answering 404). The
  dashboard shows the reverted state; re-verification via the manual endpoint
  restores it.

### TLS

- **Self-hosted**: out of scope for the Go server — document the
  reverse-proxy pattern (Caddy `on_demand_tls` with the `allowed` ask
  endpoint, or a wildcard/manual cert on the proxy). Docs page under
  `web/docs/docs/features/`.
- **SaaS (k8xp)**: terminate at the edge using the same `allowed` endpoint
  (Caddy sidecar or cert-manager). **No in-server ACME in v1** — keeps the Go
  server single-responsibility; the `allowed` endpoint is the only contract.

### Config

- `server.custom_domain_cname_target` (string). Default: empty → derive from
  the host of `server.base_url`. Because it is a multi-word koanf key it needs
  the manual env reader: extend `applyServerEnv`
  ([config.go:1186-1192](server/internal/config/config.go:1186)) to read
  `SP_SERVER_CUSTOM_DOMAIN_CNAME_TARGET` (alias
  `SP_CUSTOM_DOMAIN_CNAME_TARGET`, mirroring the docs-host dual read) **and**
  list it in `manualReaderEnvVars()`
  ([envvars.go:91](server/internal/config/envvars.go:91)).

### Entitlements

- Add `MaxCustomDomains *int json:"maxCustomDomains,omitempty"` to
  `EntitlementLimits`
  ([entitlements_payload.go:37-44](server/internal/db/models/entitlements_payload.go:37)).
  Per the comment at `:24-31` this is backward-compatible (no version bump),
  but the strict wire struct in `UnmarshalJSON` (`:60-66`) and its copy-through
  (`:78-85`) must gain the field, as must `overlayLimits`
  ([entitlements/handler.go:304-317](server/internal/handlers/entitlements/handler.go:304)).
- Defaults ([defaults.go:98-137](server/internal/entitlements/defaults.go:98)):
  SaaS `Int(0)` (billing raises per plan), self-hosted `nil` (unlimited).
- Enforcement: `CustomDomainAllowed(ctx, orgUID)` in
  [entitlements/usage.go](server/internal/entitlements/usage.go) modeled on the
  `MaxChecks` shape (`:135-165` — resolve, nil = unlimited, count
  `status_pages` rows with non-null domain in the org, `QuotaError` with
  `LimitName: "MaxCustomDomains"`). Called from the PATCH path only when
  setting a new non-null domain.

### Dashboard (dash0)

- `StatusPageForm`
  ([status-page-form.tsx](web/dash0/src/components/shared/status-page-form.tsx),
  used by
  [status-pages.$statusPageUid.edit.tsx](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.edit.tsx))
  gains a "Custom domain" section: domain input, the two DNS records with
  copy-to-clipboard, a **Verify** button, and a verified/unverified status
  chip. Start from the design reference
  ([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx));
  add a "DNS record row with copy button" pattern there if missing. Mobile
  layouts per repo rules.
- API layer is hand-written: extend the `StatusPage` /
  `CreateStatusPageRequest` interfaces and status-page hooks in
  [hooks.ts:1426-1574](web/dash0/src/api/hooks.ts:1426), add a
  `useVerifyStatusPageDomain` mutation.
- Entitlement-denied state: show the quota error with the Usage-page upgrade
  link, consistent with existing quota surfaces.
- i18n: new strings in the status-pages locale files across all four locales
  (`web/dash0/src/locales/{en,fr,de,es}/`).

### Security checklist

- Global partial unique index arbitrates ownership races → `409`.
- Serve only verified + enabled + public pages; re-verify every 6 h; unverify
  after 3 consecutive failures (domain release/transfer protection).
- Never serve dash0/auth/non-allowlisted API on custom hosts.
- Reject self-shadowing domains (base host, docs host, CNAME target).
- Punycode storage; no raw Unicode hosts in DB or headers.
- Rate-limit the verify endpoints; negative-cache unknown hosts.
- `customDomainToken` never appears on public endpoints.

### Testing

- **Host routing**: marker-handler tests alongside
  [routing_test.go:20](server/internal/app/routing_test.go:20) — cases for
  custom host → status0 index, assets, API allowlist, 404 for `/dash0`,
  unknown host → normal router, host with port, case-insensitivity.
- **Meta/bootstrap injection**: extend
  [status0_meta_test.go](server/internal/app/status0_meta_test.go) for
  host-resolved pages and the `sp-page` meta.
- **DB**: `GetStatusPageByCustomDomain` tests in both engines (postgres via
  testcontainers, `_postgres_test.go` suffix convention; sqlite inline),
  including the soft-delete filter and unique-index conflict.
- **Verification**: `domainverify` unit tests with stubbed lookup funcs
  (TXT ok/missing/wrong token, CNAME ok/missing/wrong target).
- **Handlers**: set/clear/normalize/conflict/entitlement-denied/verify-now
  table-driven tests in `statuspages`.
- **Job**: re-verify success, failure counting, unverify-at-3, reschedule.
- **Entitlements**: `CustomDomainAllowed` usage tests mirroring
  `usage_postgres_test.go`.

### Open questions

- Apex domains need ALIAS/ANAME at the DNS provider (CNAME is illegal at the
  apex) — document only, or accept A-record verification against a published
  static IP for SaaS?
- Should the canonical `/status0/{org}/{slug}` URL 301 to the custom domain
  once verified (SEO canonicalization), or serve both?
- `weak.Pointer` cache semantics: if eviction-before-TTL proves too lossy for
  negative caching, a plain `map` + mutex local to the resolver is acceptable.

## Implementation Plan

Ordered, one commit per logical step.

1. **Migration** `008_v0_7_0.up/down.sql` in postgres + sqlite: 5 `status_pages`
   columns (`custom_domain`, `custom_domain_token`, `custom_domain_verified_at`,
   `custom_domain_checked_at`, `custom_domain_failures`) + global partial unique
   index `status_pages_custom_domain_idx`. Clear statement separation, appendable
   for spec 2026-07-22-02.
2. **Model** (`status_page.go`): 5 fields on `StatusPage`; new
   `StatusPageCustomDomainUpdate` struct (whole-lifecycle writer).
3. **DB interface + both engines**: `GetStatusPageByCustomDomain`,
   `ListStatusPagesWithCustomDomain` (all orgs, for the job),
   `CountStatusPagesWithCustomDomain` (entitlements), `UpdateStatusPageCustomDomain`.
4. **domainverify** package: `Normalize` (lowercase/strip-dot/idna-ToASCII/≥2
   labels/reject IP·wildcard·port), `Verify`/`CheckTXT`/`CheckCNAME` with
   injectable `LookupTXT`/`LookupCNAME`, `Records` builder, exported constants.
5. **Config**: `Server.CustomDomainCNAMETarget` koanf key + `applyServerEnv` dual
   env read + `manualReaderEnvVars` entries + `Config.CustomDomainCNAMETarget()`
   helper (configured value or base_url host).
6. **Entitlements**: `MaxCustomDomains` on `EntitlementLimits` (+ strict wire
   struct + `overlayLimits`); defaults SaaS `Int(0)` / self-hosted nil;
   `CustomDomainAllowed(ctx, orgUID)` enforcement; `Usage.CustomDomains` count.
7. **statuspages service/handler**: `customDomain` on requests (presence-detected
   for clear-vs-omit); set/clear w/ token gen + entitlement gate; response
   enrichment (`customDomain`/`customDomainStatus`/`customDomainRecords`) on
   authed endpoints only; verify-now endpoint (per-org rate limit); public
   `custom-domains/allowed` edge endpoint (204/404).
8. **Host routing** (`server.go`): top-level custom-domain wrapper before the
   docs-host wrapper; dedicated TTL cache (map+mutex, negative-cached) — chosen
   over `utils/cache.Cache[T]` because its `weak.Pointer` makes negative sentinels
   unreliable (spec open-question escape hatch); allowlist-by-path-prefix serving;
   `sp-page` meta injection from the host-resolved page.
9. **status0 SPA** (`index.tsx`): read `<meta name="sp-page">` and render the
   status page in place (no navigation).
10. **Re-verify job** (`job_custom_domain_verify.go`): registry + startup seed +
    JobType; every 6h re-run TXT per domain; unverify after 3 consecutive
    failures.
11. **OpenAPI**: new fields + endpoints in `openapi.yaml`.
12. **Dashboard**: `StatusPageCustomDomain` component (domain input, DNS records
    w/ copy, Verify button, status chip, quota-denied state) in the edit route;
    hooks + interfaces; i18n across en/fr/de/es; design-reference DNS-row pattern.
13. **Docs**: `web/docs/docs/features/custom-domains.md` (Caddy on_demand_tls +
    SaaS edge TLS; no in-server ACME).
14. **Tests**: domainverify unit, DB (both engines), handler/service table tests,
    host-routing decision tests, meta injection, job, entitlements.
