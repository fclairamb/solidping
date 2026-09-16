---
model: sonnet
effort: medium
---

# Remove the org-wide Dependencies page (keep the dependency feature)

*Prompted by **Jens**, an early user, on 2026-09-16: "I do not know what
'Dependencies' does in the left side menu." Decision by Florent — remove the page entirely.*

## Scope, stated up front

**Delete the page. Keep the feature.** Check dependencies keep working exactly as they do today:
they are still created and edited on the check form, still displayed on the check detail page,
still drive incident cascade rollup, still round-trip through config-as-code, still available
over the REST API and the CLI. Only the standalone org-wide graph view at `/orgs/$org/dependencies`
goes away.

If a reviewer reads this spec as "drop check dependencies", it has been misread.

## What the page actually is

[`dependencies.index.tsx`](web/dash0/src/routes/orgs/$org/dependencies.index.tsx) (237 lines) is a
**read-only** flat table of `parent → child` edges with a hard/soft badge, a debounced filter
synced to `?q=` ([`:47-64`](web/dash0/src/routes/orgs/$org/dependencies.index.tsx#L47-L64)), and a
refresh button. Both endpoints of each row link to a check detail page. It creates, edits and
deletes nothing — its only hook is `useDependencyGraph`
([`:37`](web/dash0/src/routes/orgs/$org/dependencies.index.tsx#L37)). Despite the name it draws no
graph; it is a list of edges.

It is also the sidebar's only **ungated** admin-ish oddity: the nav entry at
[`AppSidebar.tsx:71-75`](web/dash0/src/components/layout/AppSidebar.tsx#L71-L75) sits in the main
list, visible to every member, with a `GitBranch` icon and a one-word label that means nothing
until you already know the feature exists.

Three facts make it cheap to remove:

1. **It has no docs page.** Its own docs link points at the *incidents* page
   ([`:115`](web/dash0/src/routes/orgs/$org/dependencies.index.tsx#L115) →
   `/docs/features/incidents#group-incidents-correlated-outages`). There is no
   `web/docs/docs/features/dependencies.md`.
2. **It has no dedicated E2E suite.** It is touched only incidentally by
   [`listing-pages-style.spec.ts:75-92`](web/dash0/e2e/listing-pages-style.spec.ts#L75-L92) and
   [`docs-links.spec.ts:109-124`](web/dash0/e2e/docs-links.spec.ts#L109-L124). The real coverage,
   [`check-dependencies.spec.ts`](web/dash0/e2e/check-dependencies.spec.ts) (359 lines), tests the
   per-check flow and does not use this page at all — its header comment says so outright:
   *"Dependencies are edited on the check EDIT page and only displayed on the check detail page."*
3. **Nothing links to it** except the sidebar and the command palette.

## Where the feature lives, and therefore what must keep working

Set a dependency: [`components/checks/form/sections/dependencies.tsx`](web/dash0/src/components/checks/form/sections/dependencies.tsx)
(the `dependsOn` picker, `:83-86`), wired through
[`check-form.tsx:318, 496-529, 960-1016`](web/dash0/src/components/shared/check-form.tsx#L496-L529),
used by `checks.new.tsx` and `checks.$checkUid.edit.tsx`.

Read a dependency: [`components/checks/dependencies-card.tsx`](web/dash0/src/components/checks/dependencies-card.tsx)
(`dependsOn` / `dependedOnBy`, `:39-96`), `dependency-row.tsx`, `dependency-warnings.tsx` (the
confirmation-margin lint).

Behaviour that depends on the edges: incident cascade rollup walks **hard** edges with a depth cap
([`handlers/incidents/rollup.go:96-107, 265-276, 365-376`](server/internal/handlers/incidents/rollup.go#L96-L107));
config-as-code export/apply/diff/validate carry `ExportedDependency`
([`openapi.yaml:10308, 10493`](server/internal/app/openapi/openapi.yaml#L10308)).

## Proposal

### A. Delete

- `web/dash0/src/routes/orgs/$org/dependencies.index.tsx`.
- The sidebar entry ([`AppSidebar.tsx:71-75`](web/dash0/src/components/layout/AppSidebar.tsx#L71-L75))
  and the palette entry ([`CommandMenu.tsx:61`](web/dash0/src/components/CommandMenu.tsx#L61)) —
  **by spec `07`, which owns those two files. Do not edit them here.**
- Any `nav.json` key that becomes unused — also spec `07`, and remember all four locales or
  [`locale-parity.test.ts`](web/dash0/src/locales/locale-parity.test.ts) fails.

### B. Keep

- `GET /api/v1/orgs/:org/dependencies` (the `Graph` handler,
  [`checkdependencies/handler.go:96`](server/internal/handlers/checkdependencies/handler.go#L96),
  wired at [`server.go:1140-1148`](server/internal/app/server.go#L1140-L1148)). It is in the
  OpenAPI spec as `getOrgDependencyGraph`
  ([`openapi.yaml:1747-1893`](server/internal/app/openapi/openapi.yaml#L1747-L1893)) and is served
  by `sp checks deps` ([`server/pkg/cli/checks_deps.go`](server/pkg/cli/checks_deps.go),
  registered at [`commands.go:365`](server/pkg/cli/commands.go#L365)). Removing a documented,
  generated-client-backed endpoint because a dashboard page went away is a breaking change for no
  gain. **Leave the API alone.**
- `useDependencyGraph` in [`api/hooks.ts:1266-1345`](web/dash0/src/api/hooks.ts#L1266-L1345) —
  *only if* something still calls it. If this deletion leaves it with zero call sites, remove the
  hook too rather than leaving dead code; check before assuming either way.
- The `dependencies` i18n namespace
  ([`web/dash0/src/locales/*/dependencies.json`](web/dash0/src/locales/en/dependencies.json), with
  its own [`lib/dependencies-locales.test.ts`](web/dash0/src/lib/dependencies-locales.test.ts)) is
  shared with the check form and detail card. Remove **only** the keys the deleted page owned, and
  in all four locales. Do not delete the namespace.

### C. Do not replace it with anything

Resist adding a "dependency graph" card to the dashboard as consolation. The per-check
`dependencies-card.tsx` already answers the only question a user actually asks — *what does this
check depend on?* — at the moment they are asking it. An org-wide edge list answers no question
anyone had, which is precisely why Jens could not guess what it was for.

## Tests

- [`listing-pages-style.spec.ts:75-92`](web/dash0/e2e/listing-pages-style.spec.ts#L75-L92) —
  remove the dependencies case (`dependencies-filter` / `dependencies-refresh` testids).
- [`docs-links.spec.ts:109-124`](web/dash0/e2e/docs-links.spec.ts#L109-L124) — remove.
- [`check-dependencies.spec.ts`](web/dash0/e2e/check-dependencies.spec.ts) must pass **unchanged**.
  That is the regression gate for "the feature survived": if it needs an edit, the deletion went
  too far.
- Backend tests unchanged — nothing backend is being removed.
- Run the **full** dash0 E2E suite, not just the touched files.

## Docs

No docs page to delete. Check whether
[`web/docs/docs/intro.md:19`](web/docs/docs/intro.md#L19) or
[`docs/cli.md:65`](web/docs/docs/cli.md#L65) point a reader at the dashboard page specifically
(as opposed to the feature); if so, repoint at the check detail page.
