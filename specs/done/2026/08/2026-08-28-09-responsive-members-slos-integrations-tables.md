---
model: sonnet
effort: medium
---

# Members, SLOs and integrations list tables side-scroll on mobile — apply the responsive column idiom

## Problem

Follow-up to `2026-08-28-08-responsive-checks-incidents-tables.md`, which
establishes (and documents in the design reference) the responsive-table
idiom: hide secondary columns with `hidden <bp>:table-cell` on the
`TableHead` + `TableCell` pair, keeping identity, primary status, and row
actions visible at every width. This spec applies it to the remaining three
column-heavy admin/list pages, none of which have any responsive classes
today:

- **Organization members**
  (`web/dash0/src/routes/orgs/$org/organization.members.tsx`, header at
  `:215-224`): member / email / role / coverage (conditional) / joined /
  actions.
- **SLOs** (`web/dash0/src/routes/orgs/$org/slos.index.tsx`, header at
  `:197-205`): name / scope / target / attainment / budget / state / actions.
- **Integrations** (`web/dash0/src/routes/orgs/$org/integrations.index.tsx`,
  header at `:208-215`): name / type / status / used-by / updated / actions.

On a phone each of these falls back to horizontal scrolling, violating the
CLAUDE.md rule that all pages must be fully usable on mobile.

## Proposal

Suggested tiers below — the implementer should sanity-check against real
seeded content at 375px and adjust breakpoints if a page still overflows; the
invariant is **no horizontal scroll at 375px, with identity + primary
status/signal + actions always visible**.

**Members** — mobile keeps member / role / actions:

| Column | Behavior |
|---|---|
| Member | always visible |
| Email | `hidden md:table-cell` |
| Role | always visible |
| Coverage | `hidden lg:table-cell` (column is already conditionally rendered — keep that condition, add the class) |
| Joined | `hidden lg:table-cell` |
| Actions | always visible |

**SLOs** — mobile keeps name / attainment / state / actions:

| Column | Behavior |
|---|---|
| Name | always visible |
| Scope | `hidden md:table-cell` |
| Target | `hidden sm:table-cell` (static config; attainment is the live number) |
| Attainment | always visible |
| Budget | `hidden md:table-cell` |
| State | always visible |
| Actions | always visible |

**Integrations** — mobile keeps name / status / actions:

| Column | Behavior |
|---|---|
| Name | always visible |
| Type | `hidden sm:table-cell` |
| Status | always visible |
| Used by | `hidden md:table-cell` |
| Updated | `hidden lg:table-cell` |
| Actions | always visible |

Mechanics:

- Always apply the class to the head **and** every matching body cell —
  a mismatched pair shifts all following columns under the wrong headers.
- None of these three tables use `colSpan` today, so no colSpan bookkeeping
  is needed.
- If a first-column cell still forces overflow (long email-as-name, long SLO
  name), constrain it with `max-w-* truncate` rather than reintroducing
  page scroll.

**QA**

- Playwright at a 375px viewport asserting no horizontal document scroll on
  all three pages with seeded data, and that the always-visible columns and
  row actions render.
- Existing desktop-viewport E2E suites stay green (hidden columns are
  unaffected at the default viewport).
- No new dash0 ESLint errors (base carries known pre-existing debt).
