---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Hand-written status page text exists in one language only

## Problem

Migration `038_v0_52_0` added recipe columns (`status_updates.title_i18n` / `body_i18n`,
`incident_publications.public_title_i18n`, `incidents.title_i18n`): `{"key": "...", "params": {...}}`,
rendered in the reader's language. They only cover text the server generates (10 keys, e.g.
`publication.openedTitle`, `incident.title.down`).

As soon as a person writes or edits a title or body, the recipe is cleared
(`server/internal/handlers/statusupdates/service.go:691` and `:703`,
`server/internal/db/models/incident_publication.go:198`) and only the literal column is left
(`status_updates.title` / `body_markdown`, `incident_publications.public_title`), in one language.
A visitor reading the status page in another language gets that text untranslated.

A key plus params cannot describe a real incident. A person documenting an event in several
languages needs to write the full title and body in each of them.

## Proposal

Keep the recipes as they are. Add full per-language text next to them, for hand-written content.

1. **Schema**, both dialects (`server/internal/db/postgres/migrations/`, `server/internal/db/sqlite/migrations/`).
   Add a nullable `translations` JSON column (`jsonb` on Postgres) on `status_updates` and
   `incident_publications`, with a column comment on Postgres. Check
   `git tag --contains` on `038_v0_52_0.tx.up.sql` first: append a new section to 038 if no tag
   contains it, otherwise create `039`. Update the `.down.sql` too. `incidents` gets no column
   (internal title, not public).
2. **Shape.** Keyed by language code, one entry per language, holding the full text:
   - `status_updates`: `{"fr": {"title": "...", "body": "..."}}` (`body` is markdown, like `body_markdown`).
   - `incident_publications`: `{"fr": {"title": "..."}}`.
   An entry is complete or absent: reject an entry with an empty `title`, and on `status_updates`
   an entry whose `body` is empty while the literal `body_markdown` is not.
3. **Models.** Add a typed `Translations` field (a map of language code to a small struct, not a
   free `map[string]any`) to `server/internal/db/models/status_update.go` (next to `TitleI18n`,
   line 58) and `server/internal/db/models/incident_publication.go` (next to `PublicTitleI18n`,
   line 141), and to their update structs. Carry it through
   `server/internal/db/postgres/status_update.go`, `incident_publication.go` and the sqlite twins.
4. **API.** `POST` and `PATCH` of status updates and publications accept and return
   `translations` (camelCase). In `PATCH`, the field replaces the whole map; `{}` or `null` clears
   it; an absent field leaves it untouched. Validate in the services
   (`server/internal/handlers/statusupdates/service.go`,
   `server/internal/handlers/incidentpublications/service.go`): language code in the supported
   UI language list, the entry for the page's own language rejected (the literal column is that
   language), same length limits as the literal fields. Invalid input answers `VALIDATION_ERROR`.
   The public status page endpoints (`server/internal/app/public_incidents.go`) return the map too.
   Update `server/internal/app/openapi/openapi.yaml` and run `make generate`.
5. **Editing the literal text does not touch `translations`.** Clearing the recipe stays as is.
6. **Read order**, for a reader in language L:
   1. `translations[L]` if present,
   2. the recipe, if the row still has one,
   3. the literal column.
   Both frontends already render through one helper, `localizeGenerated` (status0:
   `web/status0/src/components/shared/status-update-card.tsx:99` and `:117`,
   `active-incidents.tsx:77`, `tv/tv-board.tsx:202` and `:572`; dash0:
   `routes/orgs/$org/status-pages.$statusPageUid.incidents.$uid.tsx:131` and `:327`,
   `status-updates.index.tsx:107`). Extend it (or wrap it) to take the translations map and the
   current language, so every call site gets the new order. Add `translations` to the types in
   `web/status0/src/api/hooks.ts:132-180` and `web/dash0/src/api/hooks.ts`.
7. **dash0 editor.** On the dedicated edit routes of a status update and of a publication title
   (never a modal), add an "Other languages" block: one collapsible group per supported language
   with a title input and, for updates, a markdown body. Empty group means no translation. Usable
   on mobile. `data-testid` on each field (`translation-fr-title`, `translation-fr-body`...). Reuse
   primitives from the design reference page, and add the pattern there if it is new.
8. **Channels that cannot translate** (mails, webhooks, feeds) keep the literal column. No change.
9. **Docs.** Status page docs in `web/docs/docs/` (how to write an update in several languages),
   `wiki/database-model/` column tables, and a `CHANGELOG.md` entry.

## Tests

- `server/internal/handlers/statusupdates/service_test.go`: create and patch with `translations`,
  read back. Patch the literal title: translations are kept, `title_i18n` is cleared. Patch with
  `{}`: translations cleared. Negative: unknown language code, entry with an empty title, entry
  for the page's own language, oversized body, each answering a validation error.
- `server/internal/handlers/incidentpublications/service_test.go`: same cases for `publicTitle`.
- Handler tests for both: `translations` is in the JSON response, camelCase, and a bad payload
  answers `400 VALIDATION_ERROR`.
- `server/internal/db/sqlite/localized_text_migration_test.go` and
  `server/internal/db/postgres/localized_text_migration_postgres_test.go`: the column exists after
  migrating, an existing row has `NULL`, the down migration drops it.
- Unit test of the extended `localizeGenerated` in status0 and dash0: translation wins over the
  recipe, the recipe wins over the literal, a language without an entry falls through.
- Playwright, `web/dash0/e2e/`: write an update with a French translation in dash0, open the
  status page with `?lang=fr` and see the French title and body, open it with `?lang=de` and see
  the literal text (positive control: the same page shows the French text with `?lang=fr`).

## To verify

- Where the list of supported UI languages lives, and whether the server already has it
  (a grep for `supportedLngs` / `supportedLanguages` found nothing). The validation in step 4
  needs one server-side list shared by both frontends' languages.
- The exact length limits enforced today on `title`, `body_markdown` and `public_title`.
- Whether status updates and publication titles each have a dedicated edit route in dash0 today,
  or are edited inline on `status-pages.$statusPageUid.incidents.$uid.tsx`.
- Whether `public_incidents.go` builds its own response structs (then they need the field too).

## Resolved open questions

Decided by Florent, 2026-11-18 (the recommended answers).

- **Stale translations.** Do not flag or clear a translation when the literal text changes.
  Translations are left untouched. No per-entry timestamp, no hint in the editor.
- **Translation on a machine-written row.** Allowed. A row that still has its recipe accepts
  `translations`: the translation wins for its language, the recipe keeps serving the others.
- **Default language.** The literal column is always the page language. Never pick the default
  text from `translations`.
