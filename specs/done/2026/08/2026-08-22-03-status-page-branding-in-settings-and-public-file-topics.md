---
model: opus
effort: high
---

# Branding got three new columns and a bespoke access check, when `settings` and file topics already exist

## Problem

Spec `2026-08-21-07` shipped status-page branding by adding **three dedicated
columns and two partial indexes** to `status_pages`, and by authorizing the
public asset route with a **feature-specific state query**. Both are the second
implementation of something the schema already has.

### 1. Customization knobs became columns

The `status-page-branding` section of the unreleased consolidated migration
([`015_v0_18_0.up.sql:92`](server/internal/db/postgres/migrations/015_v0_18_0.up.sql#L92),
mirrored at [sqlite `:54`](server/internal/db/sqlite/migrations/015_v0_18_0.up.sql#L54))
adds:

```sql
alter table status_pages add column logo_file_uid text;
alter table status_pages add column favicon_file_uid text;
alter table status_pages add column hide_branding boolean not null default false;
create index status_pages_logo_file_idx    on status_pages (logo_file_uid)    where …;
create index status_pages_favicon_file_idx on status_pages (favicon_file_uid) where …;
```

But `status_pages` has carried a **`settings jsonb not null default '{}'`**
column since `009_v0_8_0`, typed rather than free-form as
[`StatusPageSettings`](server/internal/db/models/status_page_settings.go#L31),
whose stated purpose is *"per-page display customization … typed rather than a
free-form map so keys stay discoverable"*. It currently holds exactly one
section, `availability`. Branding is per-page display customization by any
reading of that sentence, and it did not go there.

The cost is paid per knob, not once: a column, an index, a bun tag
([`status_page.go:134`](server/internal/db/models/status_page.go#L134)), a
field on `StatusPageUpdate`, a `Set(...)` branch in **each** dialect
([postgres `:4612`](server/internal/db/postgres/postgres.go#L4612),
[sqlite `:4566`](server/internal/db/sqlite/sqlite.go#L4566)), and a migration
section in both. The next customization knob pays it again.

> **Correction to the premise.** CSS customization is *not* in `settings` — it is
> `status_pages.custom_css`, its own column, added in `008_v0_7_0` and therefore
> **released**. The instinct is right (one home for customization) but the
> existing example of it is `settings`/`availability`, not `custom_css`. See
> Proposal § *What stays a column* for what that means for `custom_css`.

### 2. Public-asset access control is a per-feature state query

`/pub/status-page-assets/:uid` is unsigned, and its entire authorization rule is
"this file is the **current** logo or favicon of a **live, enabled** status
page", implemented as a dedicated DB method,
[`GetStatusPageByAssetFileUID`](server/internal/db/service.go#L816), consumed by
[`OpenAsset`](server/internal/handlers/statuspageassets/service.go#L219). The
two partial indexes above exist only to make that query cheap.

This is the second copy of the pattern — `/pub/org-logos/`
([`orglogo/service.go:53`](server/internal/handlers/orglogo/service.go#L53))
has its own — and a third is due with every future publicly-served asset kind.

Meanwhile spec `2026-08-21-01` landed **`files.topic`**, a path-like attachment
key `<entity>/<uid>/<kind>` with an exact-match index and a prefix-reap path
([`WithTopic`](server/internal/handlers/files/service.go#L265),
[`DeleteAttachments`](server/internal/handlers/files/service.go#L354)). Status-page
assets are attachments of a status page and **do not set a topic at all**:
[`Upload`](server/internal/handlers/statuspageassets/service.go#L175) calls
`CreateFile` with no `WithTopic`, so the link between page and blob lives
exclusively in the two columns.

## Proposal

Both halves are one change, because they are load-bearing for each other:
branding can only move into an unindexed jsonb column once the access check
stops needing an indexed lookup on those columns.

**The HTTP contract does not change.** `logoUrl`, `faviconUrl` and
`hideBranding` keep their exact JSON shape on every status-page payload
([`statuspages/service.go:552`](server/internal/handlers/statuspages/service.go#L552),
[`statuspageassets/handler.go:38`](server/internal/handlers/statuspageassets/handler.go#L38)),
so dash0 (`status-page-branding.tsx`, `status-page-form.tsx`), status0 and the
OpenAPI schema are untouched except where the spec says otherwise. This is a
storage-and-authorization refactor, not a feature change.

### 1. Branding moves into `settings`

```go
type StatusPageSettings struct {
    Availability *AvailabilitySettings `json:"availability,omitempty"`
    Branding     *BrandingSettings     `json:"branding,omitempty"`
}

// BrandingSettings is the page's brand identity. File UIDs, not URLs — the
// public URL is derived (PublicURL), so the stored value stays valid when the
// route changes.
type BrandingSettings struct {
    LogoFileUID    *string `json:"logoFileUid,omitempty"`
    FaviconFileUID *string `json:"faviconFileUid,omitempty"`
    HideBranding   bool    `json:"hideBranding,omitempty"`
}
```

Drop `logo_file_uid`, `favicon_file_uid`, `hide_branding` and their two indexes
from the `status-page-branding` section of `015`, and drop the matching block
from `015_v0_18_0.down.sql` — **edit `015` in place rather than adding a `016`
that drops them.** `015` is unreleased (absent from the `v0.17.0` tag) and the
repo convention is one consolidated migration per cycle; shipping
add-then-drop in the same release is noise in the permanent record.

The section banner is a **machine-readable test anchor** — `migrationSection(t,
…)` slices it out in
[`sqlite/status_page_branding_migration_test.go`](server/internal/db/sqlite/status_page_branding_migration_test.go)
and its postgres twin. Keep the banner and its section (the password and
subscriber-channel work still lives under neighbouring banners); only its DDL
changes. Renaming or deleting a banner breaks a fixture.

> **A developer database that already ran `015` keeps three orphan columns** and
> loses any logo uploaded on it — the columns are no longer read, and nothing
> backfills them into `settings`. That is the known trap from the migration
> consolidation incident, and it is benign here only because the edit
> *removes* DDL: nothing new is required, so no boot-time failure follows. Reset
> the dev DB (`SP_DB_RESET`) or drop the columns by hand. **Verify both paths
> boot**: a fresh database, and a database that already applied the old `015`.

`UpdateStatusPageBranding` stays as the single write shape but becomes a
**jsonb merge in SQL, not a read-modify-write in Go**:

- Postgres: `set settings = settings || $1::jsonb`
- SQLite: `set settings = json_patch(settings, ?)`

Reading `StatusPageSettings`, mutating `.Branding` and writing the struct back
would clobber a concurrent `availability` threshold change — the classic
last-write-wins jsonb bug, and the single most likely way this refactor
introduces a regression. The merge keeps the write scoped to one key.

Clearing an asset patches the key to JSON `null`. The two dialects disagree on
what that means physically — `json_patch` **removes** the key, `||` **stores**
`null` — and agree on what it decodes to (`*string` nil). Pin that with a
parity test rather than trusting it.

### 2. Public access is granted by file topic, not by a state query

Set a topic on upload, using the existing convention:

```
status-pages/<pageUid>/logo
status-pages/<pageUid>/favicon
```

Then authorize the public route from the **file's own topic**, with one owner
for the rule — a new `internal/handlers/files/publictopics.go`:

```go
// IsPublicTopic reports whether an attachment topic is served unsigned.
// Closed allowlist: unknown entity, unknown kind, malformed or empty topic
// all deny. Everything not named here is private, including every future
// attachment kind, which is the point.
func IsPublicTopic(topic string) bool
```

allowing exactly `status-pages/<uid>/{logo,favicon}` and (see Open question 1)
`organizations/<uid>/logo`. Add the org-agnostic read the route needs —
`GetPublicFile(ctx, uid)`, live rows only — since today's `GetFile` requires an
org UID the public caller does not have, which is *why* the page lookup came
first.

`GetStatusPageByAssetFileUID`, its two indexes and `OpenAsset`'s page hop then
delete.

#### This loosens authorization, deliberately and narrowly

| | Today | After |
|---|---|---|
| Live, enabled page | served | served |
| Asset replaced or cleared | 404 (file soft-deleted) | 404 — **only because** the topic check is live-rows-only |
| Page **disabled** | 404 | **served** |
| Page `private` / `password` | 404 | **served** |
| Page deleted | 404 | 404 — **only if** the delete reaps the topic prefix |

Two of those rows are behaviour changes and both are acceptable, for reasons
that must be written down rather than assumed:

- the URL embeds an unguessable UUIDv4, so it is capability-like, not enumerable;
- a brand logo is not a secret — on a `password` page it is the image shown
  *above* the password prompt anyway. What must never leak is org-operational
  evidence, and none of it is allowlisted.

Two obligations follow, and neither is optional:

1. **`deleted_at is null` in the topic check.** `retireFile`
   ([`service.go:267`](server/internal/handlers/statuspageassets/service.go#L267))
   soft-deletes the replaced blob; that soft delete is now the *entire*
   un-publish mechanism for replace and clear.
2. **Deleting a status page must reap `status-pages/<uid>/`** through
   `DeleteAttachments` (the prefix path exists for exactly this). Without it a
   deleted page's logo stays public forever — today the page row disappearing
   handles it implicitly.

State the disabled-page change in the docs-site status-page page: an operator
who disables a page to hide it should not have to discover from a support
thread that the logo URL still resolves.

### What stays a column

Write the rule down in `wiki/conventions/database.md`, because this spec exists
only because it was unwritten:

> A per-page knob read **only while rendering the page** belongs in `settings`.
> A field that is **filtered, joined, uniquely constrained, resolved by an
> external lookup, or is a credential** belongs in a column.

By that rule these stay exactly where they are: `visibility` and `enabled`
(filtered), `custom_domain*` (globally unique, resolved by Host on every
request), `password_hash` (a credential that must never be serialized, read on
the auth path).

**`custom_css` also stays**, despite being the field that prompted this spec. It
is released, it is functionally identical whether it lives in a column or a
key, and moving it means a backfill migration over released rows for zero
behaviour change. The one-home argument governs *new* knobs; retrofitting it is
Open question 2.

### Testing

- **Schema guard**: a fresh database has **no** `logo_file_uid`,
  `favicon_file_uid` or `hide_branding` column on `status_pages` — the
  assertion that stops the columns creeping back, in both dialects.
- **The clobber test** (the negative control that matters most): a page with
  `availability` thresholds set, then a branding update; assert the thresholds
  survive. Then the reverse. Run it against both dialects — the merge is
  written differently in each.
- Parity: clearing a logo decodes to nil on Postgres and SQLite alike, whatever
  each stores physically.
- **Allowlist is closed** (table-driven): `status-pages/<uid>/logo` and
  `/favicon` allow; `incidents/<uid>/screenshot` denies; unknown kind denies;
  unknown entity denies; empty, whitespace, trailing-slash, extra-segment and
  `status-pages/<uid>/logo/../../incidents/x` all deny.
- Public route: a live allowlisted file serves with `nosniff` via
  `files.WriteContent`; a replaced file 404s; a cleared file 404s; a file
  belonging to a **deleted** page 404s (this is the reap assertion — it fails
  today if the reap is forgotten, which is the point of writing it).
- The disabled-page and private-page cases assert the **new** behaviour
  explicitly, so the loosening is recorded as a decision in a test rather than
  discovered later as a leak.
- API contract: `logoUrl` / `faviconUrl` / `hideBranding` are byte-identical to
  before on the org-facing and public payloads — a golden-response test is
  enough, and it is what proves this is a refactor.
- Existing `status_page_branding_migration_test.go` (both dialects) updated for
  the new section body, not deleted.
- Playwright: upload a logo, see it on status0, clear it, see it gone.

## Open questions

1. **Does `/pub/org-logos/` fold into the same route?** Its authorization is the
   same pattern and would become one `IsPublicTopic` case, but
   `organizations.logo_file_uid` is a **released** column and stays regardless.
   Recommend: allowlist `organizations/<uid>/logo` and set the topic on org-logo
   upload in this change (it is a two-line addition and prevents the third copy
   of the pattern), but keep the `/pub/org-logos/` path alive as-is — existing
   pages hold that URL.
2. **Does `custom_css` ever move into `settings`?** Only worth it bundled with
   another change that already touches those rows. Deliberately not now.
3. **Do the subscriber/subscription toggles from `2026-08-21-07` belong in
   `settings` too?** Not re-litigated here — this spec covers branding and the
   public-asset check only.

## Resolved open questions

1. **Does `/pub/org-logos/` fold into the same route?**
   **Decision: yes — migrate it fully to the new logic; do not keep a legacy
   path alive.** There is no existing customer holding a `/pub/org-logos/` URL,
   so the "existing pages hold that URL" caveat in the question does not apply
   and back-compat is not a constraint here. Concretely:
   - `IsPublicTopic` allowlists `organizations/<uid>/logo` alongside
     `status-pages/<uid>/{logo,favicon}`.
   - Org-logo upload sets the topic, the same way status-page branding does.
   - Serve org logos through the **same** public file route, and **retire the
     bespoke `/pub/org-logos/` handler and its access check** rather than
     leaving a second copy of the pattern in the tree. Update every in-repo
     reference (dash0, status0, docs, tests) to the new URL.
   - `organizations.logo_file_uid` is a released column and **stays** — this
     decision is about the serving route and the access check, not the column.
   - Backfill the topic for any existing org-logo rows so already-uploaded
     logos keep resolving through the new route; a logo that 404s after this
     change is a regression, not an acceptable migration cost.

2. **Does `custom_css` ever move into `settings`?**
   **Decision: no, not now** — as the spec already states. It is released,
   behaviourally identical either way, and moving it means a backfill over
   released rows for zero behaviour change. Leave it as a column.

3. **Do the subscriber/subscription toggles from `2026-08-21-07` belong in
   `settings` too?**
   **Decision: out of scope** — as the spec already states. This spec covers
   branding and the public-asset check only. Do not touch the subscriber
   toggles.

## Implementation Plan

Ordered so each step leaves the tree building. Steps 1–3 are the storage move,
4–6 the authorization move, 7 the reaping obligations, 8–9 docs and tests.

### 1. Model — branding becomes a `settings` section

- `models.BrandingSettings` (`logoFileUid`, `faviconFileUid`, `hideBranding`)
  hangs off `StatusPageSettings.Branding`, exactly as the spec spells it.
- Accessors on `StatusPageSettings` — `LogoFileUID()`, `FaviconFileUID()`,
  `HideBranding()` — so every call site reads `page.Settings.LogoFileUID()`
  and nothing has to nil-check the section.
- `StatusPage.LogoFileUID` / `.FaviconFileUID` / `.HideBranding` bun fields are
  **deleted**.
- `StatusPageBrandingUpdate` keeps its name and its whole-write semantics, but
  the unit it writes wholly is now the **`branding` section**, not two columns:
  it gains `HideBranding bool`, and every caller restates all three.
- `StatusPageUpdate.HideBranding *bool` stays on the generic update shape (the
  PATCH wire field is unchanged); the DB layer routes it into `settings`.

### 2. DB layer — a merge in SQL, never a read-modify-write in Go

- `UpdateStatusPageBranding` writes the `branding` section with a **two-level**
  merge, so a concurrent `availability` write cannot be clobbered:
  - Postgres: `settings = settings || jsonb_build_object('branding',
    coalesce(settings->'branding','{}'::jsonb) || ?::jsonb)`.
    A one-level `settings || '{"branding":…}'` would be the same bug one level
    down — it replaces the whole `branding` object.
  - SQLite: `settings = json_patch(settings, ?)` with
    `{"branding":{…}}`; `json_patch` is RFC 7386, i.e. already recursive.
- Clearing a slot patches the key to JSON `null`: Postgres **stores** `null`,
  SQLite **removes** the key, both decode to a nil `*string`. Pinned by a
  parity test rather than trusted.
- `UpdateStatusPage`'s `HideBranding` uses the same merge. When the same call
  also carries `Settings` (a whole-column overwrite), the flag is folded into
  that value **in Go before the write** — two `SET settings = …` clauses in one
  UPDATE is a Postgres error, and the fold is what makes it unreachable.
- `GetStatusPageByAssetFileUID` and `GetOrganizationByLogoFileUID` are deleted
  from `db.Service` and both implementations: nothing authorizes by state any
  more.

### 3. Migration — edit `015` in place, both dialects

- `status-page-branding` section keeps its banner (it is a test anchor) and
  loses all three `alter table status_pages add column` statements and the two
  partial indexes, up **and** down.
- The SQLite `status-page-password` section rebuilds `status_pages`; the three
  columns come out of its `create table`, out of both halves of its
  positional `insert … select`, and the two index recreations go.
- The branding section is not left empty — it now carries the **org-logo
  backfill** that Resolved Q1 requires:
  - `files.topic = 'organizations/<org uid>/logo'` for every file an
    organization currently points at through `logo_file_uid`;
  - `organizations.logo_url` rewritten from `/pub/org-logos/<uid>` to
    `/pub/assets/<uid>`.
  The down half reverses both, so the section still has a teardown.

### 4. `files/publictopics.go` — the closed allowlist

- `IsPublicTopic(topic string) bool`, plus the topic builders that are its only
  legitimate producers: `StatusPageAssetTopic`, `StatusPageTopicPrefix`,
  `OrganizationLogoTopic`, `OrganizationTopicPrefix`.
- It parses strictly rather than string-matching: exactly three non-empty
  segments over `[A-Za-z0-9.-]` (so `/`, `.` and `..` cannot appear inside a
  segment), then an entity→kind allowlist. Empty, whitespace, trailing slash,
  extra segment, unknown entity, unknown kind and traversal all deny.
- It cannot reuse `attachments.ParseTopic`: `attachments` imports `files`, so
  the dependency only runs one way. The grammar is re-stated, and a test pins
  that the two agree on the shape.

### 5. One public asset route, one access check

- `files.Service.GetPublicFile(ctx, uid)` — org-agnostic, **live rows only**,
  and returns `ErrFileNotFound` unless `IsPublicTopic(file.Topic)`. The
  live-rows-only clause is the entire un-publish mechanism for replace and
  clear, so it is asserted, not assumed.
- `files.Handler.PublicAssetGet` serves it through `files.WriteContent`
  (`nosniff`, attachment-disposition for SVG) with a 5-minute cache.
- Canonical path `files.PublicAssetPathPrefix = "/pub/assets/"`. The
  status-page prefix `/pub/status-page-assets/` is registered on the **same
  handler**: the spec requires `logoUrl`/`faviconUrl` to stay byte-identical,
  so that string has to keep resolving — but it is now an alias of the generic
  route, not a second implementation.
- `/pub/org-logos/` and `orglogo.OpenLogo`/`PublicGet` are **deleted**;
  `statuspageassets.OpenAsset` and its `PublicGet` go the same way. There is
  exactly one public-asset handler left in the tree.

### 6. Producers set the topic

- `statuspageassets.Upload` → `files.WithTopic(StatusPageAssetTopic(page, kind))`.
- `orglogo.Upload` → `files.WithTopic(OrganizationLogoTopic(org))`, and its
  stored `logo_url` becomes `/pub/assets/<uid>`.

### 7. The two reaping obligations

- Deleting a status page reaps `status-pages/<uid>/`; deleting an organization
  reaps `organizations/<uid>/`. Both go through
  `db.DeleteFilesByTopicPrefix` — the same primitive `files.DeleteAttachments`
  wraps — because neither service holds a `files.Service` and injecting one to
  reach the identical query would be ceremony.
- Without these, a deleted page's logo (and a deleted org's) stays public
  forever: the page/org row disappearing used to do this implicitly, and after
  this change nothing does.

### 8. Docs

- `wiki/conventions/database.md`: the settings-vs-column rule, written down.
- `web/docs/docs/features/status-pages.md`: state plainly that **disabling a
  page no longer takes its logo URL offline** — an operator must clear the
  asset, not disable the page.
- OpenAPI (`/pub/assets/` in the org-logo prose), the embedded
  `docsres/docs/api/upload-org-logo.md`, `server/CLAUDE.md` and
  `wiki/api-specification/status-pages.md` follow the route rename.
  `go generate ./pkg/client/...` is re-run and the generated client committed.

### 9. Tests

Per the spec's Testing section, and specifically:

- **Schema guard**, both dialects: a fresh database has none of the three
  columns on `status_pages` — with a positive control (`uid` is there) so the
  assertion cannot pass against an empty enumeration.
- **The clobber test**, both dialects, in both directions: availability
  thresholds survive a branding write, and branding survives an availability
  write.
- Parity: clearing a logo decodes to nil on both engines whatever each stores.
- `IsPublicTopic` table-driven, including every malformed case the spec lists.
- Public route: live allowlisted file serves; replaced, cleared, and
  deleted-page files 404; **disabled and password pages now serve** — asserted
  explicitly so the loosening is a recorded decision, not a later discovery.
- Golden response: `logoUrl` / `faviconUrl` / `hideBranding` byte-identical on
  the org-facing and public payloads.
- The existing `status_page_branding_migration_test.go` pair is updated, not
  deleted, and Postgres replay/rollback still execute both halves.
