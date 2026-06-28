# Network Discovery scan list: clickable rows, mobile-collapsed "Start new scan", and a generic "Details" column

## Context

The **Network Discovery** index
([`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx),
route `/orgs/$org/discovery`) lists scans in a table with columns
**Source · Status · CIDRs · Started at · _(View checks)_**. Four things are off
versus the rest of the dashboard:

1. **The "Start new scan" header button never collapses on mobile.** It always
   renders icon + full label
   ([`discovery.index.tsx:135-144`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx#L135)),
   even though the **Refresh** button immediately above it already collapses to
   icon-only below the `sm` breakpoint, and every other primary "New X" button does
   too (e.g. `checks.index.tsx` "New check":
   `<Plus className="sm:mr-2 h-4 w-4" /><span className="hidden sm:inline">…</span>`).

2. **Rows are not clickable.** Navigation to a scan's detail page is only possible
   via a trailing **"View checks"** ghost link in the last cell
   ([`discovery.index.tsx:86-92`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx#L86)).

3. **The "CIDRs" column is LAN-specific and mostly empty.** It renders
   `scan.config.cidrs` joined, or `—`
   ([`discovery.index.tsx:78-82`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx#L78)).
   Only `lan` scans have CIDRs; `container`, `kubernetes`, and `freebox` scans always
   show `—` (see the screenshot: a Container scan with `—`). The column header is the
   wrong concept for a multi-source list.

4. **The "View checks" link/column is redundant** with a clickable row.

### Per-source scan config (drives the new "Details" column)

`DiscoveryScan.config` is a `Record<string, unknown>`
([`api/hooks.ts:3206`](../../web/dash0/src/api/hooks.ts#L3206)); the
source is derived from `scan.type` by the existing `scanSource()` helper
([`discovery.index.tsx:52-63`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx#L52)).
The new-scan form
([`discovery.new.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.new.tsx))
shows what each source stores:

| Source (`scanSource`) | Config field | Example "Details" cell |
|---|---|---|
| `lan` | `cidrs: string[]` | `192.168.1.0/24, 10.0.0.0/24` |
| `container` | `hosts: string[]` | the container host(s), or `—` |
| `kubernetes` | `namespaces: string[]` (empty ⇒ all) | `default, kube-system` or **All namespaces** |
| `freebox` | _(only `channelUid`)_ | `—` |

## Decision

Bring the discovery list in line with the rest of dash0 with four scoped changes,
all inside `discovery.index.tsx` (+ the four `discovery.json` locale files):

1. **Collapse "Start new scan" to icon-only below `sm`**, exactly like the adjacent
   Refresh button and the canonical `checks.index.tsx` "New check" button. Keep the
   accessible name at all widths via `aria-label`.
2. **Make each scan row clickable**, navigating to the scan detail route
   `/orgs/$org/discovery/$jobUid` (the same target as today's "View checks" link).
3. **Replace the "CIDRs" column with a generic "Details" column** that summarizes the
   scan's config per source (CIDRs for LAN, hosts for container, namespaces for
   kubernetes, `—` otherwise).
4. **Remove the "View checks" link and its column** (header + cell), now redundant
   with the clickable row.

## Goals

- On a narrow viewport (< `sm`, 640px) the header shows **two icon-only buttons**
  (Refresh, then a `+`); from `sm` up both reveal their labels. The `+` button keeps
  the accessible name "Start new scan" at every width.
- Clicking (or pressing Enter/Space on) **anywhere in a scan row** opens that scan's
  detail page; the row shows a pointer cursor and a hover background.
- The table columns are **Source · Status · Details · Started at** — no "CIDRs" and
  no "View checks" column.
- The **Details** cell is meaningful for every source (never a bare `—` purely
  because the column was LAN-only).
- Detail-page navigation target is unchanged (`/orgs/$org/discovery/$jobUid`); the
  scan detail page itself is untouched.

## Out of scope

- The **new-scan form** (`discovery.new.tsx`) and its CIDR/host/namespace inputs —
  the form keeps its `cidrsLabel`/`cidrsPlaceholder`/`cidrsHelp` keys. Only the
  **list column** named "CIDRs" goes away.
- The scan **detail** page (`discovery.$jobUid.index.tsx`) — unchanged.
- The source filter, refresh behaviour, empty/loading states, and status badges.
- The legacy `web/dash` app and `web/status0`.

## Implementation

All edits in
[`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx)
unless noted.

### 1. Mobile-collapse the "Start new scan" button (lines ~135-144)

```tsx
// before
<Button asChild>
  <Link to="/orgs/$org/discovery/new" params={{ org }} search={{ method: "lan" }}>
    <Plus className="h-4 w-4 mr-1" />
    {t("newScan")}
  </Link>
</Button>
// after
<Button asChild>
  <Link
    to="/orgs/$org/discovery/new"
    params={{ org }}
    search={{ method: "lan" }}
    aria-label={t("newScan")}
  >
    <Plus className="sm:mr-2 h-4 w-4" />
    <span className="hidden sm:inline">{t("newScan")}</span>
  </Link>
</Button>
```

The `aria-label` on the `Link` keeps the rendered `<a>`'s accessible name as
"Start new scan" when the label span is hidden (also keeps the existing E2E
`getByRole("link", { name: /start new scan/i })` matching).

### 2 + 4. Clickable rows; drop the "View checks" cell (the `ScanRow` component, lines ~65-95)

Add `useNavigate` to the router import:

```tsx
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
```

Rewrite `ScanRow` so the whole `<TableRow>` navigates and the trailing
"View checks" `<TableCell>` is gone:

```tsx
function ScanRow({ scan, org }: { scan: DiscoveryScan; org: string }) {
  const { t } = useTranslation("discovery");
  const navigate = useNavigate();
  const statusLabel = t(`scanStatus.${scan.status}`, scan.status);
  const source = scanSource(scan.type);

  const open = () =>
    void navigate({
      to: "/orgs/$org/discovery/$jobUid",
      params: { org, jobUid: scan.uid },
    });

  // Generic, source-aware summary that replaces the old LAN-only "CIDRs" cell.
  const cfg = (scan.config ?? {}) as Record<string, unknown>;
  const joinList = (key: string) =>
    Array.isArray(cfg[key]) ? (cfg[key] as unknown[]).map(String).join(", ") : "";
  const details =
    source === "lan"
      ? joinList("cidrs") || "—"
      : source === "container"
        ? joinList("hosts") || "—"
        : source === "kubernetes"
          ? joinList("namespaces") || t("allNamespaces")
          : "—";

  return (
    <TableRow
      className="cursor-pointer hover:bg-muted/50"
      role="link"
      tabIndex={0}
      onClick={open}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          open();
        }
      }}
    >
      <TableCell>
        <Badge variant="outline">{t(`sourceLabel.${source}`, source)}</Badge>
      </TableCell>
      <TableCell>
        <Badge variant={statusBadgeVariant(scan.status)}>{statusLabel}</Badge>
      </TableCell>
      <TableCell className="text-xs text-muted-foreground">{details}</TableCell>
      <TableCell className="text-xs text-muted-foreground">
        {new Date(scan.createdAt).toLocaleString()}
      </TableCell>
    </TableRow>
  );
}
```

Because the "View checks" link was the row's only interactive element, the row now
contains no nested links/buttons, so a whole-row `onClick` is unambiguous. The
`role="link"` + `tabIndex={0}` + Enter/Space handler keep it keyboard-accessible.
(`Link` is still imported and used by the header "Start new scan" button, so the
import stays.)

### 3. Column headers (the `<TableHeader>` block, lines ~183-191)

```tsx
<TableRow>
  <TableHead>{t("source")}</TableHead>
  <TableHead>{t("status")}</TableHead>
  <TableHead>{t("details")}</TableHead>   {/* was t("cidrs") */}
  <TableHead>{t("startedAt")}</TableHead>
  {/* removed: trailing empty <TableHead /> for the View-checks column */}
</TableRow>
```

### 5. i18n (all four `web/dash0/src/locales/{en,fr,de,es}/discovery.json`)

- **Add** a `"details"` key (place next to the existing `"cidrs"`/`"startedAt"`):
  - en `Details` · fr `Détails` · de `Details` · es `Detalles`
- **Add** an `"allNamespaces"` key (used when a kubernetes scan targets all
  namespaces):
  - en `All namespaces` · fr `Tous les espaces de noms` · de `Alle Namespaces` ·
    es `Todos los espacios de nombres`
- **Remove** the now-unused `"viewChecks"` key (line 17 in each file) and the bare
  `"cidrs"` column-header key (line 76 in each file). Do **not** touch
  `cidrsLabel`/`cidrsPlaceholder`/`cidrsHelp` — those belong to the new-scan form.

## Design reference

The dashboard has no documented **clickable table row** primitive yet
([`design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
shows clickable *tiles* but not rows). Per the project convention ("if a needed
pattern is missing, add it to the reference page as part of your change"), add a
small **"Clickable table row"** example to `design-reference.tsx` demonstrating the
`cursor-pointer hover:bg-muted/50` + `role="link"`/`tabIndex`/`onKeyDown` +
`onClick={navigate(...)}` pattern, so future list pages copy it. The mobile
icon-collapse button pattern is already represented (Refresh/New-check) and needs no
new entry.

## Verification

With `make dev-test` running, open `/dash0/orgs/{org}/discovery` (the `default` org
already has a Container scan):

1. **Desktop width:** header shows `↻ Refresh` and `+ Start new scan` with labels;
   table columns read **Source · Status · Details · Started at** with no
   "View checks" column.
2. **Mobile width (≈390px):** both header buttons are icon-only (`↻` then `+`), no
   leftover left-margin gap; the `+` button still has the accessible name
   "Start new scan".
3. **Row click:** clicking anywhere on the Container row (and tabbing to it +
   pressing Enter) navigates to `…/discovery/<uid>` ("Scan details" heading); the row
   shows a pointer cursor and hover background.
4. **Details cell:** a LAN scan shows its CIDRs; a kubernetes scan with no namespaces
   shows "All namespaces"; the Container scan shows its host(s) or `—`.
5. Switching language (fr/de/es) shows the translated **Details** header and
   **All namespaces** value; no missing-key console warning.

## Tests

- The existing discovery E2E suite
  ([`e2e/discovery.spec.ts`](../../web/dash0/e2e/discovery.spec.ts),
  `discovery-promote.spec.ts`, `discovery-scan-method.spec.ts`) reaches scan detail
  via direct `page.goto(.../discovery/<uid>)` (never by clicking "View checks") and
  references the header button by accessible name (`/start new scan/i`), preserved by
  the new `aria-label`. The CIDR assertions there target the **new-scan form's** CIDR
  textarea, not the list column. So the suite should stay green — run `make test-dash`.
- **Add coverage** to `e2e/discovery.spec.ts` (mirrors the existing "detail header
  refresh button is icon-only on mobile" test):
  - At a mobile viewport (`setViewportSize({ width: 390 })`) the index "Start new
    scan" button is present by accessible name but its **text label is hidden**
    (`getByText(/start new scan/i)` hidden), and visible at ≥640px.
  - Clicking the first scan row on the index navigates to a
    `/\/discovery\/[0-9a-f-]{36}$/` URL with the "Scan details" heading.
  - The index table has **no** "View checks" link and **no** "CIDRs" column header.
- `bun run lint` and `bun run build` (tsc) in `web/dash0` pass with no new errors
  (the unused `t("cidrs")`/`viewChecks` usages are removed; no import becomes unused).

## Files referenced

- [`web/dash0/src/routes/orgs/$org/discovery.index.tsx`](../../web/dash0/src/routes/orgs/$org/discovery.index.tsx)
  — all four UI changes (button, `ScanRow`, headers, `useNavigate` import).
- [`web/dash0/src/locales/en/discovery.json`](../../web/dash0/src/locales/en/discovery.json)
  (+ `fr`/`de`/`es`) — add `details` + `allNamespaces`; remove `viewChecks` + `cidrs`.
- [`web/dash0/src/routes/orgs/$org/checks.index.tsx`](../../web/dash0/src/routes/orgs/$org/checks.index.tsx#L763)
  — canonical mobile-collapse "New X" button pattern (reference, do not change).
- [`web/dash0/src/routes/orgs/$org/jobs.index.tsx`](../../web/dash0/src/routes/orgs/$org/jobs.index.tsx#L428)
  — existing `cursor-pointer` scan-style row precedent (reference).
- [`web/dash0/src/routes/orgs/$org/design-reference.tsx`](../../web/dash0/src/routes/orgs/$org/design-reference.tsx)
  — add the "Clickable table row" example.
- [`web/dash0/e2e/discovery.spec.ts`](../../web/dash0/e2e/discovery.spec.ts)
  — extend with the mobile-button + row-click + column assertions.

## Implementation Plan

### Files to touch
1. `web/dash0/src/routes/orgs/$org/discovery.index.tsx` — all four UI changes:
   - Add `useNavigate` to the `@tanstack/react-router` import (keep `Link` — still
     used by the header "Start new scan" button).
   - **Button:** collapse "Start new scan" to icon-only below `sm` (mirror the
     adjacent Refresh button + the canonical `checks.index.tsx` "New check"):
     `<Plus className="sm:mr-2 h-4 w-4" /><span className="hidden sm:inline">…</span>`,
     plus `aria-label={t("newScan")}` on the `Link` so the accessible name survives.
     Preserve the existing `search={{ method: "lan" }}` discovery-routing convention.
   - **ScanRow:** whole-`<TableRow>` navigates via `navigate({ to:
     "/orgs/$org/discovery/$jobUid", params:{ org, jobUid: scan.uid } })`, with
     `className="cursor-pointer hover:bg-muted/50"`, `role="link"`, `tabIndex={0}`,
     and an `onKeyDown` Enter/Space handler. Remove the trailing "View checks"
     `<TableCell>` (and its `<Button asChild><Link>`).
   - **Details cell:** replace the LAN-only CIDRs cell with a generic, source-aware
     summary — `cidrs` (lan) / `hosts` (container) / `namespaces` (kubernetes, empty
     ⇒ `t("allNamespaces")`) / `—` (freebox & default). Read `scan.config` as
     `Record<string, unknown>`, join array values with `, `.
   - **Header row:** `t("cidrs")` → `t("details")`; drop the trailing empty
     `<TableHead />` (View-checks column).
2. `web/dash0/src/locales/{en,fr,de,es}/discovery.json` — add `details` +
   `allNamespaces`; remove `viewChecks` and the bare `cidrs` column header.
   Leave `cidrsLabel`/`cidrsPlaceholder`/`cidrsHelp` (new-scan form) untouched.
3. `web/dash0/src/routes/orgs/$org/design-reference.tsx` — add a "Clickable rows"
   variant to the existing `DataDisplaySection` table grid, demonstrating
   `cursor-pointer hover:bg-muted/50` + `role="link"`/`tabIndex`/`onKeyDown` +
   `onClick`, and document the real `useNavigate` target in the section's
   `CodeSnippet` (the static reference page has no live route target, so the mock
   row uses an inert handler — no unused `useNavigate` import added there).
4. `web/dash0/e2e/discovery.spec.ts` — add three tests:
   - mobile (`setViewportSize({ width: 390 })`): "Start new scan" present by role
     but its text label hidden; visible again at ≥640px.
   - clicking the first scan row navigates to `/\/discovery\/[0-9a-f-]{36}$/` with
     the "Scan details" heading.
   - index table has no "View checks" link and no "CIDRs" column header.

### Responsive behavior
- Header: two icon-only buttons (`↻`, `+`) below `sm`; labels reveal from `sm` up.
  `aria-label` keeps both names at every width.
- Table: unchanged column count drops from 5 → 4 (Source · Status · Details ·
  Started at); the shared `<Table>` primitive already scrolls/stacks responsively.

### Commits (granular)
feat(button) → feat(clickable rows + drop View-checks + Details cell + headers) →
feat(i18n) → feat(design-reference) → test(e2e). QA fixes folded in as needed.

### Tests / QA
`make build-dash0`, `make build-backend`, `make lint-back`, `make test`,
`cd web/dash0 && bun run lint` (no NEW errors). E2E: run only `discovery.spec.ts`
(authored; run if a server is available, else authored-but-not-run).
