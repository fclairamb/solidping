---
model: opus
effort: high
---

# Status pages cannot be visually customized (custom CSS + live preview)

## Problem

Status pages ship with a single built-in look. Organizations putting a status
page on their own (custom) domain want it to match their brand, and today there
is no appearance control at all — no logo, no colors, no CSS. Meanwhile the
status0 frontend is already fully themed through plain CSS custom properties
(`--brand`, `--background`, `--card`, `--border`, status colors, `.dark`
variant — `web/status0/src/index.css`), so a per-page CSS override is the
cheapest possible full re-skinning surface: it needs zero component changes and
doubles as a power-user escape hatch.

Separately, the repo policy is **one consolidated migration file per release**
(`server/CLAUDE.md`), but unreleased schema changes are currently split across
`008_v0_7_0.up.sql` and `009_v0_8_0.up.sql` while the latest tag is `v0.6.2`.
Everything unreleased must be re-consolidated into the single v0.7.0 pair
before this spec adds its own column.

## Proposal

Add a nullable `customCss` text field to status pages, editable by org members
from a dedicated dash0 "Appearance" route with a **live preview** (iframe of
the real status page + `postMessage`), applied on the public page on both
`/status0/{org}/{slug}` and custom-domain hosts. **Free feature — no
entitlement gating** (unlike custom domains).

### Migration consolidation (prerequisite, own commit)

Latest tag is `v0.6.2`, so `008_v0_7_0` is the first migration not in any tag.

- Fold the `009_v0_8_0` content (the ACME/TLS storage blocks from spec
  2026-07-26-01) into `008_v0_7_0.up.sql` / `008_v0_7_0.down.sql` as appended,
  clearly-separated blocks — in **both** dialects
  (`server/internal/db/postgres/migrations/`,
  `server/internal/db/sqlite/migrations/`), preserving the block comments and
  spec references. Delete the four `009_v0_8_0.*` files and move the
  `-- (append further blocks below this line)` trailer to the end of 008.
- Add this spec's `custom_css` block to the same consolidated `008_v0_7_0`
  files (see below).
- Update any code references to migration `009` if they exist (grep).
- **Dev-DB desync warning**: bun tracks applied migrations per file, so any dev
  database that already ran 008 and/or 009 will silently skip the reshuffled
  content and later crash on the missing column/tables. This is a known
  landmine (it has bitten before). The spec's Testing section must verify from
  a **fresh** database, and the final report must tell the operator to reset
  dev DBs (fresh docker volume / `SP_DB_RESET`) rather than re-migrate.

### Data model & migration

- `status_pages.custom_css text` (nullable, no default). Postgres gets a
  `comment on column`; sqlite a trailing `--` comment, matching house style.
- `CustomCSS` on the `StatusPage` model
  (`server/internal/db/models/status_page.go:52`) and `*string` on
  `StatusPageUpdate` (`:108`).
- One `if update.CustomCSS != nil` branch in each `UpdateStatusPage`
  (`server/internal/db/sqlite/sqlite.go:3810` and the postgres twin).

### API

- `CreateStatusPageRequest` / `UpdateStatusPageRequest`
  (`server/internal/handlers/statuspages/service.go:254,272`) gain optional
  `customCss`.
- Validation on write (`VALIDATION_ERROR`): max **64 KB**; reject any
  case-insensitive `@import` occurrence (prevents chained third-party
  stylesheet loading). External `url()` (fonts, background images) is
  deliberately allowed — page admins are trusted for their own public page.
- `StatusPageResponse` (`service.go:155`) gains `customCss` — and unlike the
  custom-domain fields (auth-only enrichment), it **must be present on the
  public responses** (`ViewStatusPage`/`ViewDefaultStatusPage`,
  `handler.go:419,432`), since the public page is the consumer.
- OpenAPI: add the property to `StatusPage`, `CreateStatusPageRequest`,
  `UpdateStatusPageRequest` (`server/internal/app/openapi/openapi.yaml:7529+`),
  then `make generate` and thread through the generated client, the CLI flag
  set (`server/pkg/cli/statuspages.go`) and the MCP status-page tools
  (`server/internal/mcp/tools_statuspages.go`).

### status0 rendering

- Add `customCss?: string` to the hand-written `StatusPage` interface in
  `web/status0/src/api/hooks.ts`.
- `StatusPageView` (`web/status0/src/components/shared/status-page-view.tsx`)
  renders `<style>{page.customCss}</style>` when set — as a React **text
  child**, never `dangerouslySetInnerHTML`, so `</style>` breakout is
  structurally impossible. Because status0 paints everything from the API
  payload anyway, the CSS lands atomically with the content (no FOUC), and the
  existing 30 s `refetchInterval` picks up saves. Works unchanged on
  custom-domain hosts.
- **No server-side HTML injection**: `injectStatus0Meta`
  (`server/internal/app/status0_meta.go:156`) is untouched, preserving the
  byte-identical-generic-head property for disabled/private pages.
- **Preview mode**: when the route is loaded with `?preview=1`, the page
  installs a `message` listener that accepts only
  `event.origin === window.location.origin` and messages shaped
  `{type: 'sp:preview-css', css: string}`, and applies the draft CSS in place
  of the fetched value. Without the param, no listener is installed.

### dash0 appearance editor with live preview

- New dedicated route `/orgs/$org/status-pages/$statusPageUid/appearance`
  (editing on a dedicated route per convention — never a modal), linked from
  the status page detail/edit screens alongside the existing custom-domain and
  subscribers cards (`status-pages.$statusPageUid.edit.tsx:81-83` pattern).
- Layout: monospace CSS textarea + live preview `<iframe
  src="/status0/{org}/{slug}?preview=1">` side-by-side; stacked on mobile
  (all pages must be fully usable on mobile). Same origin, no CSP /
  `frame-ancestors` exists, so the iframe just works.
- On (debounced ~300 ms) edit, `postMessage` the draft to the iframe — truly
  live, no server round-trip, pixel-exact because it *is* the production
  renderer. Save issues the normal `PATCH`; an empty textarea clears the field.
- Ship a commented starter template in the empty state listing the supported
  variables (`--brand`, `--background`, `--foreground`, `--card`, `--border`,
  status colors, `.dark`) as the documented theming API.
- Add `customCss` to the dash0 `StatusPage` type (`web/dash0/src/api/hooks.ts:1463`).
- Start from `web/dash0/src/routes/orgs/$org/design-reference.tsx` (mandatory);
  if a "code textarea" primitive is introduced, add it to the reference page.
- Route-level copy goes through `useTranslation("statusPages")` like sibling
  routes.

### Security checklist

- Custom CSS must never be applied to dash0's own chrome — only inside the
  preview iframe and on status0 pages.
- Style injection only via React text child (no `dangerouslySetInnerHTML`
  anywhere; enforce in review).
- Preview `message` handler: same-origin check + shape check + only armed with
  `?preview=1`.
- Server-side: 64 KB cap, `@import` rejected, stored verbatim otherwise (CSS is
  org-admin content scoped to that org's own public page — same trust model as
  Statuspage/Instatus).
- No change to the disabled/private-page generic-head behavior.

### Docs

- `web/docs/docs/features/status-pages.md`: new "Custom CSS" section — how to
  use it, the supported CSS variables table, the `@import`/size limits, and a
  small example (brand color + dark background).

### Testing

- **Go** (table-driven, testify/require, both dialects): create/update with
  `customCss` round-trips; oversize and `@import` payloads → `VALIDATION_ERROR`;
  clearing works; **public** view responses include `customCss`; migrations
  apply cleanly on a **fresh** database (consolidation check — postgres via
  testcontainers, sqlite in-memory).
- **Playwright** (`web/dash0/e2e/`): open appearance editor → type CSS setting
  an obvious property (e.g. `--brand`) → assert the style is applied *inside
  the preview iframe* without saving; save → public status page reflects it;
  wrong-shaped/foreign message is ignored (no style applied).
- Lint/build green: `make lint`, `make test`, dash0 scoped to no *new* eslint
  errors (pre-existing debt stays).

### Out of scope

- Structured branding controls (logo upload, color pickers writing variables),
  per-page dark/light forcing, CSP introduction on status0 — all possible
  follow-ups, not v1.

## Implementation Plan

1. Consolidate `009_v0_8_0` into `008_v0_7_0` (both dialects, up + down),
   delete 009 files — own commit.
2. Add `custom_css` column block to the consolidated 008 + model/update
   plumbing + Go persistence tests.
3. API: DTOs, validation, public response, OpenAPI + `make generate`, CLI/MCP
   threading + handler/service tests.
4. status0: type + `<style>` rendering + preview-mode message listener.
5. dash0: appearance route with editor + live iframe preview, design-reference
   addition if needed, i18n.
6. Docs page section + Playwright e2e + full `make lint` / `make test` /
   `make test-dash` pass.
