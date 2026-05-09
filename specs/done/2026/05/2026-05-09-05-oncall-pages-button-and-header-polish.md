# Align on-call pages (list / detail / new / edit) to the design reference

## Why

The on-call routes were built before the dash0 design-reference page
codified button conventions. They still mix three older patterns:

- A back affordance rendered as a small `<Link>` with a leading arrow icon
  in the **top-left** of the page.
- Action buttons (Edit / Delete / Create) using `Icon className="h-4 w-4 mr-1"`
  + always-visible label, instead of the design-reference's
  icon + `<span className="hidden sm:inline">Label</span>` collapsing pattern.
- The edit/save flow's primary button just says "Save" with no Save icon,
  and the form's cancel/submit row sits centred-left instead of right-aligned
  like the rest of the app.

The design reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`,
section "Buttons & badges", L415–L520) is now the canonical source. This
spec re-skins the four on-call routes to match it without touching their
behaviour.

## Reference patterns

From `design-reference.tsx`:

**Action button (icon + collapsing label):**
```tsx
<Button aria-label="Edit">
  <Pencil />
  <span className="hidden sm:inline">Edit</span>
</Button>
```
Icons used:
- Save → `<Save />`
- Edit → `<Pencil />`
- Delete → `<Trash2 />` with `variant="destructive"`
- Reload → `<RotateCw />` with `variant="outline"`
- Create → `<Plus />`

**Top-right back button (always icon-only, regardless of viewport):**
```tsx
<Button variant="ghost" size="icon" aria-label="Back">
  <ArrowLeft />
</Button>
```

The back button sits next to the other top-right actions, not in a separate
top-left strip.

## Scope — page by page

### 1. `on-call.index.tsx` (list)

Current header:
```tsx
<Button asChild>
  <Link to="…/new">
    <Plus className="h-4 w-4 mr-1" />
    {t("oncall:list.create")}
  </Link>
</Button>
```

Change to:
```tsx
<Button asChild aria-label={t("oncall:list.create")}>
  <Link to="…/new">
    <Plus />
    <span className="hidden sm:inline">{t("oncall:list.create")}</span>
  </Link>
</Button>
```

Refresh button (already `size="icon"`) stays as-is — it's icon-only by
design and matches `checks.index.tsx`.

Row actions (Edit / Delete `ghost size="icon"`) already follow the row-actions
convention. No change.

### 2. `on-call.$slug.index.tsx` (detail — created by spec 04)

Current header has a top-left back link + a top-right action group with
"Edit" and "Delete" buttons that always show their label.

Drop the top-left back link entirely. Move the back button into the
top-right action group as the leftmost element, icon-only:

```tsx
<div className="flex items-start justify-between gap-4">
  <div>
    <h1>…</h1>
    <p className="text-muted-foreground text-sm">…</p>
  </div>
  <div className="flex items-center gap-2">
    <Button asChild variant="ghost" size="icon" aria-label={t("oncall:detail.back")}>
      <Link to="/orgs/$org/on-call" params={{ org }}>
        <ArrowLeft />
      </Link>
    </Button>
    <Button asChild variant="outline" aria-label={t("oncall:detail.edit")}>
      <Link to="…/edit" data-testid="oncall-edit-button">
        <Pencil />
        <span className="hidden sm:inline">{t("oncall:detail.edit")}</span>
      </Link>
    </Button>
    <Button variant="destructive" aria-label={t("oncall:detail.delete")} onClick={handleDelete}>
      <Trash2 />
      <span className="hidden sm:inline">{t("oncall:detail.delete")}</span>
    </Button>
  </div>
</div>
```

Notes:
- Keep `data-testid="oncall-edit-button"` on the Edit Link wrapper so e2e
  tests stay green.
- Replace the unconditional `confirm(...)` in `handleDelete` with the
  `AlertDialog` pattern used by `on-call.index.tsx` (in scope — the
  inconsistent UX is a small fix that lands naturally with the button
  rework, and unblocks a Playwright test that can't drive a native
  `window.confirm`). If this widens the diff too much, lift it into a
  separate follow-up spec; do not skip the AlertDialog migration without
  a reason.
- The iCal section's "Rotate" / "Disable" / "Enable" buttons keep their
  current shape (in-card, not page-header actions) — out of scope.

### 3. `on-call.new.tsx` (create)

Currently has only a top-left H1 and the form Card. Add a top-right back
button mirroring the detail page:

```tsx
<div className="flex items-start justify-between gap-4">
  <h1>{t("oncall:form.create")}</h1>
  <Button asChild variant="ghost" size="icon" aria-label={t("oncall:detail.back")}>
    <Link to="/orgs/$org/on-call" params={{ org }}>
      <ArrowLeft />
    </Link>
  </Button>
</div>
```

(Keep the form Card unchanged.)

### 4. `on-call.$slug.edit.tsx` (edit)

Drop the top-left back link. Add a top-right back button aimed at the
detail page (`/on-call/$slug`):

```tsx
<div className="flex items-start justify-between gap-4">
  <div>
    <h1>{t("oncall:form.edit")}</h1>
  </div>
  <Button asChild variant="ghost" size="icon" aria-label={t("oncall:detail.back")}>
    <Link to="/orgs/$org/on-call/$slug" params={{ org, slug }}>
      <ArrowLeft />
    </Link>
  </Button>
</div>
```

### 5. `OnCallScheduleForm` footer (shared by new + edit)

Current:
```tsx
<div className="flex gap-2">
  <Button type="submit" disabled={submitting}>{t("oncall:form.submit")}</Button>
  <Button type="button" variant="outline" onClick={onCancel}>{t("oncall:form.cancel")}</Button>
</div>
```

Target:
```tsx
<div className="flex justify-end gap-2">
  <Button type="button" variant="outline" onClick={onCancel}>
    {t("oncall:form.cancel")}
  </Button>
  <Button type="submit" disabled={submitting} aria-label={t("oncall:form.submit")}>
    <Save />
    <span className="hidden sm:inline">{t("oncall:form.submit")}</span>
  </Button>
</div>
```

(`oncall:form.submit` is already "Save" — no i18n change needed. The
`Save` icon comes from `lucide-react`.)

## Out of scope

- The `OnCallScheduleForm` field layout itself (labels, sections, user-list).
  Restructuring the form is a separate piece of work; this spec only touches
  the footer button row.
- The list page's table layout, filter bar, search input — already aligned
  by `2026-05-06-02-align-listing-pages-to-checks-style.md`.
- The detail page's iCal feed Card and overrides table.
- Adopting the shared `<PageHeader>` component if/when one lands. We keep
  inline JSX byte-compatible with sibling pages.
- Backend or i18n key changes (existing `oncall:detail.back`,
  `oncall:detail.edit`, `oncall:detail.delete`, `oncall:list.create`, and
  `oncall:form.submit` keys all keep their current values).

## Acceptance criteria

- [ ] All four on-call routes use the design-reference's
  icon + `<span className="hidden sm:inline">…</span>` action-button
  pattern with explicit `aria-label` on each.
- [ ] No on-call route has a top-left back link any more — back is always
  the leftmost element of the top-right action group, icon-only,
  `variant="ghost" size="icon"`, `<ArrowLeft />`.
- [ ] On the detail page, the destructive delete is a single
  `variant="destructive"` `<Button>` with a `<Trash2 />` icon and a
  collapsing label, gated by an `AlertDialog` (same pattern as the list
  page's delete confirmation). No `window.confirm`.
- [ ] On a viewport ≤ 640 px (Tailwind `sm` breakpoint), every action
  button on every on-call route renders its icon only — labels collapse
  cleanly with no overflow.
- [ ] The form footer's primary button shows a `<Save />` icon next to
  its label and right-aligns with the cancel button.
- [ ] Existing Playwright e2e (selectors `oncall-edit-button`,
  `oncall-row`, `oncall-refresh`) still pass without modification.
- [ ] `bun run lint`, `bun run build:no-check`, and `make lint-back` pass.

## Files affected

- `web/dash0/src/routes/orgs/$org/on-call.index.tsx` — Create button
- `web/dash0/src/routes/orgs/$org/on-call.$slug.index.tsx` — top-right
  action group + AlertDialog for delete (created by spec 04 — sequence
  this spec after it lands)
- `web/dash0/src/routes/orgs/$org/on-call.new.tsx` — top-right back button
- `web/dash0/src/routes/orgs/$org/on-call.$slug.edit.tsx` — top-right
  back button
- `web/dash0/src/components/oncall/on-call-schedule-form.tsx` — footer
  layout + Save icon

## Sequencing

This spec depends on `2026-05-09-04-fix-oncall-edit-route.md` because the
detail-page changes target `on-call.$slug.index.tsx`, which the edit-route
fix creates. Land 04 first, then this one. They could be combined into a
single PR if both are reviewed together, but keep the commits separate so
the route-shape fix is isolated for any future bisect.

## Visual QA checklist

After merging, walk through these URLs in both light and dark mode at
desktop and ≤ 375 px viewport widths:

1. `/dash0/orgs/default/on-call` — list, create button collapses to icon
   on mobile.
2. `/dash0/orgs/default/on-call/new` — back icon top-right; form footer
   has Save icon, right-aligned.
3. `/dash0/orgs/default/on-call/<slug>` — back / edit / delete in
   top-right, edit + delete labels collapse on mobile, delete opens
   AlertDialog.
4. `/dash0/orgs/default/on-call/<slug>/edit` — back icon top-right;
   form footer matches `/new`.

Reference the design-reference page in a second tab while comparing.
