# Edit-on-route convention + on-call schedule edit page

## Goal

Establish a project-wide convention that editing an entity always navigates
to a dedicated route (mirroring the create route), never opens a modal
dialog. Apply it by migrating the on-call schedule editor — currently a
`Dialog` opened from the detail page — to a real `/on-call/$slug/edit`
route. Audit and queue follow-ups for any other edit-in-modal patterns
discovered.

## Why

- Routes are bookmarkable, deep-linkable, and survive page refreshes; modals
  don't.
- Browser back/forward becomes the natural cancel/return gesture instead of
  custom modal close handlers.
- Multi-field forms in modals fight viewport height, scroll independently of
  the page, and lose work on accidental backdrop clicks.
- Consistent route shape (`<resource>/new` ↔ `<resource>/$id/edit`) makes
  the app predictable and lets us share the same form component across
  create and edit.

The current on-call editor is a `Dialog` containing the full
`OnCallScheduleForm` (multi-section form with name, timezone, rotation,
handoff, users). It's the clearest violation of the rule and the right
proving ground for the convention.

## Scope

### In scope

1. **Convention doc** — already added to `web/dash0/CLAUDE.md` and
   surfaced on the design-reference page so future contributors see it
   immediately. No code change here, just verifying both copies stay in
   sync as part of the spec's deliverable.
2. **On-call schedule edit page**
   - New route file `web/dash0/src/routes/orgs/$org/on-call.$slug.edit.tsx`
     mirroring `on-call.new.tsx`'s structure.
   - It loads the current schedule via `useOnCallSchedule(org, slug)`,
     renders `<OnCallScheduleForm mode="edit" />`, calls
     `useUpdateOnCallSchedule`, and on success navigates back to
     `/on-call/$slug` with a success toast.
   - A back arrow returns to the detail page (not the list).
3. **Detail page cleanup** (`on-call.$slug.tsx`)
   - Remove the edit `Dialog`, `editOpen`/`editError` state, and the
     `OnCallScheduleForm` import.
   - The Edit button (top-right of the detail page) becomes a `<Link>` to
     `/on-call/$slug/edit`.
   - The list-row Edit icon (added in the recent row-actions migration)
     also points to `/on-call/$slug/edit`, not `/on-call/$slug`.
4. **i18n** — reuse `oncall:form.edit` for the edit page title; no new keys
   needed unless we want a distinct page header (`oncall:edit.title`).
5. **Audit** — list every other route in `web/dash0/src/routes/` that
   renders a `<Dialog>` with an edit-style form. Known candidates from
   initial scan:
   - `checks.index.tsx` — `dialog.renameGroupTitle` (group rename).
     Single-field rename is borderline; document as accepted exception or
     migrate to `/check-groups/$slug/edit`.
   - `status-pages.$statusPageUid.index.tsx` — section/resource Add dialogs
     are *create* flows; edit-mode for a section likely doesn't exist yet.
     Confirm and either add edit routes or note the gap.
   For each, file a follow-up spec under `specs/todos/` rather than fixing
   in this one.

### Out of scope

- Inline single-field edits (e.g. slug rename in `checks.$checkUid.index`,
  password edit toggles in `server.*.tsx`). The convention explicitly
  exempts those — document the carve-out, don't refactor them.
- Confirmation `AlertDialog`s used for destructive actions. Those stay as
  modals.
- Backend changes — no API surface moves.

## Implementation notes

- Use the existing `OnCallScheduleForm` component as-is. It already accepts
  `mode="edit"`, `initialValues`, `onSubmit`, `onCancel`, `submitting`,
  `error`. The only new thing is the route wrapper that wires it up.
- Route file naming follows the codebase's TanStack file-router conventions:
  `on-call.$slug.edit.tsx` will become `/orgs/$org/on-call/$slug/edit`.
- Loading + error states: while `useOnCallSchedule` is loading, render the
  same skeleton the detail page uses. If the schedule is missing (404),
  redirect to `/on-call` with an error toast.
- Submit handler should mirror `handleEditSubmit` from the current detail
  page (lines ~133–154 of `on-call.$slug.tsx`).

## Acceptance criteria

- `/dash0/orgs/default/on-call/<slug>/edit` renders the form prefilled with
  the schedule's current values.
- Browser back from the edit page returns to `/on-call/$slug`.
- Saving navigates to `/on-call/$slug` with a "Schedule updated" toast.
- The detail page no longer imports `Dialog` or `OnCallScheduleForm`.
- The list-row Edit icon and the detail page Edit button both point to the
  new edit route.
- Playwright e2e: existing on-call edit test (if any) updated to drive the
  new flow; otherwise add a minimal smoke test that visits the edit page,
  changes the name, saves, and asserts the new name on the detail page.
- A follow-up spec is filed for each remaining edit-in-modal site
  identified in the audit.

## Files affected

- `web/dash0/src/routes/orgs/$org/on-call.$slug.edit.tsx` — new
- `web/dash0/src/routes/orgs/$org/on-call.$slug.tsx` — remove edit Dialog
- `web/dash0/src/routes/orgs/$org/on-call.index.tsx` — Edit icon target
- `web/dash0/CLAUDE.md` — convention (already done)
- `web/dash0/src/routes/orgs/$org/design-reference.tsx` — surface the rule
- `web/dash0/e2e/` — update or add the on-call edit smoke test
