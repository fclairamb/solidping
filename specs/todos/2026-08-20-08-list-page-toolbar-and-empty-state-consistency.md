---
model: sonnet
effort: medium
---

# List-page consistency: Refresh-button placement and empty states — fix the drifted pages and document both conventions in the design reference

> Merged from `2026-08-20-08-status-pages-refresh-button-placement.md` and
> `2026-08-20-09-empty-state-design-consistency.md` — same root cause (the
> design reference under-specifies list-page conventions), same files touched.

## Problem

Two conventions are missing from the design reference
([design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx)),
and the index pages have drifted accordingly.

### A. Refresh-button placement is split between two patterns

On `/dash0/orgs/$org/status-pages` the **Refresh** button sits in the
`PageHeader` `actions` slot (top right, next to "New status page"), while the
search input sits alone in its own row below the header —
[status-pages.index.tsx:219-249](web/dash0/src/routes/orgs/$org/status-pages.index.tsx:219).
Refresh is a data-affecting toolbar control, not a page-level action; it should
sit to the **right of the search input**, in the same row.

The codebase is split between two placements:

- **Toolbar placement (desired)** — `integrations.index.tsx` (~line 198) puts
  Refresh directly to the right of its search input in a shared flex row;
  `checks.index.tsx` (~line 1550) also keeps Refresh in the filter toolbar next
  to search/status filters.
- **Header placement (what status-pages does)** — `status-pages.index.tsx`,
  `maintenance-windows.index.tsx` (~line 286) put Refresh in `PageHeader`
  `actions` alongside the primary "New …" button.
- **Special case** — **on-call** has no search field, yet its Refresh button
  occupies a whole `justify-end` row of its own below the header
  ([on-call.index.tsx:103-114](web/dash0/src/routes/orgs/$org/on-call.index.tsx:103)).
  With no toolbar row to join, it should sit in the `PageHeader` `actions`
  slot next to "Create schedule".

### B. The empty-state pattern is under-specified and most list pages don't follow it

The design reference's "Empty state" example
([design-reference.tsx:2921-2938](web/dash0/src/routes/orgs/$org/design-reference.tsx:2921))
shows only the minimal shape — card surface, muted icon circle, title, hint. It
does not document:

- the **primary CTA button** ("Create your first …" linking to the create
  route) that a truly empty list should carry;
- the **"no search matches" variant** (Search icon, no CTA) used when a
  filter hides all rows;
- that the page's toolbar (search input, Refresh) stays rendered above the
  empty state.

Because the pattern is under-specified, the index pages have drifted into four
different renderings:

| Page | Current empty state | Verdict |
|---|---|---|
| [escalation-policies.index.tsx:181-201](web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx:181) | Card + icon circle + title + hint, plus a proper no-matches variant | The model ("perfect" per report) — only missing the CTA button |
| [on-call.index.tsx:123-140](web/dash0/src/routes/orgs/$org/on-call.index.tsx:123) | Icon circle + title + hint + CTA button inside the card wrapper | Structure OK; the hint at line 131 is a **hardcoded English string** (not i18n'd), and Refresh sits alone in its own row (lines 103-114) |
| [status-pages.index.tsx:288-292](web/dash0/src/routes/orgs/$org/status-pages.index.tsx:288) | Bare `text-center py-12` paragraph — no card, no icon, no hint, no CTA | Doesn't respect the design at all |
| [status-updates.index.tsx:424-428](web/dash0/src/routes/orgs/$org/status-updates.index.tsx:424) | Bare centered Megaphone icon + one line — no card, no title/hint/CTA | Doesn't respect the design at all |
| [slos.index.tsx:170-177](web/dash0/src/routes/orgs/$org/slos.index.tsx:170) | `rounded-lg border border-dashed p-8` — dashed border, no icon, no shadow-card, no CTA | Doesn't respect the design |
| [me.notifications.tsx:79-92](web/dash0/src/routes/orgs/$org/me.notifications.tsx:79) | Plain muted paragraph inside a `Card`/`CardContent` | Doesn't respect the design |
| [maintenance-windows.index.tsx:364-376](web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx:364) | Card + icon + title + hint, plus no-matches variant | Fine — its only issue is the Refresh placement, covered by part A |

Note / open question: the report says the on-call empty state "should have a
button", but the code has shipped one since 2026-08-14 (commit `6d343fabc`).
Verify against the live page — if the button is missing there, suspect a stale
embedded dash0 build rather than this route file.

## Proposal

### 1. Document both conventions in the design reference

In [design-reference.tsx](web/dash0/src/routes/orgs/$org/design-reference.tsx),
rendered like the existing `Section` blocks:

**"Button placement" section** (new), documenting with a live example:

- `PageHeader` `actions` slot: **primary page-level actions only** —
  typically the single "New <resource>" create button (plus at most one
  secondary page-level action). A page with **no** search toolbar keeps
  Refresh here too (the on-call case).
- Toolbar row below the header: **data/view controls** — search input,
  filters, and the Refresh button, with Refresh placed to the right of the
  search input.
- Row-level: the existing icon-button rules (`Pencil`/`Trash2`) already
  documented elsewhere on the page — cross-reference, don't duplicate.
- Show a live mini-example: a `PageHeader` with one primary action, above a
  search-input + Refresh toolbar row.

**"Empty state" subsection** (clarify the existing one at
[design-reference.tsx:2920-2938](web/dash0/src/routes/orgs/$org/design-reference.tsx:2920)):

- Canonical truly-empty state = same card surface as the table it replaces
  (`rounded-xl border bg-card p-12 text-center shadow-card space-y-3`),
  muted icon circle, `text-sm font-medium` title, `text-xs
  text-muted-foreground max-w-sm` hint, **and a primary CTA button**
  linking to the page's create route whenever the page has a create flow.
  Update the live example and its `importLine` snippet to include the button.
- Document the **no-matches variant**: same card, `Search` icon, title
  only, **no CTA** — used when rows exist but the search/filter hides them
  all (mirror `escalation-policies.index.tsx:193-201`).
- State that the toolbar row (search, Refresh) stays visible above the
  empty state, and cross-reference the new button-placement section.

### 2. Fix Refresh placement on the drifted pages

- **status-pages.index.tsx**: move the Refresh button out of `PageHeader`
  `actions` and into the search row, to the right of the search input —
  mirror the `integrations.index.tsx` layout (flex row, search input
  `flex-1 min-w-[200px] max-w-sm`, then the Refresh outline button). Keep the
  existing button markup (spinner on `isRefetching`, label hidden on mobile
  via `hidden sm:inline`). The "New status page" primary action stays in the
  header.
- **maintenance-windows.index.tsx**: same move (header → search row).
- **on-call.index.tsx**: move the Refresh button from its standalone
  `justify-end` row into the `PageHeader` `actions` slot next to "Create
  schedule" (outline variant left of the primary button), and delete the
  now-empty row. Keep `data-testid="oncall-refresh"` and the
  `aria-label={t("common:refresh")}`.
- Sweep any other `*.index.tsx` pages that render `RefreshCw` inside
  `PageHeader` `actions` while having a search toolbar, so the convention the
  reference documents is actually true everywhere.

### 3. Apply the clarified empty-state pattern to the divergent pages

Each gets the card-elevated empty state with icon, i18n'd title + hint, and a
CTA button pointing at its create route; pages with a search/kind filter also
get the no-matches variant where missing:

- `status-pages.index.tsx` (CTA → `/status-pages/new`; upgrade the
  existing bare no-match branch too),
- `status-updates.index.tsx` (CTA → `/status-updates/new`),
- `slos.index.tsx` (CTA → `/slos/new`; keep `data-testid="slo-empty"`),
- `me.notifications.tsx` (no create flow — CTA becomes a link/button to
  `/account/notifications`, keeping the current "configure how you get
  paged" intent; card-within-Card should be avoided: restyle the
  `CardContent` interior to the icon/title/hint stack instead of nesting a
  second card surface),
- `escalation-policies.index.tsx` (already conforming — only add the CTA
  button to the truly-empty branch),
- `on-call.index.tsx` (replace the hardcoded English hint with an i18n
  key alongside the existing `oncall:list.*` keys).

### 4. Translations, mobile, tests

- Add/adjust translations for every new title/hint/CTA string in each
  locale file touched (`oncall`, `statusPages`, `statusUpdates`, `slos`
  namespaces, etc.) — no hardcoded English in the empty states.
- Keep all pages mobile-usable (toolbar rows wrap, `hidden sm:inline` labels
  and touch targets preserved).
- Update any Playwright selectors that target the old Refresh placement or
  the old empty markup (`slo-empty` stays; grep `e2e/` for `noStatusPages`,
  `noStatusUpdates`, and empty-state text assertions). Keep
  `aria-label={t("common:refresh")}` so accessibility and existing lookups
  keep working.
