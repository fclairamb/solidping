---
model: sonnet
effort: medium
---

# Status pages list needs a magic-wand auto-create, and the Getting Started CTA should land on the list, not the form

## Problem

Creating a first status page still takes a full form walk. The status pages
list (`/dash0/orgs/$org/status-pages`,
`web/dash0/src/routes/orgs/$org/status-pages.index.tsx`) offers exactly one
path: the "New status page" button in the header (`status-pages.index.tsx:220`)
and an empty state (`status-pages.index.tsx:293`) that only *describes* the
feature — neither gives the one-click "just make me a sensible page" shortcut
that already exists elsewhere in the app:

- the **new status page form** has a `Wand2` "Prefill for me" wand that
  attaches every check (`web/dash0/src/components/shared/status-page-form.tsx:243`),
  but you must already be inside the form to use it;
- the **integrations page** has a full auto-create wand — "Set up email alerts
  for me" creates the integration outright with a success/failure toast
  (`web/dash0/src/routes/orgs/$org/integrations.index.tsx:170-178`).

Separately, the dashboard "Getting started" checklist sends the *statusPage*
step's CTA ("Create a status page") straight to the creation form:
`web/dash0/src/components/dashboard/onboarding-checklist.tsx:416` links to
`/orgs/$org/status-pages/new` (pre-attaching the org's first check via
`?checkUid=…`, per spec 2026-08-28-16). Once the list page carries the wand,
dropping the user into the blank form skips the better entry point: the list
is where they can pick between "wand it for me" and the manual form.

## Proposal

Two changes, both dash0-only (no backend work — the create endpoint already
does everything needed):

### 1. Magic-wand auto-create on the status pages list

Add a `Wand2` wand to `status-pages.index.tsx`, following the integrations
wand pattern (`integrations.index.tsx:146-178`):

- **Placement**: an `outline` button next to "New status page" in the
  `PageHeader` actions (`status-pages.index.tsx:219-226`), label along the
  lines of `wand.createForMe` = "Create a status page for me". Also surface it
  in the no-pages empty state (`status-pages.index.tsx:293-303`) so the
  first-run path is one click.
- **Behavior**: one click creates a page via the existing
  `useCreateStatusPage` mutation with sensible defaults, mirroring what the
  form wand + prefill would produce (`status-pages.new.tsx:100-116`):
  - `name`: the org's display name (same source as `status-pages.new.tsx:37`);
  - `slug`: derived from the name with the same slugification the form uses;
  - `checkUids`: **all** the org's checks — reuse the auto-page-through
    pattern from `status-pages.new.tsx:44-57` (`useInfiniteChecks` with
    `limit: 100`, keep fetching until `hasNextPage` is false) so orgs with
    >100 checks aren't silently under-attached. Disable the wand until the
    full list is loaded, exactly like the form wand's `allChecksLoaded` gate
    (`status-pages.new.tsx:72-73`).
  - every other field: the same defaults the create form starts with
    (visibility, history period, toggles) — do not invent new values.
- **After creation**: success toast, then navigate to the created page's
  detail route `/orgs/$org/status-pages/$statusPageUid` (same as
  `status-pages.new.tsx:117-121`) so the user reviews/tweaks what the wand
  built.
- **Failure**: error toast (`ApiError` message when available), stay on the
  list. If the derived slug collides (`CONFLICT`), surface the error — don't
  loop inventing suffixes.
- **Locales**: new `wand.*` keys must land in all six locales
  (`web/dash0/src/locales/*/statusPages.json` — a `wand` object already
  exists there with `prefill`/`loadChecksFailed`); the unit tests check
  locale-key parity.

### 2. Getting Started CTA targets the list

In `onboarding-checklist.tsx:416`, change the statusPage step's link target
from `/orgs/$org/status-pages/new` to `/orgs/$org/status-pages`, and drop the
`checkUid` search prefill from this call site (the `?checkUid=` prefill on the
new-page route itself stays — other callers still use it). Keep the
row-stretch styling and `data-testid` intact. Update any E2E that asserts the
CTA's destination.

## Notes / open questions

- "magic want" in the request is read as **magic wand**, consistent with the
  existing `Wand2` patterns.
- Whether the wand-created page should be published immediately or left at the
  form's default visibility is deliberately left at "form defaults" — if the
  implementer finds the default produces an invisible/unreachable page, prefer
  matching whatever a user gets by opening the form, clicking the prefill
  wand, and submitting without touching anything else.
- This partially supersedes the checklist half of spec 2026-08-28-16
  (pre-attaching the first check from the checklist CTA); the wand attaching
  *all* checks from the list page covers the same intent more completely.
