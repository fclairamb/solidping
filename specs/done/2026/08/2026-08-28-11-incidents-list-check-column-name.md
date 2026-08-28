---
model: sonnet
effort: low
---

# The Incidents list "Check" column shows the check slug instead of the check name

## Problem

On `/dash0/orgs/$org/incidents` (e.g.
`http://localhost:4000/dash0/orgs/default/incidents?state=active`) the **Check**
column renders the check *slug*, not its display *name*. The fallback order is
inverted relative to every other incident surface:

[`web/dash0/src/routes/orgs/$org/incidents.index.tsx:409`](web/dash0/src/routes/orgs/$org/incidents.index.tsx#L409)

```tsx
{incident.checkSlug || incident.checkName}
```

Everywhere else the name wins and the slug is only the degradation path:

- [`incidents.index.tsx:346-348`](web/dash0/src/routes/orgs/$org/incidents.index.tsx#L346) — the incident title cell in the very same table: `incident.title || incident.checkName || incident.checkSlug`
- [`incidents.$incidentUid.tsx:894-896`](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx#L894) and [`:1075`](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx#L1075), [`:1253`](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx#L1253), [`:1683`](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx#L1683) — all `checkName || checkSlug`
- [`orgs/$org.tsx:287`](web/dash0/src/routes/orgs/$org.tsx#L287) and [`dashboard-page.tsx:1136`](web/dash0/src/components/dashboard/dashboard-page.tsx#L1136) — name first

The result (see the screenshot in the report) is a row whose title cell shows a
truncated `http-test-…` name while the Check column next to it shows the slug
`http-test-api` — two spellings of the same thing, and the slug is the less
readable one.

No API work is needed: the list query already asks for the enrichment
(`with: "check"`, [`incidents.index.tsx:172`](web/dash0/src/routes/orgs/$org/incidents.index.tsx#L172)), and the
handler returns both `checkName` and `checkSlug` behind that flag
(`server/internal/handlers/incidents/enrichment_test.go:154-183`).

## Proposal

Swap the fallback order in the Check column so the display name wins:

```tsx
{incident.checkName || incident.checkSlug}
```

Notes for the implementation:

- Keep the slug as the fallback — `checks.name` is nullable, so a check with no
  name must still render something rather than an empty cell. If both are absent
  (e.g. `with=check` enrichment withheld), the existing behaviour of an empty
  cell is acceptable; do not introduce a new placeholder string.
- The link target (`/orgs/$org/checks/$checkUid`) is unchanged — only the label.
- This is the only remaining inverted site; a `grep -rn 'checkSlug' web/dash0/src`
  after the change should show no other `checkSlug || checkName` ordering.

### Tests

Add coverage in the dash0 Playwright suite (`web/dash0/e2e/`) asserting that,
for an incident whose check has a name distinct from its slug, the Check column
renders the **name**. `web/dash0/e2e/incident-group-header.spec.ts:45-51` seeds
incidents with `checkName` equal to `checkSlug`, which cannot distinguish the
two — the new fixture must use different values (e.g. slug `http-test-api`,
name `Payments API`) so the assertion actually proves the ordering.
