# Check detail header: collapse actions into an overflow menu much sooner on mobile

## Context

On a check detail page — e.g.
`http://localhost:4000/dash0/orgs/default/checks/94f591d4-2614-4c2b-9984-606357fa5c4e`
— the header action buttons **don't fit on narrow screens**: they get pushed off the right
edge (or squished/wrapped). The mobile view should drop down to far fewer icons, and it
should do so **much sooner** (at a wider breakpoint) than it does today.

Two root causes, both in the header at
[`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:421-588`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx):

1. **The header layout doesn't follow the canonical truncating pattern.** The outer row is
   `flex items-center justify-between` with **no `gap`**, the left/title block has **no
   `min-w-0 flex-1`**, and the `h1` has **no `truncate`**
   ([`checks.$checkUid.index.tsx:421-427`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)).
   So a long check name does not yield — it shoves the right-hand button cluster off-screen.
   The design reference's "Detail page header" section
   ([`design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx))
   prescribes `flex items-start justify-between gap-3`, a `min-w-0 flex-1` title block with a
   `truncate` `h1`, and a `flex gap-2 shrink-0` action cluster. This header predates that.

2. **Every action is an always-visible icon button.** The cluster
   ([`checks.$checkUid.index.tsx:507-587`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx))
   renders five buttons (Back, Edit, Clone, Refresh, Delete) at `size="icon"`, and the
   pending in-flight spec
   [`2026-06-15-04-check-detail-badges-button-and-labeled-actions.md`](2026-06-15-04-check-detail-badges-button-and-labeled-actions.md)
   adds a sixth (**Badges**) plus text labels. Six 36 px buttons + gaps + the status dot +
   the title simply do not fit a ~360 px phone, and nothing collapses them — only the
   slug/uid sub-links hide (`hidden sm:flex`).

There is an established overflow pattern to reuse: the checks list row collapses its actions
into a `MoreVertical` (⋯) `DropdownMenu`
([`checks.index.tsx:195-242`](../../web/dash0/src/routes/orgs/$org/checks.index.tsx)),
with the delete item rendered as `className="text-destructive"` + `Trash2`. The detail page
already holds the delete dialog in **controlled state** (`deleteOpen` / `setDeleteOpen`), so
it can be opened from either an inline button or a menu item.

## Goals

- On narrow screens the header never overflows; the title truncates instead of pushing
  buttons off-screen.
- Below the `md` breakpoint (768 px), the header shows only the **back arrow** plus a single
  **⋯ overflow menu** — every other action (Edit, Badges, Clone, Refresh, Delete) moves into
  that menu. (Far fewer icons, kicking in "much sooner" than today's never-collapse.)
- At `md` and up, the full inline toolbar is shown (with labels, per spec 04).
- Delete stays red + `Trash2` everywhere (inline destructive button on desktop;
  `text-destructive` menu item on mobile), and its confirm `AlertDialog` works from both.
- No behavioural regression: clone navigates to the new check, refresh shows its spinner,
  delete confirms.

## Out of scope

- The badges page and badge backend (untouched).
- Re-styling other pages' headers — only the check detail header.
- Container queries / new breakpoint tokens — use the existing Tailwind `md` switch.

## Relationship to spec 04

Spec 04 (in `specs/todos/`) adds the **Badges** button and gives each action an icon + label
that collapses at `sm`. This spec changes the *responsive strategy* for the same toolbar, so
the two must be reconciled — implement them together, or whichever lands second adopts the
ladder below. Net combined design:

| Width | Header actions |
|---|---|
| `< md` (mobile/small) | **Back** (icon ghost) · **⋯** overflow menu (Edit, Badges, Clone, Refresh, Delete) |
| `≥ md` (tablet/desktop) | Full inline toolbar: Back · Edit · Clone · Badges · Refresh · Delete, each **icon + label** |

The single `md` switch governs both "collapse to menu" and "show labels," superseding spec
04's `sm` label breakpoint (labels only ever render in the inline `md+` toolbar).

## Implementation

All in [`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx).

### 1. Fix the header container to the canonical truncating pattern (lines 421-506)

- Outer row → `flex items-start justify-between gap-3`.
- Title block wrapper → add `min-w-0 flex-1` so it can shrink.
- `h1` → add `truncate` and make it `text-2xl font-bold tracking-tight sm:text-3xl`.
- The action cluster (`div` at line 507) → add `shrink-0` (keep `flex items-center gap-2`).
- Leave the `hidden sm:flex` slug/uid sub-links as they are. The `type` / pending badges sit
  in the title row — verify they wrap/shrink gracefully once the title truncates; if they
  still force overflow on the smallest widths, gate the `type` badge behind `hidden sm:inline-flex`.

### 2. Add imports

Add `MoreVertical` to the `lucide-react` import (and `BadgeCheck` if spec 04 hasn't already),
and the dropdown primitives from `@/components/ui/dropdown-menu`:
`DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator`.

### 3. Decouple the delete dialog from its trigger

The delete `AlertDialog` is currently opened via `<AlertDialogTrigger asChild>`
([line 557](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)). Because it now
opens from two places (the desktop inline button and the mobile menu item), render it
**triggerless** and drive it purely from the existing controlled state:

```tsx
<AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
  <AlertDialogContent>{/* ...unchanged title/description/footer... */}</AlertDialogContent>
</AlertDialog>
```

Both call sites just do `onClick={() => setDeleteOpen(true)}`.

### 4. Rebuild the action cluster: always-on Back, `md+` inline toolbar, `<md` overflow menu

```tsx
<div className="flex items-center gap-2 shrink-0">
  {/* Back — always visible, icon-only ghost (convention) */}
  <Button
    variant="ghost"
    size="icon"
    aria-label={t("checks:detail.backToChecks") ?? "Back to checks"}
    onClick={() => navigate({ to: "/orgs/$org/checks", params: { org } })}
  >
    <ArrowLeft className="h-4 w-4" />
  </Button>

  {/* Full inline toolbar — md and up (icon + label per spec 04) */}
  <div className="hidden items-center gap-2 md:flex">
    <Button asChild variant="outline" aria-label={t("checks:edit")}>
      <Link to="/orgs/$org/checks/$checkUid/edit" params={{ org, checkUid }}>
        <Pencil className="h-4 w-4 sm:mr-2" />
        <span className="hidden sm:inline">{t("checks:edit")}</span>
      </Link>
    </Button>
    <Button variant="outline" aria-label={t("checks:detail.clone")} disabled={cloneCheck.isPending} onClick={handleClone}>
      <Copy className="h-4 w-4 sm:mr-2" />
      <span className="hidden sm:inline">{t("checks:detail.clone")}</span>
    </Button>
    {/* Badges (from spec 04) */}
    <Button asChild variant="outline" aria-label={t("checks:detail.badges")}>
      <Link to="/orgs/$org/badges" params={{ org }} search={{ check: check.slug ?? checkUid }}>
        <BadgeCheck className="h-4 w-4 sm:mr-2" />
        <span className="hidden sm:inline">{t("checks:detail.badges")}</span>
      </Link>
    </Button>
    <Button variant="outline" aria-label={t("checks:detail.refresh")} onClick={() => refetch()} disabled={isRefetching}>
      <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
      <span className="hidden sm:inline">{t("checks:detail.refresh")}</span>
    </Button>
    <Button variant="destructive" aria-label={t("checks:detail.delete")} onClick={() => setDeleteOpen(true)}>
      <Trash2 className="h-4 w-4 sm:mr-2" />
      <span className="hidden sm:inline">{t("checks:detail.delete")}</span>
    </Button>
  </div>

  {/* Compact overflow — below md */}
  <div className="md:hidden">
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="icon" aria-label={t("checks:detail.moreActions") ?? "More actions"}>
          <MoreVertical className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem asChild>
          <Link to="/orgs/$org/checks/$checkUid/edit" params={{ org, checkUid }}>
            <Pencil className="mr-2 h-4 w-4" />{t("checks:edit")}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to="/orgs/$org/badges" params={{ org }} search={{ check: check.slug ?? checkUid }}>
            <BadgeCheck className="mr-2 h-4 w-4" />{t("checks:detail.badges")}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem disabled={cloneCheck.isPending} onClick={handleClone}>
          <Copy className="mr-2 h-4 w-4" />{t("checks:detail.clone")}
        </DropdownMenuItem>
        <DropdownMenuItem disabled={isRefetching} onClick={() => refetch()}>
          <RefreshCw className={`mr-2 h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />{t("checks:detail.refresh")}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem className="text-destructive focus:text-destructive" onClick={() => setDeleteOpen(true)}>
          <Trash2 className="mr-2 h-4 w-4" />{t("checks:detail.delete")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  </div>

  {/* Triggerless, controlled delete dialog (see step 3) */}
  <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
    <AlertDialogContent>{/* unchanged */}</AlertDialogContent>
  </AlertDialog>
</div>
```

Extract the existing inline clone handler into a `handleClone` callback so the inline button
and the menu item share it (avoids duplicating the try/catch + navigate logic).

### 5. i18n

Add `detail.moreActions` to `web/dash0/src/locales/{en,de,es,fr}/checks.json`
(en `More actions`, fr `Plus d'actions`, de `Weitere Aktionen`, es `Más acciones`). Reuse the
`detail.badges` / `detail.refresh` keys added by spec 04 (add them here if spec 04 is not
landed first).

## Verification

1. **Reported check** (`…/checks/94f591d4-2614-4c2b-9984-606357fa5c4e`) at a mobile width
   (≤ 400 px): the header shows only the back arrow + a ⋯ button — no overflow, even with a
   long check name (the title truncates). The ⋯ menu lists Edit, Badges, Clone, Refresh,
   Delete.
2. At ≥ 768 px (`md`): the full inline toolbar shows; the ⋯ button is gone.
3. Delete works from both surfaces: the desktop inline red button and the mobile menu item
   both open the same confirm dialog and delete the check.
4. Clone (both surfaces) navigates to the new check's edit page; Refresh spins while
   refetching in both.
5. `bun run lint` and `bun run build` pass in `web/dash0`.

## Tests

Extend the check-detail Playwright coverage in `web/dash0/e2e/`:

- **Mobile viewport (e.g. 390 px):** assert the ⋯ trigger (`aria-label` "More actions") is
  visible and the inline Edit/Clone/Refresh/Delete buttons are not; open the menu and assert
  it contains Edit, Badges, Clone, Refresh, Delete; assert the header does not overflow
  horizontally for a long-named check.
- **Desktop viewport (e.g. 1280 px):** assert the inline toolbar is visible and the ⋯ trigger
  is hidden.
- Assert delete-from-menu opens the confirm dialog.

## Files referenced

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — header (the change).
- `web/dash0/src/routes/orgs/$org/checks.index.tsx:195-242` — overflow `DropdownMenu` pattern to mirror.
- `web/dash0/src/routes/orgs/$org/design-reference.tsx` — canonical detail-header + row-action conventions.
- `web/dash0/src/components/ui/dropdown-menu.tsx` — dropdown primitives.
- `specs/todos/2026-06-15-04-check-detail-badges-button-and-labeled-actions.md` — companion spec (Badges button + labels).
- `web/dash0/src/locales/{en,de,es,fr}/checks.json` — labels.

## Implementation Plan

1. Apply the canonical truncating header layout (`min-w-0 flex-1` + `truncate` h1, `gap-3`, `shrink-0` cluster).
2. Add `MoreVertical` + dropdown-menu imports (and `BadgeCheck` if needed).
3. Make the delete `AlertDialog` triggerless/controlled; extract `handleClone`.
4. Build the three-part cluster: always-on Back, `hidden md:flex` inline toolbar, `md:hidden` ⋯ overflow menu (with `text-destructive` delete item).
5. Add `detail.moreActions` (and reconcile `detail.badges`/`detail.refresh`) across the four locale files.
6. Add the Playwright mobile/desktop assertions; `bun run lint` + `bun run build`.
