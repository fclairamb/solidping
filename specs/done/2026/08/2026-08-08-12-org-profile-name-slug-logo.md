---
model: opus
effort: high
---

# Owners can edit the organization profile: name, slug, and logo

## Problem

An organization's identity is frozen at creation. `POST /api/v1/orgs` sets
name + slug once, and there is no HTTP endpoint to change either afterwards —
the db layer already supports it (`OrganizationUpdate{Slug, Name}` in
`server/internal/db/models/organization.go:37-45`, `UpdateOrganization` in
`server/internal/db/service.go:74`) but nothing calls it. There is no logo at
all: no column on `organizations`
(`server/internal/db/models/organization.go:10-21`), nothing in the API,
nothing in the dashboard.

The org settings page
(`web/dash0/src/routes/orgs/$org/organization.settings.tsx`) only manages
behavioral settings (email join pattern, session duration, default escalation
policy) — the org's own name/slug/logo are not editable anywhere.

**Infrastructure check for logo upload** (the description conditions direct
upload on existing infra): the files subsystem exists and is sufficient —
`models.File`, storage backends (`handlers/filestorage/localfs`, `s3fs`),
org-scoped file routes (`server/internal/app/server.go:1061-1067`), public
unauthenticated serving at `/pub/files/:uid`
(`files/handler.go:103`), and `files.Service.CreateFile`
(`files/service.go:201`, used today by feedback screenshots). The only
missing piece is a small HTTP upload handler, so **direct upload is in
scope** alongside URL-based logos.

## Proposal

Depends on spec `2026-08-08-11` (owner role + `RequireOrgOwner` middleware);
implement after it.

### 1. `PATCH /api/v1/orgs/:org` (owner-gated)

New endpoint accepting `{name?, slug?, logoUrl?}` (all optional,
standard PATCH semantics; `logoUrl: null`/`""` clears the logo).

- **name**: non-empty validation, straightforward.
- **slug**: validate with the existing `orgSlugRegex`
  (`server/internal/handlers/auth/service.go:2623`) + availability check
  (`GetOrganizationBySlug`), same errors as CreateOrg
  (`ErrInvalidOrgSlug` / `ErrOrgSlugTaken`).
- **Slug-rename session handling** — the critical part:
  `AuthorizeOrgAccess` compares the JWT `orgSlug` claim to the URL param
  (`server/internal/handlers/auth/orgaccess.go:63`), so after a rename every
  live access token 403s on the new slug's URLs. Mirror the
  CreateOrg/SwitchOrg pattern (`auth/service.go:2673-2709`): when the slug
  changed, the response carries a fresh org-scoped session
  (`accessToken`/`refreshToken`/`expiresIn`/`tokenType`) minted for the new
  slug. Other sessions self-heal on their next refresh — verify `Refresh`
  (`auth/service.go:1123`) derives the claim's slug from the org row (by
  UID), not from the old token; fix if it doesn't.
- **logoUrl**: `http(s)` URL, reasonable max length. Stored as a new
  nullable `logo_url` text column on `organizations` (migration for both
  `server/internal/db/postgres/migrations/` and
  `server/internal/db/sqlite/migrations/`). Expose `logoUrl` in org
  responses (login/switch-org/org listings where the org object is
  serialized).

Documented consequence (no silent magic): renaming the slug changes the
canonical form of every `/orgs/:org/...` URL, including the intentionally
public ones — status-page links, SVG badges, embed widgets
(`server/internal/app/server.go:548-549`). **Old links keep working**: the
previous slug is remembered and redirects (301 for safe methods, 308 for the
rest) to the current one — see `## Resolved open questions`, which is
authoritative on this point. That guarantee lasts only until another
organization claims the freed slug, so the UI must still warn before saving.
Custom-domain status pages are host-routed and unaffected.

### 2. Logo upload: `POST /api/v1/orgs/:org/logo` (owner-gated)

Multipart upload reusing `files.Service.CreateFile` + the storage backends:

- Allowlist content types (`image/png`, `image/jpeg`, `image/webp`,
  `image/svg+xml`), enforce a small size cap (e.g. 1 MB), reject anything
  else with `VALIDATION_ERROR`.
- On success, store the file and set `logo_url` to its public URL
  (`/pub/files/:uid` — relative, so it works on any host). Replacing a
  previously uploaded logo deletes the old file row; an external URL logo
  is left alone.
- Response: the updated org profile (including the new `logoUrl`).

### 3. Dashboard (dash0)

In `organization.settings.tsx`, add an **Organization profile** card at the
top, visible/editable for owners only:

- **Name** and **slug** inputs (reuse the shipped name+slug pair primitive
  from the design reference). Slug edits show an inline warning that all org
  URLs — including public status/badge/widget URLs — will change.
- On successful slug rename: adopt the returned session tokens, then
  navigate to `/orgs/<newSlug>/organization/settings`.
- **Logo**: preview + URL input + "Upload" button (file picker → the upload
  endpoint). Clearing resets to the default.
- Display the org logo where the org identity is shown (sidebar org header /
  org switcher in `web/dash0/src/components/layout/AppSidebar.tsx`) with a
  fallback to the current default when unset.
- Update the OpenAPI spec (`server/internal/app/openapi/openapi.yaml`) and
  `wiki/api-specification/`.

### 4. Tests

- Backend: PATCH validation (bad slug, taken slug, non-owner → 403 even for
  admin), slug rename mints a working session for the new slug + old access
  token 403s, refresh-after-rename yields the new slug claim, logo upload
  happy path + oversized + wrong content-type + old-file cleanup.
- E2E (`web/dash0/e2e/`): owner renames the org and lands authenticated on
  the new URL; admin doesn't see the profile card; logo upload shows in the
  sidebar.

## Open questions

- ~~Should the old slug 404 or 410 after a rename?~~ Resolved below: neither —
  the old slug keeps resolving and redirects to the current one.

## Resolved open questions

**Q: "Should the old slug 404 or 410 after a rename? Spec assumes plain 404
(org lookup by slug simply misses), no tombstone."**

**Decision: neither — the old slug must KEEP WORKING.** This overrides the
Proposal's "No redirect from the old slug" line and its "Documented consequence"
paragraph; update that prose as part of this spec so the file stops contradicting
itself.

Implement a previous-slug store so a renamed org stays reachable at its old
slug:

- Persist previous slugs (a `previous_slugs` table, or an equivalent column,
  with migrations in **both** `server/internal/db/postgres/migrations/` and
  `server/internal/db/sqlite/migrations/`). Follow the existing numbering
  convention; never reuse or renumber an existing migration.
- Slug lookup falls back to the previous-slug store on a miss, and responds with
  a **301 to the current slug** rather than serving the old URL indefinitely.
  This must cover the public surfaces customers have already pasted elsewhere —
  status pages, SVG badges, and the `/embed/v1` widget — as well as the
  dashboard and API `/orgs/:org/...` routes.
- A previous slug is **released the moment another org claims it**: an active
  org's slug always wins over any alias, and `CreateOrg` may hand out a slug that
  is only held as an alias. Aliases must never shadow a live org, and must never
  resolve across tenants once reclaimed.
- The UI still warns before saving a rename, but the warning changes: old links
  redirect rather than break, and the old slug is only guaranteed until someone
  else claims it.

**Scope boundary:** this alias mechanism is for **renames only**. Deleted orgs
must 404 immediately with no alias and their slug freed — that is spec
`2026-08-08-11-owner-role-org-create-delete.md`. Make sure a soft-deleted org can
never resolve through this rename-alias path.

**Q: "Logo dimensions/normalization (resize server-side?)"**

**Decision: no processing beyond type/size validation for V1** — the spec's own
assumption stands. The security caveat it raises is **binding**, not optional:
verify the files handler does not serve inline-executable SVG with a sniffable
type, and set `Content-Disposition` / `X-Content-Type-Options: nosniff` if it
does. A stored SVG that executes in a victim's browser on the app's own origin
is XSS.

Note that "type validation" means the **declared** multipart content type,
clamped to an allowlist — the bytes are not sniffed. That is why the serving
headers, not the upload check, carry the security weight.

## Implementation Plan

### 1. Migration `012_org_profile` (both dialects)

`server/internal/db/{postgres,sqlite}/migrations/012_org_profile.{up,down}.sql` —
scratch migration for the in-flight v0.10.0 cycle (`wiki/conventions/database.md`),
never appended to `011_owner_role`:

- `organizations` gains `logo_url text` and `logo_file_uid` (nullable, FK to
  `files(uid) on delete set null` in Postgres; plain column in SQLite, which
  cannot `add column ... references` retroactively without a table rebuild —
  the rebuild is not worth it for a nullable pointer, so SQLite gets a plain
  column and the app enforces the link).
- new table `organization_previous_slugs (uid, organization_uid, slug,
  created_at, deleted_at)` + partial unique index on `slug where deleted_at is
  null` + index on `organization_uid`.

### 2. DB layer

- `models.Organization` gains `LogoURL *string` / `LogoFileUID *string`;
  `models.OrganizationUpdate` gains `LogoURL`, `ClearLogoURL`, `LogoFileUID`,
  `ClearLogoFileUID`.
- New `models.OrganizationPreviousSlug`.
- New `db.Service` methods (implemented in both `sqlite` and `postgres`):
  - `AddOrganizationPreviousSlug(ctx, orgUID, slug)`
  - `GetOrganizationByPreviousSlug(ctx, slug)` — joins `organizations` and
    filters `organizations.deleted_at IS NULL`, so a **soft-deleted org can
    never resolve through an alias** (the cross-spec boundary with
    `2026-08-08-11`).
  - `ReleaseOrganizationPreviousSlug(ctx, slug)` — soft-deletes every alias row
    for that slug, whichever org holds it.
  - `ReleaseOrganizationPreviousSlugsForOrg(ctx, orgUID)`
  - `GetOrganizationByLogoFileUID(ctx, fileUID)`

### 3. Slug resolution + redirect

- `auth.Service.ResolveOrgSlug` is not needed; resolution is a middleware:
  `middleware.OrgSlugRedirect` (`server/internal/middleware/orgslug.go`).
  - live org wins → pass through (an alias **never** shadows a live org);
  - miss + alias hit on a live org → `301` (GET/HEAD) / `308` (other methods)
    to the same URL with the `{org}` segment replaced by the canonical slug.
    The `{org}` segment is located through the matched chi route pattern, not
    by string matching, so a check named like the org is never rewritten.
  - miss + no alias → pass through, downstream 404s as before.
- Wired ahead of `RequireAuth`/`RequireOrgAccess` on the `orgGroup` helper (so
  the redirect happens before the token-scope 403 in
  `auth.AuthorizeOrgAccess`), on `orgOwnerGroup`, and on the public surfaces
  registered directly on `api`: per-check badge, heartbeat, magic-link ack,
  status-page subscribe, and the four public `/status-pages/:org/...` routes
  (view, summary, badge, feed) — the last of which is what the `/embed/v1`
  widget polls, so the widget follows the redirect transparently.
- The groups that cannot use the `orgGroup` helper (they need `RequireOrgAdmin`,
  or the entitlements service-signature chain) list the middleware first in
  their own chain. Coverage is enforced by a test that walks the real route
  table, so a new org-scoped group cannot silently opt out.
- SPA URLs: `app.orgSlugRedirectForSPA` handles `/status0/<org>/...` and
  `/dash0/orgs/<org>/...` in `serveStatus0Root` / `serveDash0Root`.
- The realtime WS (`/orgs/:org/events/ws`) cannot redirect (no HTTP redirect in
  a handshake) and is left alone — documented.

### 4. `PATCH /api/v1/orgs/:org` (owner-gated)

`auth.Service.UpdateOrgProfile` in `server/internal/handlers/auth/org_profile.go`:
name / slug / logoUrl, `orgSlugRegex` + availability, and on rename:
persist the old slug as an alias, release any alias on the *new* slug, and mint
a fresh org-scoped session for the new slug (mirroring CreateOrg/SwitchOrg).
`CreateOrg` releases any alias holding the slug it just claimed, and `DeleteOrg`
releases all of the deleted org's aliases.

### 5. Logo

- `POST /api/v1/orgs/:org/logo` (owner-gated, multipart `logo`): allowlist
  `image/png|jpeg|webp|svg+xml|gif`, 1 MB cap, `files.Service.CreateFile`,
  soft-delete the previous uploaded logo file.
- `DELETE /api/v1/orgs/:org/logo` clears it.
- Public serving: `GET /pub/org-logos/:uid` — unsigned but validated (the file
  must be the *current* logo of a live org), because `/pub/files/:uid` requires
  an expiring signed URL and a logo needs a stable one.
- **Security hardening (binding):** `files.writeFileContent` now always sets
  `X-Content-Type-Options: nosniff` and only serves `Content-Disposition:
  inline` for a safe raster allowlist — everything else, `image/svg+xml`
  included, is `attachment`. `<img src>` still renders an SVG logo; top-level
  navigation to it can no longer execute script on our origin.

### 6. Dashboard (dash0)

Owner-only **Organization profile** card in
`web/dash0/src/routes/orgs/$org/organization.settings.tsx` (above the existing
danger zone, untouched): name+slug pair with a rename warning stating that old
links redirect until someone else claims the slug, logo preview + URL field +
upload/clear. On rename, adopt the returned tokens and navigate to the new slug.
Org logo shown in `AppSidebar`.

### 7. Docs & tests

OpenAPI (`openapi.yaml` + regenerated `pkg/client`), `wiki/api-specification/`.
Backend tests: rename→alias 301 across API/status-page/badge/embed surfaces
(with the new slug 200 as the positive control), alias never shadows a live org,
CreateOrg claims an alias and the alias stops resolving, rename+delete → both
slugs 404, slug validation/uniqueness, owner gating, logo type/size validation
and the SVG content-type hardening. Playwright E2E for the profile card.
