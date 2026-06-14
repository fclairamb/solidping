# Check detail: add a "Badges" button and give every action button a label (collapsing on mobile)

## Context

On a check detail page — e.g.
`https://solidping.k8xp.com/dash0/orgs/default/checks/c4bbb9bf-f83e-468d-85bc-0c3ceb749f24`
— two things are missing:

1. **No way to reach the badges page for this check.** The badges builder lives at
   `/orgs/$org/badges` and pre-selects a check via the `?check=` search param
   ([`web/dash0/src/routes/orgs/$org/badges.tsx:355-358`](../../web/dash0/src/routes/orgs/$org/badges.tsx)
   resolves it by uid, then slug). The badges page already links **back** to the check
   ([`badges.tsx:409-419`](../../web/dash0/src/routes/orgs/$org/badges.tsx), added in
   `specs/done/2026/06/2026-06-10-04-badges-link-back-to-check.md`), but there is **no
   forward link** from the check detail page to badges. Today you can only get there via
   the sidebar (`AppSidebar.tsx`) or the command menu, then re-pick the check.

2. **Every header action button is icon-only.** The toolbar at
   [`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:506-587`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)
   renders five `size="icon"` buttons with no visible text:

   | Button | Icon | Variant | Notes |
   |---|---|---|---|
   | Back | `ArrowLeft` | `ghost` | `aria-label` only |
   | Edit | `Pencil` | `outline` | `<Link>` wrapping `<Button size="icon">` |
   | Clone | `Copy` | `outline` | `title` attr only |
   | Refresh | `RefreshCw` | `outline` | **no `aria-label`** |
   | Delete | `Trash2` | `destructive` | `AlertDialogTrigger`, icon only |

   The request: buttons should show text labels on desktop and collapse to icon-only on
   mobile — exactly the pattern the design reference already documents
   (`design-reference.tsx`, "Action buttons (icon + label, mobile collapses to icon)" and
   "Detail page header" sections):

   ```tsx
   <Button variant="outline" aria-label="Edit">
     <Pencil className="h-4 w-4 sm:mr-2" />
     <span className="hidden sm:inline">Edit</span>
   </Button>
   ```

   Live examples of this exact pattern: the incident detail header
   ([`incidents.$incidentUid.tsx:657-663`](../../web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx))
   and the integrations list "New" button
   ([`integrations.index.tsx:104-111`](../../web/dash0/src/routes/orgs/$org/integrations.index.tsx)).

### Deliberate exception: the back arrow stays icon-only

The design reference and `specs/done/2026/06/2026-06-10-02-status-pages-detail-back-arrow-placement.md`
establish that the **back arrow is always icon-only ghost** — never paired with a "Back"
label — and leads the right-aligned action cluster. So "all buttons get text" applies to
the *action* buttons (Edit, Clone, Refresh, Badges, Delete); the leading back button keeps
its current icon-only ghost form. This keeps us consistent with every other detail page.

## Goals

- A **Badges** button in the check detail header navigates to
  `/orgs/$org/badges` with the current check pre-selected (`?check=<slug>`).
- Edit, Clone, Refresh, Badges, and Delete each show an icon **and** a text label on
  desktop, collapsing to icon-only below the `sm` breakpoint (`hidden sm:inline`), each
  with a permanent `aria-label`.
- The back arrow remains icon-only ghost (unchanged), per the established convention.
- Labels are translated across all four locales (en, de, es, fr).
- No regression to existing behaviour (clone, refresh spinner, delete confirm dialog).

## Out of scope

- Any change to the badges page itself or the badge backend endpoint.
- Re-styling buttons on other pages — this spec touches only the check detail header.
- Changing the back-button convention (it stays icon-only).

## Implementation

### 1. Add the `BadgeCheck` icon import

In [`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx),
add `BadgeCheck` to the existing `lucide-react` import (same icon the sidebar and badges
page use for badges).

### 2. Rework the header toolbar (`checks.$checkUid.index.tsx:506-587`)

Keep the leading back button icon-only. Convert the rest to icon + collapsing label, and
insert the new **Badges** button after **Clone**. Final cluster order:
**Back · Edit · Clone · Badges · Refresh · Delete**.

```tsx
<div className="flex items-center gap-2">
  {/* Back — stays icon-only ghost (convention) */}
  <Button
    variant="ghost"
    size="icon"
    aria-label={t("checks:detail.backToChecks") ?? "Back to checks"}
    onClick={() => navigate({ to: "/orgs/$org/checks", params: { org } })}
  >
    <ArrowLeft className="h-4 w-4" />
  </Button>

  {/* Edit — refactor Link-wrapping-Button into Button asChild so the label sits inside */}
  <Button asChild variant="outline" aria-label={t("checks:edit")}>
    <Link to="/orgs/$org/checks/$checkUid/edit" params={{ org, checkUid }}>
      <Pencil className="h-4 w-4 sm:mr-2" />
      <span className="hidden sm:inline">{t("checks:edit")}</span>
    </Link>
  </Button>

  {/* Clone */}
  <Button
    variant="outline"
    aria-label={t("checks:detail.clone") ?? "Clone"}
    disabled={cloneCheck.isPending}
    onClick={async () => { /* unchanged clone logic */ }}
  >
    <Copy className="h-4 w-4 sm:mr-2" />
    <span className="hidden sm:inline">{t("checks:detail.clone")}</span>
  </Button>

  {/* Badges — NEW: navigate to the badges builder with this check pre-selected */}
  <Button asChild variant="outline" aria-label={t("checks:detail.badges") ?? "Badges"}>
    <Link
      to="/orgs/$org/badges"
      params={{ org }}
      search={{ check: check.slug ?? checkUid }}
    >
      <BadgeCheck className="h-4 w-4 sm:mr-2" />
      <span className="hidden sm:inline">{t("checks:detail.badges")}</span>
    </Link>
  </Button>

  {/* Refresh — gains a label + aria-label; keep the spin animation */}
  <Button
    variant="outline"
    aria-label={t("checks:detail.refresh") ?? "Refresh"}
    onClick={() => refetch()}
    disabled={isRefetching}
  >
    <RefreshCw className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`} />
    <span className="hidden sm:inline">{t("checks:detail.refresh")}</span>
  </Button>

  {/* Delete — trigger button gains a label; dialog body unchanged */}
  <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
    <AlertDialogTrigger asChild>
      <Button variant="destructive" aria-label={t("checks:detail.delete") ?? "Delete"}>
        <Trash2 className="h-4 w-4 sm:mr-2" />
        <span className="hidden sm:inline">{t("checks:detail.delete")}</span>
      </Button>
    </AlertDialogTrigger>
    {/* ...existing AlertDialogContent unchanged... */}
  </AlertDialog>
</div>
```

Notes:
- **Drop `size="icon"`** on every labeled button so it grows to fit the text. The icon
  uses `sm:mr-2` so there is no trailing margin on mobile when the label is hidden — this
  matches `incidents.$incidentUid.tsx` and the design reference. The back button keeps
  `size="icon"`.
- **`?check=` value:** pass `check.slug ?? checkUid`. `handleCheckChange` in `badges.tsx`
  also prefers slug (`check?.slug || uid`), and `validateSearch` + `selectedCheck`
  resolution accept either, so passing the slug keeps URLs human-readable and round-trips
  cleanly. (`check` is the loaded check object already in scope — it is passed to
  `CheckSummaryCards check={check}` just below the header.)
- TanStack Router will type-check the `search={{ check }}` object against
  `badges.tsx`'s `BadgeSearch`; `check?: string` is optional so no other params are needed.

### 3. Add/confirm i18n labels in `web/dash0/src/locales/{en,de,es,fr}/checks.json`

Under the `detail` object. `detail.clone` and `detail.delete` already exist and are reused;
`checks:edit` (top-level `edit`) already exists for the Edit label. Add the two new keys:

| Key | en | fr | de | es |
|---|---|---|---|---|
| `detail.badges` | `Badges` | `Badges` | `Badges` | `Insignias` |
| `detail.refresh` | `Refresh` | `Actualiser` | `Aktualisieren` | `Actualizar` |

Keep all four locale files in sync (same keys present in each).

## Verification

1. **Reported check:** on `…/checks/c4bbb9bf-f83e-468d-85bc-0c3ceb749f24`, a **Badges**
   button appears in the header; clicking it lands on `/orgs/default/badges?check=<slug>`
   with that check already selected and its badge previewed.
2. **Desktop (≥640px):** Edit, Clone, Badges, Refresh, Delete each show icon + text; the
   back arrow remains icon-only.
3. **Mobile (<640px):** the same five action buttons collapse to icon-only; `aria-label`s
   remain so screen readers still announce each action. The header does not overflow.
4. **No regressions:** clone still navigates to the new check's edit page, refresh still
   spins while refetching, delete still opens the confirm dialog.
5. `bun run lint` and `bun run build` (type check) pass in `web/dash0`.

## Tests

Extend the check-detail Playwright coverage in `web/dash0/e2e/` (or add a focused spec):

- Assert the Badges button is present and its `href` resolves to
  `/orgs/$org/badges?check=…` for the check under test.
- At a desktop viewport, assert the action labels (`Edit`, `Clone`, `Badges`, `Refresh`,
  `Delete`) are visible.
- At a mobile viewport (e.g. 390px), assert the label `<span>`s are hidden while the
  buttons (by `aria-label`) remain present and clickable.

## Files referenced

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` — header toolbar (the change).
- `web/dash0/src/routes/orgs/$org/badges.tsx` — `?check=` resolution + the existing
  back-link (no change).
- `web/dash0/src/routes/orgs/$org/design-reference.tsx` — canonical icon+label / detail-header
  patterns being followed.
- `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx` — live reference implementation.
- `web/dash0/src/locales/{en,de,es,fr}/checks.json` — labels.

## Implementation Plan

1. Import `BadgeCheck` in `checks.$checkUid.index.tsx`.
2. Rewrite the header toolbar: back stays icon-only; Edit/Clone/Refresh/Delete gain
   `icon + <span className="hidden sm:inline">label</span>` + `aria-label` and drop
   `size="icon"`; add the new **Badges** button (asChild `<Link>` to `/orgs/$org/badges`
   with `search={{ check: check.slug ?? checkUid }}`) after Clone.
3. Add `detail.badges` and `detail.refresh` to all four `checks.json` locale files.
4. Add/extend a Playwright e2e for the Badges link and the desktop-label / mobile-icon
   behaviour.
5. `bun run lint` + `bun run build`; run the dash0 e2e for the check detail page.
