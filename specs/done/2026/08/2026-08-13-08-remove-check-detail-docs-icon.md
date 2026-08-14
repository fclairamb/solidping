---
model: sonnet
effort: low
---

# Remove the documentation icon from the check detail page

## Problem

The check detail page (`/orgs/$org/checks/$checkUid`, e.g.
https://solidping.k8xp.com/dash0/orgs/stonal/checks/c607678d-d713-429a-add6-ff927753d102)
renders a `DocsLink` icon (`BookOpen` → `/docs/features/check-types`) in its
header toolbar, between the back arrow and the Edit/Pause buttons:

- `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:930` — `<DocsLink href="/docs/features/check-types" />`

The check **edit** page already shows the same docs link via the shared check
form (`web/dash0/src/components/shared/check-form.tsx:852`), which is where a
user actually configures check types and needs the documentation. On the
detail page the icon is redundant toolbar noise — the one in `/edit` is
enough.

## Proposal

1. Remove the `<DocsLink href="/docs/features/check-types" />` line from
   `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:930`.
2. Remove the now-unused `DocsLink` import at
   `web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:76` (lint fails
   on unused imports).
3. Leave everything else untouched: the checks **list** page docs link, the
   check form (edit/new) docs link, and the `DocsLink` primitive itself all
   stay.

No test changes required: `web/dash0/e2e/docs-links.spec.ts` asserts the
checks *list* and status-pages links plus an unmapped page, and never visits
the check detail page. Optionally, a `toHaveCount(0)` assertion on the detail
page could be added there, but it's not required for this removal.
