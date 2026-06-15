# Check detail header: action buttons stay inline, shrink to icon-only instead of collapsing

## Context

On the check detail page — e.g.
`https://solidping.k8xp.com/dash0/orgs/default/checks/99f16c92-a2cf-4532-a0da-f2712270b69a`
— the current responsive design collapses all actions below the `md` breakpoint (768 px) into
a single `MoreVertical` (⋯) overflow `DropdownMenu`. This was shipped in spec
[`2026-06-15-05-check-detail-header-mobile-overflow-menu.md`](../done/2026/06/2026-06-15-05-check-detail-header-mobile-overflow-menu.md).

The user's preference is different: **buttons should never collapse into a hidden menu**. When
space is tight they should become compact **icon-only** buttons (text labels disappear, icons
stay). The user should always be able to see and act on the toolbar without first opening a
dropdown.

Relevant source block:
[`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:557-696`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)

```
{/* Full inline toolbar — md and up (icon + label) */}
<div className="hidden items-center gap-2 md:flex">   ← hide below md
  …6 buttons…
</div>

{/* Compact overflow — below md */}
<div className="md:hidden">                            ← drop into menu below md
  <DropdownMenu>…</DropdownMenu>
</div>
```

## Goals

- The 6 action buttons (Edit, Enable/Disable, Clone, Badges, Refresh, Delete) are **always
  visible inline** — never collapsed into a dropdown.
- At wide viewports (`≥ lg`, 1024 px) each button shows **icon + text label**.
- Below `lg`, each button shows **icon only** — compact but still individually tappable.
- The back arrow (`ArrowLeft`) behaviour is unchanged (always icon-only, ghost variant).
- Delete stays `variant="destructive"` + `Trash2` icon in all states.
- No regression to clone, refresh, enable/disable, or delete functionality.

## Out of scope

- Changing any other page's header or button pattern.
- Container queries or new Tailwind breakpoints.
- The `AlertDialog` confirm flow — it is already controlled state, keep it as-is.

## Implementation

All changes in
[`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx).

### 1. Remove the `md:hidden` overflow dropdown block

Delete the entire `<div className="md:hidden">…</div>` block (the `DropdownMenu` and its
contents). This also removes the `MoreVertical` import if it is no longer used elsewhere in
the file.

### 2. Make the inline toolbar always visible

Change:
```tsx
<div className="hidden items-center gap-2 md:flex">
```
to:
```tsx
<div className="flex items-center gap-2">
```

### 3. Shift the text-label breakpoint from `sm` to `lg`

Inside every button in the toolbar, the label span currently reads:
```tsx
<span className="hidden sm:inline">…</span>
```
Change all occurrences to:
```tsx
<span className="hidden lg:inline">…</span>
```

Do the same for the responsive icon right-margin — change `sm:mr-2` → `lg:mr-2` on every
icon inside these buttons so the spacing only activates when the label is actually present.

### 4. Make icon-only buttons compact below `lg`

When there is no text label, the default Button padding (`px-4 py-2`) wastes horizontal space
and makes the button look odd. Add responsive sizing so buttons are icon-sized below `lg` and
return to their natural width above:

```tsx
<Button
  variant="outline"
  size="icon"
  className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"
  …
>
  <Pencil className="h-4 w-4 lg:mr-2" />
  <span className="hidden lg:inline">{t("checks:edit")}</span>
</Button>
```

Apply the same `size="icon" className="lg:h-9 lg:w-auto lg:px-4 lg:py-2"` pattern to every
button in the toolbar (Edit, Enable/Disable, Clone, Badges, Refresh, Delete). The Delete
button keeps `variant="destructive"`.

For the `asChild` buttons that wrap a `<Link>`, the `size` + `className` go on `<Button>`
(not on the Link) — shadcn's `asChild` forwards className/style correctly.

### 5. Clean up now-unused imports

Remove `DropdownMenu`, `DropdownMenuContent`, `DropdownMenuItem`, `DropdownMenuSeparator`,
`DropdownMenuTrigger`, and `MoreVertical` from the import list if they are not used elsewhere
in the file.

## Verification

1. Open the check detail page at a mobile width (e.g. 375 px): **all 6 action buttons are
   visible** as icon-only squares; no ⋯ button; no horizontal overflow (title truncates).
2. At a tablet width (e.g. 768 px, `md` but below `lg`): buttons still icon-only and inline.
3. At 1024 px+ (`lg`): each button shows icon + text label.
4. Delete from the inline button opens the confirm dialog and removes the check.
5. Enable/Disable, Clone, Refresh work as before.
6. `bun run lint` and `bun run build` pass in `web/dash0`.

## Files referenced

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — the only file changed.
