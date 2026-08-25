---
model: opus
effort: high
---

# Status pages can't be branded, shared privately, or subscribed to beyond email

## Problem

Three holes that show up on every buyer's comparison checklist (Instatus,
Hyperping, BetterStack all ship all three), unchanged since the July roadmap:

1. **No logo/favicon upload, no white-label.** Theming is CSS-variables only.
   The brand bar in
   [status-page-view.tsx:363](web/status0/src/components/shared/status-page-view.tsx:363)
   renders the SolidPing `<Logo>` and a "powered by SolidPing" footer (i18n key
   `poweredBy` in `web/status0/src/locales/*/status.json`). The `StatusPage`
   model ([status_page.go](server/internal/db/models/status_page.go)) has no
   logo or favicon fields. Removing the badge (white-label) is a natural
   paid-tier entitlement for the SaaS.
2. **No private sharing.** `visibility` is `public`/`private`
   (default `'public'`, [status_page.go:59](server/internal/db/models/status_page.go:59)),
   and `private` simply 404s the public endpoint — there is no way to share a
   status page with customers behind a password.
3. **Subscriptions are email + Atom only.** Double-opt-in email and the Atom
   feed live in
   [statussubscribers](server/internal/handlers/statussubscribers/handler.go);
   webhook and Slack subscriptions are the most-requested next channels.

The building blocks all exist now: file storage with an S3 backend
([filestorage](server/internal/handlers/filestorage/filestorage.go) +
`s3fs`), an owner-only public-logo upload pattern
([orglogo/service.go](server/internal/handlers/orglogo/service.go) —
public path prefix + file UID), and per-org entitlements
([entitlements/defaults.go](server/internal/entitlements/defaults.go)).
This is assembly, not invention.

## Proposal

Four deliverables, roughly independent — implement in this order so each lands
usable on its own:

### 1. Logo & favicon upload

- Add nullable `logo_file_uid` / `favicon_file_uid` to `status_pages`
  (+ migration), mirrored as `logoUrl`/`faviconUrl` (public paths) in API
  responses.
- Upload/delete endpoints under `/api/v1/orgs/:org/status-pages/:uid/logo`
  and `/favicon`, following the `orglogo` service pattern (size/type
  validation, public path prefix + file UID, cleanup of the replaced file).
- status0: render the uploaded logo in the brand bar instead of the SolidPing
  `<Logo>` when set; emit `<link rel="icon">` for the favicon (and keep it on
  custom domains).
- dash0: settings section on the status-page edit route with upload +
  preview + remove (start from the design reference; delete is `Trash2` red).
- Config-as-code: export/import must round-trip these fields sanely (likely
  export the public URL as informational, skip on import with a warning —
  binary blobs don't belong in YAML; note the decision in the spec's
  implementation plan).

### 2. White-label entitlement

- New boolean `whiteLabel` in `org_entitlements` (decode + defaults in
  [entitlements/defaults.go](server/internal/entitlements/defaults.go)):
  default **true** for self-hosted deployments, **false** for the SaaS tier
  defaults, so paying is what unlocks it on the SaaS.
- When the org is entitled *and* the page opts in (`hideBranding` on the
  status page), status0 drops the "powered by SolidPing" footer.
- Surface the flag on the org Usage page like the other entitlements.

### 3. Password-protected pages

- Extend `visibility` with a `password` value (keep `private` = fully hidden,
  404). Store a bcrypt hash in a new column; the API never returns it —
  write-only field `password` on create/update, `hasPassword` on read.
- Public read endpoints (page, updates, incidents, Atom feed, subscribe)
  return `401` for `password` pages without a valid unlock; add
  `POST /public/status-pages/:slug/unlock` which checks the password and sets
  a signed, HTTP-only, page-scoped cookie (works on custom domains — the
  cookie must be issued for the serving host, not solidping.io).
- status0: unlock form (single password field) shown on 401; wrong password
  stays on the form with an error. Rate-limit unlock attempts.
- Subscribers of a password page still receive email notifications — gate
  only the *page views*, not already-confirmed subscriptions; the subscribe
  form itself sits behind the unlock.

### 4. Webhook + Slack subscriptions

- Extend the status-subscriber model with a channel type:
  `email` (today), `webhook` (target URL + optional signing secret), `slack`
  (incoming-webhook URL). Slug the fan-out through
  [statussubscribers/notifier.go](server/internal/handlers/statussubscribers/notifier.go).
- Webhook deliveries carry an HMAC signature header (reuse the
  `internal/servicesig` scheme) and get disabled after sustained delivery
  failure (with an event, so the operator can see why it stopped).
- Treat endpoint URLs with the same opacity as subscriber emails —
  credentials-encryption at rest, masked in list responses.
- Public subscribe UX: webhook/Slack subscription management is
  **operator-side** (dash0, per status page), not public self-serve — a
  random visitor pasting an incoming-webhook URL has no verification story.
  Public self-serve stays email-only for now; note this explicitly in the UI.

Cross-cutting: i18n for all four locales, mobile-usable forms, REST
conventions (camelCase, `data` envelope, `$uid` paths), API-spec pages under
`wiki/api-specification/`, and docs-site updates for status pages.

## Implementation Plan

All schema work lands as new **SECTIONs of the open `015_v0_18_0`** migration
(Postgres + SQLite), per `wiki/conventions/database.md` — `014_v0_17_0` is
released and untouchable.

### Step 1 — Logo & favicon upload

- Migration SECTION `status-page-branding`: `status_pages.logo_file_uid`,
  `status_pages.favicon_file_uid` (both nullable text), plus the partial
  indexes used to authorize the unsigned public route.
- `models.StatusPage` gains `LogoFileUID` / `FaviconFileUID`;
  `StatusPageBrandingUpdate` is a dedicated whole-column writer (mirroring
  `StatusPageCustomDomainUpdate`) so set/clear is one shape.
- `db.Service`: `UpdateStatusPageBranding`, `GetStatusPageByAssetFileUID`
  (Postgres + SQLite).
- New `filestorage.GroupTypeStatusPageAssets = "status-page-assets"` — the
  world-readable blobs stay in their own storage group, same reasoning as
  `org-logos`.
- New package `internal/handlers/statuspageassets`, a near-copy of the
  `orglogo` pattern: MIME allowlist, 1 MB cap enforced by
  `http.MaxBytesReader` **before** parsing, replaced-file retirement,
  state-based authorization on the unsigned public route
  (`/pub/status-page-assets/:uid` only serves a file that is the CURRENT logo
  or favicon of a LIVE, enabled status page).
- Routes (admin, under the existing org status-page group):
  `POST|DELETE /api/v1/orgs/:org/status-pages/:statusPageUid/logo` and
  `.../favicon`.
- `StatusPageResponse` gains `logoUrl` / `faviconUrl` (public paths) — set on
  BOTH admin and public payloads; they are brand assets, deliberately public.
- status0: brand bar renders `<img src={page.logoUrl}>` instead of `<Logo>`
  when set (keeping the `sp-logo` custom-CSS hook); `faviconUrl` is written
  into a `<link rel="icon">` from the view component so it also applies on
  custom domains.
- dash0: `StatusPageBranding` card on the edit route — upload + preview +
  remove (`Trash2`, destructive red), mobile-first layout.

**Config-as-code decision (the spec asks for it explicitly):** SolidPing's
config-as-code (`internal/handlers/checks/apply.go`, the
`solidping.io/managed` scope) covers **checks only** — status pages have never
been exported or applied through it. There is therefore nothing to round-trip
and no importer to teach about binary blobs. The decision stands as the spec
anticipated: **binary assets are never embedded in YAML**; if status pages are
ever added to config-as-code, `logoUrl`/`faviconUrl` are exported as
informational read-only URLs and ignored on import with a warning. No code in
this spec.

### Step 2 — White-label entitlement

- `models.EntitlementLimits.WhiteLabel *bool` (`json:"whiteLabel"`), accepted
  by the strict PUT decoder. nil = "use the deployment default".
- `entitlements.DefaultsFor`: **true** self-hosted, **false** SaaS — paying is
  what unlocks it on the SaaS.
- Migration SECTION `status-page-branding` also adds
  `status_pages.hide_branding boolean not null default false` (the page-level
  opt-in) — API field `hideBranding` on create/update/read.
- Effective rule: the "powered by SolidPing" footer disappears only when
  `entitlement.whiteLabel && page.hideBranding`. The **public** payload
  therefore carries the RESOLVED boolean (already ANDed server-side) so status0
  never has to know about entitlements; the **admin** payload carries the raw
  opt-in plus `whiteLabelAllowed` so the dash0 toggle can explain itself.
- dash0: toggle in the status-page form (disabled + explanatory note when the
  org is not entitled), and a boolean feature row on
  `/orgs/$org/organization/usage`.

### Step 3 — Password-protected pages

- `visibility` gains a third value `password`; `private` keeps its meaning
  (fully hidden, 404). Migration SECTION adds
  `status_pages.password_hash text`.
- Write-only `password` on create/update (bcrypt, cost from the shared hashing
  config); `hasPassword` on read. The hash is NEVER serialized.
- `POST /api/v1/status-pages/:org/:slug/unlock` (public, sibling of the other
  `/status-pages/:org/:slug/*` routes) verifies the password and sets a
  **host-only** (no `Domain` attribute → the serving host, custom domain
  included), `HttpOnly`, `SameSite=Lax`, `Secure`-when-https cookie
  `sp_unlock_<pageUID>`.
  - The cookie value is `<expUnix>.<base64 HMAC>` where the HMAC key is
    `sha256(page.password_hash)`. No new server secret is needed, and
    **changing the password invalidates every outstanding cookie for free**.
  - Unlock attempts are rate-limited per (IP, page) in-process.
- Gated public endpoints: page view, default page view, summary, badge,
  incidents, `feed.xml`, and subscribe — all return **401
  `STATUS_PAGE_LOCKED`** for a `password` page without a valid cookie.
- Already-confirmed subscribers keep receiving mail: the notifier never
  consults visibility, and nothing in this step changes it. Only the
  *subscribe form* moves behind the unlock.
- status0: a 401 with that code renders an unlock form (single password field);
  a wrong password stays on the form with an inline error.

### Step 4 — Webhook + Slack subscriptions

- Migration SECTION `status-subscriber-channels`: `status_page_subscriber`
  gains `channel` (`email` | `webhook` | `slack`, default `email`),
  `endpoint_url_private` (the AES-GCM credentials envelope),
  `endpoint_hint` (the masked, safe-to-show remnant), `failure_count`,
  `disabled_at`. `email` becomes nullable — a webhook subscriber has none.
- Endpoint URLs are treated exactly like subscriber emails: encrypted at rest
  through `internal/crypto/credentials` (per-org DEK) and **only** the masked
  hint is ever returned by the list endpoint.
- Management is **operator-side only**: `POST /api/v1/orgs/:org/status-pages/
  :statusPageUid/subscribers` (authenticated) creates a webhook/slack
  subscriber, already confirmed — a random visitor pasting an incoming-webhook
  URL has no verification story. The public subscribe endpoint stays
  email-only and rejects a `channel` other than `email`; the status0 subscribe
  widget says so.
- Fan-out (`notifier.go`) dispatches per channel:
  - `email` — unchanged.
  - `slack` — Block Kit-ish JSON POST to the incoming-webhook URL.
  - `webhook` — JSON POST carrying the `internal/servicesig` HMAC headers
    (`X-SP-Signature: v1,<b64>` / `X-SP-Timestamp` / `X-SP-Key-Id`), signed
    with the subscriber's own signing secret.
- Delivery failures increment `failure_count`; at the threshold the subscriber
  is disabled (`disabled_at`) and a `statuspage.subscriber.disabled` event is
  recorded so an operator can see WHY it stopped. A success resets the counter.
- dash0: the existing subscribers card gains a channel column, a masked
  endpoint, a disabled badge, and an "add webhook/Slack subscriber" form.

### Cross-cutting

- i18n: en/fr/de/es for every new string in both status0 and dash0.
- `wiki/api-specification/status-pages.md` + `.../status-subscribers.md`
  document every new endpoint and field; `web/docs` status-pages page gains
  branding / password / webhook sections.
