# Status-page edit silently drops historyDays / showAvailability / showResponseTime

## Context

On `/dash0/orgs/$org/status-pages/$uid/edit`, changing the **History
Period** dropdown (7 / 30 / 90 days) appears to save successfully but
the value never changes — it stays on `90`. The same is true for the
two visibility toggles **Show Availability** and **Show Response
Time**.

## Root cause

`StatusPageForm` (`web/dash0/src/components/shared/status-page-form.tsx`,
L67–69, L79) collects all three fields and passes them to its
`onSubmit(data)` callback:

```tsx
const [showAvailability, setShowAvailability] = useState(initialData?.showAvailability ?? true);
const [showResponseTime, setShowResponseTime] = useState(initialData?.showResponseTime ?? true);
const [historyDays, setHistoryDays] = useState(initialData?.historyDays ?? 90);
…
await onSubmit({ name, slug, description, visibility, isDefault, enabled, showAvailability, showResponseTime, historyDays });
```

But the **edit** route component
(`web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.edit.tsx`
L62–69) destructures only six of those nine fields when calling the
mutation:

```tsx
await updateStatusPage.mutateAsync({
  name: data.name,
  slug: data.slug,
  description: data.description || undefined,
  visibility: data.visibility,
  isDefault: data.isDefault,
  enabled: data.enabled,
});
```

The three new-style fields are silently dropped. The mutation succeeds
because the backend accepts the partial payload (its
`UpdateStatusPageRequest` makes them optional `*int` / `*bool`), and
`useStatusPage` re-fetches the unchanged row — so the UI shows the old
value (`90`) and the user assumes the dropdown didn't save.

The **create** route
(`web/dash0/src/routes/orgs/$org/status-pages.new.tsx` L22–29) has the
same shape: it forwards `name, slug, description, visibility,
isDefault` only, dropping the three fields. The bug is just less
visible there because the backend defaults match what the form shows
at create time.

## Honest opinion

1. **The forwarding pattern is the bug, not the form.** Both route
   handlers re-build the payload by hand and forgot the new fields
   when spec #47 (or whichever spec added them) extended the form.
   Forwarding the whole `data` object would have made this impossible.
2. **Fix the forwarders, then make the bug structurally hard.** A
   minimal fix patches the two route files. To stop the next field
   from silently regressing the same way, change the forwarders to
   pass `data` straight through (the payload type is already a
   superset of the API request) and rely on the API request type to
   pick up new fields.
3. **No backend work needed.** `service.go` L139–141 / L153–155
   already accept the three `*` fields. Verified via `curl` against a
   running dev server (see Verification §3).
4. **No new translation keys, no UI changes** — the form already
   renders the dropdown and toggles correctly.

## Scope

**In**

- Forward `historyDays`, `showAvailability`, `showResponseTime` from
  the form's `onSubmit(data)` to the mutation in both routes.
- Use the simplest forwarding shape that keeps TypeScript honest
  (spread or explicit field list — see "Implementation").
- Playwright e2e: create a status page → edit it to set
  `historyDays = 7` → verify the persisted page reflects the change.

**Out**

- Refactoring `StatusPageForm` into a controlled component or
  extracting a shared payload-builder helper. Spec is a bug fix,
  not a redesign.
- Backend changes (none needed).
- Status-page detail-page header tweaks (covered by spec #52).

## Per-element changes

### 1. Edit route — `status-pages.$statusPageUid.edit.tsx` (L62–69)

Current:

```tsx
await updateStatusPage.mutateAsync({
  name: data.name,
  slug: data.slug,
  description: data.description || undefined,
  visibility: data.visibility,
  isDefault: data.isDefault,
  enabled: data.enabled,
});
```

Target:

```tsx
await updateStatusPage.mutateAsync({
  name: data.name,
  slug: data.slug,
  description: data.description || undefined,
  visibility: data.visibility,
  isDefault: data.isDefault,
  enabled: data.enabled,
  showAvailability: data.showAvailability,
  showResponseTime: data.showResponseTime,
  historyDays: data.historyDays,
});
```

(Explicit shape rather than spread, because `description` needs the
empty-string-to-undefined coercion that spread would bypass.)

### 2. Create route — `status-pages.new.tsx` (L22–29)

Current:

```tsx
const page = await createStatusPage.mutateAsync({
  name: data.name,
  slug: data.slug,
  description: data.description || undefined,
  visibility: data.visibility,
  isDefault: data.isDefault || undefined,
});
```

Target:

```tsx
const page = await createStatusPage.mutateAsync({
  name: data.name,
  slug: data.slug,
  description: data.description || undefined,
  visibility: data.visibility,
  isDefault: data.isDefault || undefined,
  showAvailability: data.showAvailability,
  showResponseTime: data.showResponseTime,
  historyDays: data.historyDays,
});
```

`CreateStatusPageRequest` already declares all three as optional
(`hooks.ts` L807–816), so this compiles unchanged.

## Files to modify

- `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.edit.tsx`
- `web/dash0/src/routes/orgs/$org/status-pages.new.tsx`

No backend, type, or i18n changes.

## Verification

1. **Manual smoke (admin):**
   - `make dev`. Login as `admin@solidping.com`.
   - Create a status page named "Test"; default historyDays 90.
   - Open it → Edit → change History Period to "7 days" → Save.
   - Return to the detail view; refetch — the chart now shows 7 days
     of history. Re-open Edit; dropdown is on "7 days".
2. **API double-check (curl):**
   - `curl -X PATCH …/status-pages/{uid} -d '{"historyDays":30}'` →
     200, returned row has `historyDays: 30`. (Confirms backend is
     fine; pure client bug.)
3. **Playwright e2e:**
   - Add a test in `web/dash0/e2e/status-pages.spec.ts`
     (create the file if it doesn't exist; mirror
     `web/dash0/e2e/availability.spec.ts` style):
     - Login → create a status page → open edit → select 7 days →
       save → assert the next GET returns `historyDays: 7`.
     - Toggle Show Availability off → save → assert
       `showAvailability: false` round-trips.

## Implementation plan

1. Patch the edit-route forwarder.
2. Patch the new-route forwarder.
3. Run `make build-dash0 lint-dash`.
4. Add a Playwright spec covering the round-trip.
5. Commit, archive, merge.

## Critical files

- `web/dash0/src/components/shared/status-page-form.tsx` — form
  (read-only for this spec; field collection is correct).
- `web/dash0/src/api/hooks.ts` L807–828 — `CreateStatusPageRequest`
  and `UpdateStatusPageRequest` (already accept the three fields).
- `server/internal/handlers/statuspages/service.go` L139–155 —
  backend update DTO (already accepts the three fields).
