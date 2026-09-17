---
model: opus
effort: high
---

# Move the badge builder under the check it belongs to

*Reported by **Jens**, an early user, on 2026-09-16, in first-run feedback on
SolidPing.*

## What Jens said

> "Badges" should be under each check, instead of its own page, no?

He also filed **Badges** under his own "Checks setup" bucket rather than under anything global —
i.e. he read a badge as a *property of a check*, not as a feature of the org.

## He is right, and the code already agrees with him

[`badges.tsx`](web/dash0/src/routes/orgs/$org/badges.tsx) (627 lines) is **not** a gallery, a
list, or a manager. It is a stateless **builder for exactly one check at a time**, with all state
in the URL (`validateSearch`, [lines 71-106](web/dash0/src/routes/orgs/$org/badges.tsx#L71-L106)):
`?check=&components=&period=&style=&label=&minWidth=&width=`. Nothing is persisted. There is no
list of "my badges". The first thing the page asks you to do is pick a check
([`CheckPicker` at line 449](web/dash0/src/routes/orgs/$org/badges.tsx#L449)); until you do, the
right pane is an empty state reading "Select a check to preview and generate badges"
([line 619](web/dash0/src/routes/orgs/$org/badges.tsx#L619)).

Three things in the current code are already admissions that this belongs to a check:

1. The check detail page has a **Badges** button that deep-links with the check pre-filled —
   [`checks.$checkUid.index.tsx:1253-1269`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L1253-L1269),
   `to="/orgs/$org/badges" search={{ check: check.slug ?? checkUid }}`.
2. When `?check=` is set, the page renders a **back-link to that check**
   (`badge-back-to-check`, [lines 421-431](web/dash0/src/routes/orgs/$org/badges.tsx#L421-L431)).
   A page that grows a back-link to a parent is a child page.
3. The backend route is already check-scoped:
   `GET /api/v1/orgs/:org/checks/:check/badges/:components`
   ([`server.go:1166-1168`](server/internal/app/server.go#L1166-L1168)). The **URL structure of
   the API is the IA Jens is asking for**; only the dashboard route disagrees.

So the sidebar entry is the redundant part: it is the only way to land on the page with no check
selected, which is the only state in which the check picker is needed.

## Proposal

### A. Move the route

`/orgs/$org/badges` → **`/orgs/$org/checks/$checkUid/badges`**, as a sibling of
`checks.$checkUid.index.tsx` under the existing `checks.$checkUid.tsx` layout.

- The check comes from the path param, not from `?check=`. Drop the `check` search param and the
  `CheckPicker` entirely — along with the "select a check" empty state
  ([line 619](web/dash0/src/routes/orgs/$org/badges.tsx#L619)) and the `badge-check-not-found`
  alert ([lines 602-607](web/dash0/src/routes/orgs/$org/badges.tsx#L602-L607)), which become the
  route loader's 404 instead.
- Keep every other search param exactly as-is (`components`, `period`, `style`, `label`,
  `minWidth`, `width`) including the default-stripping behaviour in `updateSearch`
  ([lines 370-384](web/dash0/src/routes/orgs/$org/badges.tsx#L370-L384)). Those are pinned by
  e2e and are not what this change is about.
- Replace the `badge-back-to-check` link with the standard breadcrumb the check section already
  renders, so the page reads as "Checks › *check name* › Badges".
- The page header keeps `docsHref="/docs/features/status-badges"`.

### B. Keep `/orgs/$org/badges` alive as a redirect

Badge builder URLs get pasted into tickets and chat. Make the old route resolve:

- `?check=<slug|uid>` present → **redirect** to `/orgs/$org/checks/<uid>/badges` preserving every
  other search param. The slug→uid resolution already exists in spirit at
  [`badges.tsx:339-342`](web/dash0/src/routes/orgs/$org/badges.tsx#L339-L342), which calls
  `useCheck(org, search.check)` directly rather than relying on the picker, precisely so a deep
  link to a check outside the picker's first 25 results still resolves. Reuse that.
- No `?check=` → redirect to `/orgs/$org/checks`. There is nothing to build a badge for, and the
  check list is where you pick one.

Do **not** leave the old route rendering a second copy of the builder. One implementation.

### C. Entry points

- The check detail **Badges** button
  ([`checks.$checkUid.index.tsx:1253-1269`](web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx#L1253-L1269))
  now points at the new child route with no search param. It stays exactly where it is, in the
  toolbar between **Clone** and **Publish on a status page**.
- The sidebar and command-palette entries are removed — **by spec `07`, which owns those files**.
  Do not edit `AppSidebar.tsx` or `CommandMenu.tsx` here.

### D. Out of scope — the status-page badge stays where it is

There is a **second, unrelated** badge: `GET /api/v1/status-pages/{org}/{slug}/badge`
([`server.go:1892`](server/internal/app/server.go#L1892)), a page-level rollup surfaced on the
status page Appearance tab
([`status-pages.$statusPageUid.appearance.tsx:340`](web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.appearance.tsx#L340)
→ [`status-page-badge-card.tsx`](web/dash0/src/components/shared/status-page-badge-card.tsx)).
It is already scoped to its own object and needs no change. Mentioning it here only so the
implementer does not "unify" the two — they have different visibility rules (the per-check badge
endpoint is public and unauthenticated regardless of status-page visibility; the page badge
honours the page's gate).

## Tests

- [`web/dash0/e2e/badges.spec.ts`](web/dash0/e2e/badges.spec.ts) (694 lines, 22 tests, serial) is
  the bulk of the work. Most cases stay valid but must navigate via the **check detail → Badges**
  button instead of the sidebar. The cases that must change shape:
  - sidebar-navigation test → delete (the entry is gone).
  - check-selection / live-search / picker tests → delete; there is no picker.
  - "deep-link by slug" and "deep-link by uid beyond the first list page" → convert into
    **redirect** tests against the legacy `/badges?check=` URL. Keep both; they are what proves
    part B works for the two identifier forms.
  - `badge-check-not-found` → becomes the route's 404 behaviour for an unknown `$checkUid`.
  - back-to-check link presence/absence → becomes a breadcrumb assertion.
- [`check-detail.spec.ts:964-969`](web/dash0/e2e/check-detail.spec.ts#L964-L969) asserts the
  Badges button's `href` matches `/orgs/test/badges?check=` — update to the new path.
- [`docs-links.spec.ts:70-79`](web/dash0/e2e/docs-links.spec.ts#L70-L79) reaches the badges page
  to assert its docs link — update its navigation.
- Backend: unchanged. [`badges/service_test.go`](server/internal/handlers/badges/service_test.go)
  (1194 lines) should not need edits; if it does, something went wrong in the frontend-only scope
  of this spec.

## Docs

[`web/docs/docs/features/status-badges.md`](web/docs/docs/features/status-badges.md) describes
reaching the builder. Update the navigation instructions to "open a check, then **Badges**". The
public badge **URL** shape does not change, so the embedding snippets and the URL/param tables
stay as they are.

## Answering the question in Jens's question mark

He wrote it as a question ("…instead of its own page, no?"), so the reply should confirm rather
than just announce: yes, and the API route was already `/checks/:check/badges/:components` — the
dashboard was the only part that pretended badges were an org-level feature.

## Implementation Plan

### Step 1 — new child route (Proposal A)

1. Create `web/dash0/src/routes/orgs/$org/checks.$checkUid.badges.tsx`, route id
   `/orgs/$org/checks/$checkUid/badges` (a sibling of `checks.$checkUid.index.tsx` under the
   existing `checks.$checkUid.tsx` `<Outlet/>` layout).
2. Move the whole builder body over from `badges.tsx`: `BadgePreview`, `CopyButton`,
   `componentDefs`, `parseComponentsString`, `hasRowToken`, the period/style/width/minWidth/label
   controls, and `updateSearch`'s default-stripping — unchanged.
3. `validateSearch` keeps `components`, `period`, `style`, `label`, `minWidth`, `width` with
   identical normalization; the `check` key is dropped.
4. The check is resolved from the path param with `useCheck(org, checkUid)`. Loading → skeleton;
   resolved → builder; not found / error → the `badge-check-not-found` alert, which is now the
   route's own 404 instead of a "bad ?check=" notice. The `CheckPicker` and the
   "select a check" empty state are gone.
5. The `badge-back-to-check` link is deleted; the crumb trail comes from `Breadcrumbs` in
   `routes/orgs/$org.tsx` — extend its `isChecks` branch with a `Badges` leaf
   (`BadgeCheck` icon, `nav:badges`, already present in all four locale bundles) and make the
   check-name crumb a link on that route. Delete the now-unreachable `isBadges` branch.
6. `PageHeader` keeps `docsHref="/docs/features/status-badges"`.

### Step 2 — legacy route becomes a redirect (Proposal B)

1. Rewrite `web/dash0/src/routes/orgs/$org/badges.tsx` down to a redirect-only route. Keep
   `validateSearch` permissive enough to carry `check` plus every builder param through.
2. No `?check=` → `beforeLoad` throws `redirect({ to: "/orgs/$org/checks" })` (synchronous, no
   fetch needed).
3. `?check=<slug|uid>` → a tiny component resolves it with `useCheck(org, search.check)` (the
   same hook the old page used, so a check outside the list's first page still resolves) and
   `navigate({ replace: true })`s to `/orgs/$org/checks/<uid>/badges` carrying every other
   search param. An unresolvable identifier redirects with the raw value as `$checkUid`, so the
   new route's 404 is the single owner of that state.
4. No second copy of the builder anywhere.

### Step 3 — entry point (Proposal C)

Point the check-detail toolbar **Badges** button at
`/orgs/$org/checks/$checkUid/badges` with `search={{}}`, same toolbar slot between **Clone** and
**Publish on a status page**. `AppSidebar.tsx`, `CommandMenu.tsx` and `locales/*/nav.json` are
owned by spec `07` and are not touched here. The status-page badge (Proposal D) is not touched
either.

### Step 4 — tests

1. `e2e/badges.spec.ts`: navigate via check detail → **Badges** (or straight to the new path);
   delete the sidebar-navigation, check-selection and live-search/picker cases; convert the two
   deep-link cases into legacy-redirect assertions (slug and uid); keep the not-found case as the
   new route's 404; replace the two back-link cases with breadcrumb assertions.
2. `e2e/check-detail.spec.ts`: the Badges `href` assertion becomes the new path.
3. `e2e/docs-links.spec.ts`: reach the badges page through a check instead of the sidebar.
4. Backend `badges/service_test.go` stays untouched — this is frontend-only.

### Step 5 — docs + gate

Update the navigation sentence in `web/docs/docs/features/status-badges.md` ("open a check, then
**Badges**"); URL/param tables and embed snippets are unchanged because the public badge URL
shape is unchanged. Gate: `make build-dash0`, `cd web/dash0 && bun run lint`, `bun run
typecheck:e2e`, `make build-docs`, and the three affected Playwright files against a disposable
side-car server.
