---
model: sonnet
effort: low
---

# The check detail page's documentation icon is not at the top right like on other pages

## Problem

On every page that uses the shared `PageHeader`
([web/dash0/src/components/shared/page-header.tsx:43-47](../../web/dash0/src/components/shared/page-header.tsx)),
the docs icon is rendered **after** the page's action buttons, making it the
rightmost element of the header — the consistent "top right" position users
learn across the app (checks list, incidents, status pages, SLOs, …).

The check detail page (`/dash0/orgs/$org/checks/$checkUid`, e.g.
`/orgs/superfc/checks/9246f00d-…` as reported) does not use `PageHeader`; it
builds its own header row. There, the `DocsLink` sits in the middle of the
action row
([checks.$checkUid.index.tsx:1007](../../web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx)):
back arrow → **DocsLink** → toolbar (Edit, Enable/Disable, Clone, Badges,
Publish, Refresh, Delete). So on this page the docs icon appears left of the
toolbar instead of at the far top right, breaking the cross-page convention.

## Proposal

Move the `<DocsLink href={docsHrefForType(check.type)} />` in
`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` so it is the
**last** element of the header's action container (the
`flex flex-wrap items-center justify-end gap-2` div starting at line 993),
i.e. after the inline toolbar `div`, mirroring `PageHeader`'s
`{actions}{docsHref && <DocsLink …/>}` ordering.

Notes:

- Keep the existing deep link per check type (`docsHrefForType(check.type)`)
  and the explanatory comment — only the position changes.
- The action row wraps on small screens (`flex-wrap justify-end`); verify on a
  mobile viewport that the icon still lands at the end of the row and doesn't
  wrap awkwardly on its own line.
- Check whether any other custom-header detail pages (e.g. an integration or
  status-page detail route that renders `DocsLink` outside `PageHeader`) have
  the same misplacement; fix them in the same pass if trivially similar,
  otherwise leave them and note it.
- E2E: if an existing check-detail Playwright spec asserts header layout,
  update it; otherwise a screenshot-level manual verification is enough for a
  pure reordering.
