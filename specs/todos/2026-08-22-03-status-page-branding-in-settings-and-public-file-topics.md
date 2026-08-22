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
