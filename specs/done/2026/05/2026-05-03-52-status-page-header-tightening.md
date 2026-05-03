# Tighten status-page detail header (badges, actions, add buttons)

## Context

The status-page detail view at `/dash0/orgs/$org/status-pages/$uid`
(component `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`,
header at lines ~472–536) currently lays out three stacked rows in
the header: title row, slug/description row, and a third row of
status badges. Action buttons (View / Modify) sit top-right on
desktop and wrap below on mobile. Spec
`2026-05-03-47-mobile-ux-improvements.md` (just merged) is what put
the badges on a separate row and collapsed the action buttons to
icon-only on mobile.

The user is iterating: **tighter** header.

1. View / Modify pinned to the top-right at all viewports.
2. Status indicators on the **title row** (no separate row of badges).
3. The two add-buttons ("+ Ajouter une section" at the page level,
   "+ Ajouter un check" inside each section) reduced to icon-only
   everywhere, with a tooltip carrying the verb.

## Honest opinion

1. **The user wrote `+ Add Check` should become `-`. That's a typo.**
   A `-` glyph reads as remove/delete. Both add buttons get the same
   `Plus` icon. If a remove affordance is wanted later that's a
   separate spec.
2. **Icon-only with no label on desktop hurts discoverability.**
   Mitigation: every icon-only Add button MUST have a `<Tooltip>`
   carrying the same string the visible label used to carry.
3. **Full `<Badge>` next to the title overflows on mobile.** That's
   why spec 47 moved them below. Trade them for **dot indicators**
   (~8 px circles) with the label moved into a tooltip. ~16 px of
   horizontal space versus ~140 px for two badges.
4. **Top-right action buttons on mobile means they encroach on long
   titles.** Title already has `truncate` so the row stays one line;
   the action cluster has `shrink-0`. Long titles elide sooner —
   acceptable trade.
5. **Keep the `page.isDefault` star marker (line 484).** It's the
   only signal of "default" status — keep it on the title row before
   the dots.

## Scope

**In scope**

- Header redesign in `status-pages.$statusPageUid.index.tsx`:
  - Title row: `[← back] H1 [⭐?] [• enabled] [• public]      [👁] [✏]`
  - Subtitle row: `/slug — description`
  - No third row (badges removed).
- "Add section" trigger inside `AddSectionDialog` (same file, line 95)
  → icon-only `Plus` with `Tooltip`.
- "Add check" trigger inside `AddResourceDialog` (same file, line 175)
  → icon-only `Plus` with `Tooltip`.
- Ensure a `<TooltipProvider>` wraps the app (verify in `__root.tsx`;
  add if missing — Radix tooltips need it).

**Out of scope**

- Converting badges to dot indicators on other pages (spec 56
  territory if applicable).
- Extracting a shared `<PageHeader>` component (separate refactor).
- Touching SectionCard internals beyond the AddResource trigger.
- Adding new translation keys (all reused).

## Per-element changes

### 1. Outer header container (line 474)

Current:

```tsx
<div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
```

Target — single flex row at all viewports, title gets `flex-1`,
actions get `shrink-0`:

```tsx
<div className="flex items-start gap-3 justify-between">
```

`items-start` keeps the back button level with the first line of the
title even when the slug/description wraps.

### 2. Title row + dots (lines 481–508)

Replace the existing three-row stack (title+star, slug paragraph,
badges) with two rows. The badges row goes away entirely:

```tsx
<div className="min-w-0 flex-1">
  <div className="flex items-center gap-2 flex-wrap">
    <h1 className="text-2xl sm:text-3xl font-bold tracking-tight truncate">
      {page.name}
    </h1>
    {page.isDefault && (
      <Star className="h-4 w-4 text-yellow-500 fill-yellow-500 shrink-0" />
    )}
    <StatusDot
      enabled={page.enabled}
      enabledLabel={t("statusPages:enabled")}
      disabledLabel={t("statusPages:disabled")}
    />
    <VisibilityDot
      isPublic={page.visibility === "public"}
      publicLabel={t("statusPages:visibility.public")}
      restrictedLabel={t("statusPages:visibility.restricted")}
    />
  </div>
  <p className="text-muted-foreground mt-1">
    /{page.slug}
    {page.description && ` — ${page.description}`}
  </p>
</div>
```

Add the two helpers inline at the top of the file (after imports,
before line 95). They are ~15 lines each — keep them inline; do not
create new files.

```tsx
function StatusDot({
  enabled,
  enabledLabel,
  disabledLabel,
}: {
  enabled: boolean;
  enabledLabel: string;
  disabledLabel: string;
}) {
  const label = enabled ? enabledLabel : disabledLabel;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          aria-label={label}
          className={cn(
            "inline-block h-2 w-2 rounded-full shrink-0",
            enabled ? "bg-green-500" : "bg-muted-foreground/40",
          )}
        />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function VisibilityDot({
  isPublic,
  publicLabel,
  restrictedLabel,
}: {
  isPublic: boolean;
  publicLabel: string;
  restrictedLabel: string;
}) {
  const label = isPublic ? publicLabel : restrictedLabel;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          aria-label={label}
          className={cn(
            "inline-block h-2 w-2 rounded-full shrink-0",
            isPublic ? "bg-blue-500" : "bg-amber-500",
          )}
        />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}
```

Imports needed at the top of the file (most already present):

- `Tooltip`, `TooltipTrigger`, `TooltipContent` from
  `@/components/ui/tooltip`
- `cn` from `@/lib/utils`

The `Badge` import becomes unused if no other site in this file uses
it — remove it from the import list to keep lint happy. (Search for
other `<Badge` usages in the same file before removing.)

### 3. Action buttons (lines 510–530)

Drop `self-end sm:self-auto` (no longer needed — outer flex pins them
right). Keep the icon-only-on-mobile pattern from spec 47, add a
`<Tooltip>` that's only active on mobile (where the label is hidden):

```tsx
<div className="flex gap-2 shrink-0">
  <Tooltip>
    <TooltipTrigger asChild>
      <a
        href={`/status0/${org}/${page.slug}`}
        target="_blank"
        rel="noopener noreferrer"
      >
        <Button
          variant="outline"
          size="sm"
          aria-label={t("statusPages:detail.view")}
        >
          <ExternalLink className="sm:mr-2 h-4 w-4" />
          <span className="hidden sm:inline">
            {t("statusPages:detail.view")}
          </span>
        </Button>
      </a>
    </TooltipTrigger>
    <TooltipContent className="sm:hidden">
      {t("statusPages:detail.view")}
    </TooltipContent>
  </Tooltip>
  <Tooltip>
    <TooltipTrigger asChild>
      <Link
        to="/orgs/$org/status-pages/$statusPageUid/edit"
        params={{ org, statusPageUid }}
      >
        <Button
          variant="outline"
          size="sm"
          aria-label={t("statusPages:edit")}
        >
          <Pencil className="sm:mr-2 h-4 w-4" />
          <span className="hidden sm:inline">{t("statusPages:edit")}</span>
        </Button>
      </Link>
    </TooltipTrigger>
    <TooltipContent className="sm:hidden">
      {t("statusPages:edit")}
    </TooltipContent>
  </Tooltip>
</div>
```

`sm:hidden` on `TooltipContent` means: tooltip only fires below the
`sm` breakpoint. Above it the visible label is informative enough.

### 4. AddSectionDialog trigger (line 127–132 inside the file)

Current:

```tsx
<DialogTrigger asChild>
  <Button variant="outline" size="sm">
    <Plus className="mr-2 h-4 w-4" />
    {t("statusPages:sections.add")}
  </Button>
</DialogTrigger>
```

Target — icon-only square button + tooltip:

```tsx
<DialogTrigger asChild>
  <Tooltip>
    <TooltipTrigger asChild>
      <Button
        variant="outline"
        size="icon"
        aria-label={t("statusPages:sections.add")}
      >
        <Plus className="h-4 w-4" />
      </Button>
    </TooltipTrigger>
    <TooltipContent>{t("statusPages:sections.add")}</TooltipContent>
  </Tooltip>
</DialogTrigger>
```

The same component is rendered both at the top of the page (line ~535)
and in the empty-state card (line ~554). Both instances pick up the
new icon-only style automatically.

### 5. AddResourceDialog trigger (line 209–213 inside the file)

Current:

```tsx
<DialogTrigger asChild>
  <Button variant="ghost" size="sm">
    <Plus className="mr-1 h-3 w-3" />
    {t("statusPages:resources.add")}
  </Button>
</DialogTrigger>
```

Target — same pattern, kept `variant="ghost"` so it stays visually
lighter (it's nested inside a SectionCard):

```tsx
<DialogTrigger asChild>
  <Tooltip>
    <TooltipTrigger asChild>
      <Button
        variant="ghost"
        size="icon"
        aria-label={t("statusPages:resources.add")}
      >
        <Plus className="h-4 w-4" />
      </Button>
    </TooltipTrigger>
    <TooltipContent>{t("statusPages:resources.add")}</TooltipContent>
  </Tooltip>
</DialogTrigger>
```

> **Note on the user's "-" wording.** The original request said
> "+ Add Check should become -". This is treated as a typo for `+`.

### 6. TooltipProvider check

Radix `<Tooltip>` requires a `<TooltipProvider>` ancestor. Verify it
exists in `web/dash0/src/routes/__root.tsx` or `main.tsx`. If absent,
wrap the app shell with `<TooltipProvider delayDuration={300}>`.

## Files to modify

- `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`
  - Header layout (~L472–531)
  - `AddSectionDialog` trigger (~L127)
  - `AddResourceDialog` trigger (~L209)
  - Inline `StatusDot` + `VisibilityDot` helpers near the top of the
    file
  - Drop the unused `Badge` import if applicable
- `web/dash0/src/routes/__root.tsx` (or wherever the app shell lives)
  — only if `TooltipProvider` is missing

No new files. No new translation keys.

## Verification

Use Playwright (the repo has Playwright at `web/dash0/e2e/` — see the
stored memory `feedback_browser_testing.md`):

1. **Visual at 360 / 768 / 1280:**
   - Title row shows: back button → title → optional star → green dot
     (when enabled) → blue dot (when public).
   - Subtitle row: `/slug — description`.
   - Action buttons (View / Modify) anchored top-right at every
     viewport.
   - No `<Badge>` text "public" / "Activée" on the page.
2. **Tooltips:**
   - `Tab` to each dot → tooltip surfaces the same label that used to
     be a badge.
   - Hover/focus View/Modify on mobile (≤ 640 px) → tooltip surfaces
     the verb. Above 640 px → no tooltip (label visible).
3. **Add buttons:**
   - "Add section" is a square icon button with `+`. Hover/focus →
     tooltip "Ajouter une section" (FR) or "Add section" (EN).
   - "Add check" inside each section: same.
4. **Manual smoke as `admin@solidping.com`:**
   - Navigate to `/dash0/orgs/default/status-pages/{uid}`.
   - Toggle the page enabled/disabled and public/restricted via the
     edit form, return to detail — dots reflect the new state.
   - Click "+ Add section" → dialog opens.
   - Add a section, click "+ Add check" inside → dialog opens.

## Implementation plan

1. Confirm `<TooltipProvider>` exists at the app root (add if missing).
2. Add `StatusDot` + `VisibilityDot` inline helpers (no consumers yet).
3. Refactor header: title row + dots, subtitle row, top-right actions.
   Remove old `<Badge>` JSX. Drop unused `Badge` import.
4. Replace `AddSectionDialog` trigger with icon-only + Tooltip.
5. Replace `AddResourceDialog` trigger with icon-only + Tooltip.
6. Run Playwright smoke against the status-page detail route at the
   three viewports.
7. `make fmt && make lint-dash`.

## Critical files

- `web/dash0/src/routes/orgs/$org/status-pages.$statusPageUid.index.tsx`
  — entire file (both dialogs and the header live here).
- `specs/done/2026/05/2026-05-03-47-mobile-ux-improvements.md` — context
  on the just-merged work this spec partially supersedes.
- `web/dash0/src/components/ui/tooltip.tsx` — Radix Tooltip wrapper.
- `web/dash0/src/lib/utils.ts` — `cn` helper.
