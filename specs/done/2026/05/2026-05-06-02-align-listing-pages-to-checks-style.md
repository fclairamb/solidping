# Align dependencies / on-call / escalation-policies / channels listing pages to the checks-page style

## Context

The checks list at `/dash0/orgs/$org/checks` is the canonical "list of things"
page in dash0: a large icon-prefixed h1 with subtitle, a toolbar row with a
prefixed search input, optional filters, a refresh button, and a single
`rounded-md border` table. Four other listing pages already exist but each
arrived in its own style:

| Page | Path | Today's layout |
|---|---|---|
| Dependencies | `/orgs/$org/dependencies` | h1 + Card-wrapped Input + Table |
| On-call | `/orgs/$org/on-call` | h1 + 3-column Card grid (no table) |
| Escalation policies | `/orgs/$org/escalation-policies` | h1 + 3-column Card grid (no table) |
| Channels | `/orgs/$org/channels` | h1 (text-2xl) + Card-wrapped Input + Table |

They diverge on header size, container (Card vs. raw border), action-button
placement, search-icon prefix, refresh button, and empty/loading affordances.
This spec re-skins all four to match the checks-page pattern so the operator
moves between sections without re-learning the chrome each time.

The checks page is the reference, not the implementation: we reuse its
visual conventions inline. The shared `<PageHeader>` proposed in
`specs/todos/2026-05-06-01-dash0-design-reference-page.md` would be a nicer
substrate, but it doesn't ship in this spec — these pages stay as inline
JSX matching the checks page byte-for-byte. When `PageHeader` lands, a
follow-up sweep adopts it across the five pages in one go.

## Reference pattern (read from `web/dash0/src/routes/orgs/$org/checks.index.tsx`)

Header (L750–797):

```tsx
<div className="flex items-center justify-between">
  <div>
    <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
      <Icon className="h-7 w-7 text-muted-foreground" />
      {t("title")}
    </h1>
    <p className="text-muted-foreground">{t("subtitle")}</p>
  </div>
  <div className="flex items-center gap-2">{/* actions */}</div>
</div>
```

Toolbar (L799–852):

```tsx
<div className="flex flex-wrap items-center gap-4">
  <div className="relative flex-1 min-w-[200px] max-w-sm">
    <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
    <Input placeholder={t("search…")} value={search} onChange={…} className="pl-9" />
  </div>
  {/* optional filter Selects */}
  <Button variant="outline" size="icon" onClick={handleRefresh} disabled={isRefetching}>
    <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
  </Button>
</div>
```

Table (L278–305):

```tsx
<div className="rounded-md border">
  <Table>…</Table>
</div>
```

Loading: `Skeleton h-12 rounded-lg` rows (×6). Error: `<QueryErrorView>`. Empty:
centered muted text inside the same border container.

## Per-page changes

### 1. `routes/orgs/$org/dependencies.index.tsx`

Today: Card-wrapped, redundant CardTitle/Description that re-states the h1.
Filter input lacks the search-icon prefix.

- Drop the outer `<Card>` / `<CardHeader>` / `<CardContent>` wrapper. The h1
  block already conveys the title; the duplicate inside the card is dead
  weight.
- Add `Search` lucide icon prefix to the existing filter input; move it to
  the new toolbar row with a refresh button (`refetch()` from the existing
  `useDependencyGraph` hook). Keep `data-testid="dependencies-filter"`.
- Wrap the table in `<div className="rounded-md border">`.
- Loading state → 6 `Skeleton h-12 rounded-lg` rows (was 3 × `h-10`).
- Empty state → centered muted text inside the bordered container (was a
  `<p>` inside the Card). Reuse `t("dependencies:list.empty")`.
- Search behavior is unchanged (client-side filter on parent/child name+slug).

### 2. `routes/orgs/$org/on-call.index.tsx`

Today: 3-column Card grid. Each card shows name, timezone, rotation type,
currently-on-call. **No search.** (User-confirmed.)

- Replace the grid with a `rounded-md border` Table. Columns:
  1. **Name** — link to detail (`/orgs/$org/on-call/$slug`)
  2. **Timezone** — `schedule.timezone` (text-muted-foreground)
  3. **Rotation** — `t("oncall:detail.rotationDaily" | "rotationWeekly")`
  4. **Currently on call** — `schedule.currentlyOnCall?.name` or muted
     `t("oncall:list.noOneOnCall")`
  5. Trailing `w-[50px]` cell with `<MoreVertical>` dropdown (View, Edit,
     Delete) mirroring the checks-row dropdown. Wire delete via the existing
     `useDeleteOnCallSchedule` hook (verify name in `api/hooks.ts`; otherwise
     keep the action navigating to detail and let the user delete from
     there in this pass — note inline if so).
- Toolbar row contains only a refresh button (no search) so the row still
  exists for visual parity with the other pages — bare refresh button on
  the right.
- Header keeps the existing `Plus` "Create" action button on the right.
- Loading: 6 `Skeleton h-12 rounded-lg` rows.
- Empty: centered muted `t("oncall:list.empty")` inside the bordered
  container, instead of a Card with `py-10`.
- Add `data-testid="oncall-row"` on each `<TableRow>` and
  `data-testid="oncall-refresh"` on the refresh button to enable the e2e
  test below.
- New translation keys (en + fr + de + es):
  - `oncall:list.col.name` = "Name"
  - `oncall:list.col.timezone` = "Timezone"
  - `oncall:list.col.rotation` = "Rotation"
  - `oncall:list.col.currentlyOnCall` = "Currently on call"

### 3. `routes/orgs/$org/escalation-policies.index.tsx`

Today: 3-column Card grid. Each card shows name, description, repeat info.

- Replace the grid with a `rounded-md border` Table. Columns:
  1. **Name** — link to `/orgs/$org/escalation-policies/$slug`
  2. **Description** — `policy.description` (truncate to one line via
     `truncate max-w-[420px]`); empty cell if blank
  3. **Repeats** — when `policy.repeatMax > 0`, render `<Repeat />` icon +
     `t("escalation:list.repeats", { count, minutes })`; else "—"
  4. Trailing `w-[50px]` cell with `<MoreVertical>` dropdown (View, Edit,
     Delete via `useDeleteEscalationPolicy` if it exists — same caveat
     as on-call).
- Toolbar row with prefixed `Search` input (`q` filters by name +
  description, client-side over `policies` since the list is small) and
  refresh button.
- Header keeps the existing `Plus` "Create" action.
- Loading: 6 `Skeleton h-12 rounded-lg` rows.
- Empty (no policies at all): centered muted `t("escalation:list.empty")`
  inside the bordered container.
- Empty (search yields nothing): a different centered text — add
  `escalation:list.noMatches` = "No policies match your search."
- `data-testid="policy-row"` and `data-testid="policy-search"` for e2e.
- New translation keys (en + fr + de + es):
  - `escalation:list.col.name` = "Name"
  - `escalation:list.col.description` = "Description"
  - `escalation:list.col.repeats` = "Repeats"
  - `escalation:list.searchPlaceholder` = "Search by name…"
  - `escalation:list.noMatches` = "No policies match your search."

### 4. `routes/orgs/$org/channels.index.tsx`

Today: already a Card-wrapped Table with name/type/status/used-by/updated.
The closest of the four to the target — main work is shedding the Card
shell and matching the checks-page toolbar.

- Drop the `<Card>` / `<CardHeader>` / `<CardContent>` shell. Move the
  `<Input>` into the new toolbar row. Add `Search` lucide icon prefix
  and the `pl-9` className.
- Add a refresh button to the toolbar (`useConnections` is via TanStack
  Query; expose `refetch` and `isRefetching` from the hook return — the
  hook already returns the full query object via `useQuery`, just
  destructure them). Pattern matches `handleRefresh` in checks page L745.
- Wrap the `<Table>` in `<div className="rounded-md border">`.
- Bump h1 from `text-2xl` to `text-3xl font-bold tracking-tight flex
  items-center gap-2` with the `Bell` icon (consistent with sidebar).
  Keep the existing "+ New channel" action on the right.
- Header `subtitle` becomes a plain `<p className="text-muted-foreground">`
  outside the title block, matching the checks page exactly.
- `EmptyState` (no channels) keeps the type-quick-pick block but renders
  in place of the table (same conditional as today).
- Loading state → 6 `Skeleton h-12 rounded-lg` rows in the bordered
  container, instead of the centered spinner.
- Add `data-testid="channels-refresh"` on the refresh button. (Existing
  `Row` already has implicit selectors, no other testids needed.)

## Out of scope

- Server-side filtering / pagination for any of the four pages. They all
  return small lists (≤ a few dozen). Client-side filter is fine.
- Bulk actions, multi-select, sorting controls. Adding sort to the table
  is a follow-up — not part of "make it look like the checks page".
- Adopting the `<PageHeader>` shared component (separate spec, see
  2026-05-06-01).
- Re-skinning other listing pages: members, invitations, tokens, status
  pages, incidents, events. Those already use varying patterns; pick
  them off in their own specs once `<PageHeader>` exists.
- Changing the route shape (search-param state, etc.). On-call has
  no URL search state today; we are not adding it.
- Dropdown actions for delete on on-call / escalation-policies if their
  delete hooks don't already exist. Note inline at implementation time
  and gate behind hook existence; do not invent backend mutations in
  this spec.

## Verification

1. `make test-dash` and `make lint-dash` pass.
2. Manual sweep, dev mode (`make dev`):
   - Visit each of the four pages. Header is `text-3xl`, icon `h-7`,
     subtitle muted, action buttons right-aligned.
   - Each page's toolbar row shares the same height and spacing.
   - Tables sit inside `rounded-md border` (no Card shadow, no doubled
     title).
   - Refresh button spins while refetching.
   - Search (where present) filters in real-time and shows the
     "no matches" empty state without hiding the toolbar.
   - On-call list still shows current on-call name and rotation
     correctly per row.
   - Escalation policies still show the repeat info as a column.
3. Cross-page screenshot diff (manually): the four pages now look like
   siblings of the checks page, not strangers.
4. Playwright (extend existing e2e or add a thin spec
   `web/dash0/e2e/listing-pages-style.spec.ts`):
   - Per page: assert the h1 has `text-3xl` (read computed font-size or
     check class), the toolbar contains a search-icon-prefixed input
     where applicable, the table has the `rounded-md` `border` parent.
   - On-call: row count matches schedule count; clicking a row name
     navigates to detail.
   - Escalation policies: typing in the search filters rows; clearing
     restores them.
   - Dependencies: filter input still works (regression).
   - Channels: toggling a channel via the row dropdown still updates
     status (regression).

## Critical files

- `web/dash0/src/routes/orgs/$org/checks.index.tsx` — reference pattern,
  do not edit.
- `web/dash0/src/routes/orgs/$org/dependencies.index.tsx` — re-skin.
- `web/dash0/src/routes/orgs/$org/on-call.index.tsx` — re-skin (table
  conversion).
- `web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx` —
  re-skin (table conversion).
- `web/dash0/src/routes/orgs/$org/channels.index.tsx` — re-skin (Card
  shed, h1 bump).
- `web/dash0/src/locales/{en,fr,de,es}/oncall.json` — add `list.col.*`
  keys.
- `web/dash0/src/locales/{en,fr,de,es}/escalation.json` — add
  `list.col.*`, `list.searchPlaceholder`, `list.noMatches`.
- `web/dash0/src/api/hooks.ts` — confirm `useConnections`,
  `useOnCallSchedules`, `useEscalationPolicies`, `useDependencyGraph`
  expose `refetch` and `isRefetching` (they're plain `useQuery`
  returns, so they do — no hook change needed).

## Out of band

If the implementer notices on-call or escalation-policies don't have
delete mutations exposed in `api/hooks.ts`, **do not invent them in
this spec** — render the dropdown with View / Edit only and note it in
the PR description. A follow-up spec adds the missing mutations, since
backend delete endpoints already exist (`DELETE /api/v1/orgs/:org/
on-call-schedules/:slug` and the corresponding escalation-policies
route — confirm in `server/internal/handlers/...` before claiming).

## Implementation Plan

Pre-flight: confirmed `useDeleteOnCallSchedule` (line 2071) and
`useDeleteEscalationPolicy` (line 2282) both exist in
`web/dash0/src/api/hooks.ts`, so dropdowns can wire delete actions.

Concrete commit breakdown:

1. **Translations (single commit)** — add `oncall:list.col.{name,timezone,rotation,currentlyOnCall}`,
   `escalation:list.{col.{name,description,repeats},searchPlaceholder,noMatches}` keys
   in en + fr + de + es.

2. **Dependencies re-skin** — drop the Card shell, add Search-prefixed input
   + refresh button toolbar, wrap table in `rounded-md border`, swap
   loading skeletons to `h-12 rounded-lg ×6`, move empty state inside
   the bordered container.

3. **Channels re-skin** — drop the Card shell, bump h1 to `text-3xl
   font-bold tracking-tight flex items-center gap-2` with Bell icon,
   add Search-prefix on input + refresh button to toolbar, wrap table
   in `rounded-md border`, swap centered spinner for 6 skeleton rows
   inside the bordered container, add `data-testid="channels-refresh"`.

4. **On-call re-skin** — replace 3-col Card grid with `rounded-md
   border` Table (Name / Timezone / Rotation / Currently on call /
   actions). Toolbar = bare refresh button. Dropdown wires View /
   Edit / Delete via `useDeleteOnCallSchedule`. Add testids.

5. **Escalation-policies re-skin** — replace 3-col Card grid with
   `rounded-md border` Table (Name / Description / Repeats / actions).
   Toolbar = Search-prefixed input + refresh. Dropdown wires
   View / Edit / Delete via `useDeleteEscalationPolicy`. Add testids.

6. **e2e** — add `web/dash0/e2e/listing-pages-style.spec.ts` covering
   the four pages: h1 size class, search-icon prefix where applicable,
   table parent has `rounded-md` + `border`, on-call row click
   navigates to detail, escalation-policies search filters rows.

7. **QA** — `make build-backend build-dash0 lint-back test`. Iterate
   until clean.

8. **Completeness audit** — independent subagent verifies every per-page
   change matches the spec (toolbar shape, header sizing, table border
   wrap, skeleton count, testids, new translation keys).

9. **Archive + merge** — Move spec to `specs/done/2026/05/`, merge
   `feat/align-listing-pages-to-checks-style` into `main`.
